package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MessagesReader only invokes SQLite read-only queries and imsg's messages.after.
// It never invokes the bridge, a send method or an app/permission action.
type MessagesReader struct {
	Binary   string
	Database string
	run      func(context.Context, string, []string, []byte) ([]byte, error)
}

type MessageRow struct {
	ID        int64  `json:"id"`
	GUID      string `json:"guid"`
	ChatID    int64  `json:"chat_id"`
	Edited    int64  `json:"edited"`
	Retracted int64  `json:"retracted"`
	TextBytes int64  `json:"text_bytes"`
	BodyBytes int64  `json:"body_bytes"`
}

func (r MessagesReader) command(ctx context.Context, binary string, args []string, input []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if r.run != nil {
		return r.run(ctx, binary, args, input)
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	// A pipe/EOF, never the terminal: upstream must not request Contacts access.
	cmd.Stdin = bytes.NewReader(input)
	var stdout boundedMessageOutput
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard // Source diagnostics may contain private message data.
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("Messages reader: %w", ctx.Err())
		}
		return nil, fmt.Errorf("Messages reader %s failed: %w", filepath.Base(binary), err)
	}
	return stdout.buffer.Bytes(), nil
}

type boundedMessageOutput struct{ buffer bytes.Buffer }

func (b *boundedMessageOutput) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 16<<20 {
		return 0, errors.New("Messages reader output exceeds 16 MiB")
	}
	return b.buffer.Write(p)
}

func (r MessagesReader) validate() error {
	if !filepath.IsAbs(r.Binary) || !filepath.IsAbs(r.Database) {
		return errors.New("Messages reader and database must be explicit absolute paths")
	}
	return nil
}

func (r MessagesReader) Baseline(ctx context.Context) (MessageRow, error) {
	if err := r.validate(); err != nil {
		return MessageRow{}, err
	}
	version, err := r.command(ctx, r.Binary, []string{"--version"}, nil)
	if err != nil {
		return MessageRow{}, err
	}
	if strings.TrimSpace(string(version)) != "0.15.2" {
		return MessageRow{}, errors.New("Messages reader must be the verified imsg 0.15.2 build")
	}
	data, err := r.command(ctx, "/usr/bin/sqlite3", []string{"-readonly", "-json", r.Database,
		"SELECT COALESCE(MAX(ROWID),0) AS id, COALESCE((SELECT guid FROM message ORDER BY ROWID DESC LIMIT 1),'') AS guid FROM message;"}, nil)
	if err != nil {
		return MessageRow{}, err
	}
	var rows []MessageRow
	if err := json.Unmarshal(data, &rows); err != nil || len(rows) != 1 || rows[0].ID < 0 || (rows[0].ID > 0 && rows[0].GUID == "") {
		return MessageRow{}, errors.New("Messages baseline returned an invalid identity")
	}
	return rows[0], nil
}

// Page includes the original anchor in every bounded query, at most 501 rows total.
// Read receipts are deliberately absent from the fingerprint.
func (r MessagesReader) Page(ctx context.Context, baseline int64, anchorGUID string, after, high int64) ([]MessageRow, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	if baseline < 0 {
		return nil, errors.New("Messages baseline must be nonnegative")
	}
	query := `SELECT m.ROWID AS id, m.guid,
 COALESCE((SELECT chat_id FROM chat_message_join WHERE message_id=m.ROWID ORDER BY chat_id LIMIT 1),0) AS chat_id,
 COALESCE(m.date_edited,0) AS edited, COALESCE(m.date_retracted,0) AS retracted,
 COALESCE(length(CAST(m.text AS BLOB)),0) AS text_bytes, COALESCE(length(m.attributedBody),0) AS body_bytes
 FROM message m WHERE m.ROWID = ` + strconv.FormatInt(baseline, 10) + ` OR (m.ROWID > ` + strconv.FormatInt(after, 10) + ` AND m.ROWID <= ` + strconv.FormatInt(high, 10) + `) ORDER BY m.ROWID LIMIT 501;`
	data, err := r.command(ctx, "/usr/bin/sqlite3", []string{"-readonly", "-json", r.Database, query}, nil)
	if err != nil {
		return nil, err
	}
	rows := []MessageRow{}
	if len(bytes.TrimSpace(data)) > 0 { // sqlite3 emits no bytes for an empty result.
		if err := json.Unmarshal(data, &rows); err != nil {
			return nil, errors.New("Messages metadata returned invalid JSON")
		}
	}
	if baseline > 0 {
		if len(rows) == 0 || rows[0].ID != baseline || rows[0].GUID != anchorGUID {
			return nil, errors.New("Messages baseline anchor changed or was removed; coverage requires an explicit new baseline")
		}
		rows = rows[1:]
	}
	previous := after
	for _, row := range rows {
		if row.ID <= previous || row.ID > high || row.GUID == "" || row.ChatID <= 0 {
			return nil, errors.New("Messages metadata is incomplete; retry after source synchronization")
		}
		previous = row.ID
	}
	return rows, nil
}

func (r MessagesReader) Message(ctx context.Context, row MessageRow) (json.RawMessage, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	if row.ID <= 0 || row.GUID == "" || row.ChatID <= 0 {
		return nil, errors.New("Messages identity is incomplete")
	}
	request := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  struct {
			Since       int64 `json:"since_rowid"`
			Limit       int   `json:"limit"`
			Attachments bool  `json:"attachments"`
			Reactions   bool  `json:"include_reactions"`
		} `json:"params"`
	}{JSONRPC: "2.0", ID: 1, Method: "messages.after"}
	request.Params.Since, request.Params.Limit = row.ID-1, 1
	request.Params.Attachments, request.Params.Reactions = true, true
	input, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	data, err := r.command(ctx, r.Binary, []string{"rpc", "--db", r.Database}, append(input, '\n'))
	if err != nil {
		return nil, err
	}
	var response struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Error   json.RawMessage `json:"error"`
		Result  struct {
			Messages []json.RawMessage `json:"messages"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &response); err != nil || response.JSONRPC != "2.0" || response.ID != 1 {
		return nil, errors.New("Messages reader returned an invalid RPC response")
	}
	if len(response.Error) > 0 && string(response.Error) != "null" {
		return nil, errors.New("Messages read RPC failed; source coverage is not current")
	}
	if len(response.Result.Messages) != 1 {
		return nil, errors.New("Messages reader did not return the requested record")
	}
	message := response.Result.Messages[0]
	var identity struct {
		ID     int64  `json:"id"`
		GUID   string `json:"guid"`
		ChatID int64  `json:"chat_id"`
	}
	if err := json.Unmarshal(message, &identity); err != nil || identity.ID != row.ID || identity.GUID != row.GUID || identity.ChatID != row.ChatID {
		return nil, errors.New("Messages record changed or was removed during capture; retry")
	}
	return message, nil
}

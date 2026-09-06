package plaud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Client performs the read-only data calls against the developer API.
type Client struct {
	Session Session
}

// File is the subset of a recording the CLI acts on; Raw keeps the whole
// object for --json.
type File struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	CreatedAt  string          `json:"created_at"`
	Duration   float64         `json:"duration"`
	SourceList []Block         `json:"source_list"`
	NoteList   json.RawMessage `json:"note_list"`
	Raw        map[string]any  `json:"-"`
}

// Block is one derived artifact of a recording (transcript, outline, …);
// content is inline or behind a presigned link.
type Block struct {
	DataType    string `json:"data_type"`
	DataContent string `json:"data_content"`
	DataLink    string `json:"data_link"`
}

// FileList is one page of recordings; the filtered form also reports how
// much of the account was scanned.
type FileList struct {
	Data      []map[string]any `json:"data"`
	Scanned   int              `json:"scanned,omitempty"`
	Matched   int              `json:"matched,omitempty"`
	Truncated bool             `json:"truncated,omitempty"`
	Raw       map[string]any   `json:"-"`
}

// ListOptions mirror the MCP's list_files: page/page_size for plain paging,
// Query for a case-insensitive name substring scan (up to filterPages ×
// filterPageSize recordings, like the MCP did).
type ListOptions struct {
	Page     int
	PageSize int
	Query    string
}

const (
	filterPages    = 5
	filterPageSize = 100
)

func (c Client) get(ctx context.Context, path string, out any) error {
	cfg := c.Session.Config.withDefaults()
	body, err := c.getBytes(ctx, cfg, path, true)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// getBytes performs an authenticated GET; one 401 clears the access-token
// cache and retries so a stale cache never surfaces as an error.
func (c Client) getBytes(ctx context.Context, cfg Config, path string, retry bool) ([]byte, error) {
	token, err := c.Session.AccessToken(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.APIBase+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	body, err := cfg.do(req, "GET "+path)
	var status *StatusError
	if errors.As(err, &status) && status.Code == http.StatusUnauthorized && retry {
		if clearErr := ClearCache(cfg); clearErr != nil {
			return nil, clearErr
		}
		return c.getBytes(ctx, cfg, path, false)
	}
	return body, err
}

// CurrentUser returns the authenticated account as the API describes it.
func (c Client) CurrentUser(ctx context.Context) (map[string]any, error) {
	var user map[string]any
	if err := c.get(ctx, "/open/third-party/users/current", &user); err != nil {
		return nil, err
	}
	return user, nil
}

// ListFiles pages recordings, or scans for a name match when Query is set.
func (c Client) ListFiles(ctx context.Context, opts ListOptions) (FileList, error) {
	if opts.Page <= 0 {
		opts.Page = 1
	}
	if opts.PageSize <= 0 {
		opts.PageSize = 20
	}
	if opts.Query == "" {
		return c.listPage(ctx, opts.Page, opts.PageSize)
	}
	needle := strings.ToLower(opts.Query)
	result := FileList{Data: []map[string]any{}}
	for page := 1; page <= filterPages; page++ {
		chunk, err := c.listPage(ctx, page, filterPageSize)
		if err != nil {
			return FileList{}, err
		}
		result.Scanned += len(chunk.Data)
		for _, item := range chunk.Data {
			name, _ := item["name"].(string)
			if strings.Contains(strings.ToLower(name), needle) {
				result.Data = append(result.Data, item)
			}
		}
		if len(chunk.Data) < filterPageSize {
			break
		}
		if page == filterPages {
			result.Truncated = true
		}
	}
	result.Matched = len(result.Data)
	return result, nil
}

func (c Client) listPage(ctx context.Context, page, size int) (FileList, error) {
	path := "/open/third-party/files/?" + url.Values{"page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(size)}}.Encode()
	var raw map[string]any
	if err := c.get(ctx, path, &raw); err != nil {
		return FileList{}, err
	}
	list := FileList{Raw: raw}
	if items, ok := raw["data"].([]any); ok {
		for _, item := range items {
			if m, ok := item.(map[string]any); ok {
				list.Data = append(list.Data, m)
			}
		}
	}
	return list, nil
}

// GetFile fetches one recording with its derived blocks and notes.
func (c Client) GetFile(ctx context.Context, id string) (File, error) {
	cfg := c.Session.Config.withDefaults()
	body, err := c.getBytes(ctx, cfg, "/open/third-party/files/"+url.PathEscape(id), true)
	if err != nil {
		return File{}, err
	}
	var file File
	if err := json.Unmarshal(body, &file); err != nil {
		return File{}, fmt.Errorf("file %s: %w", id, err)
	}
	if err := json.Unmarshal(body, &file.Raw); err != nil {
		return File{}, fmt.Errorf("file %s: %w", id, err)
	}
	return file, nil
}

// TranscriptBlocks are the source_list data types that carry speech.
var TranscriptBlocks = []string{"transaction", "transaction_polish", "outline"}

// Transcript is one block's content: Segments when the block is a JSON
// utterance list, Text otherwise. Unlike the MCP, the whole block is
// returned — pagination was a client-size artefact, not an API one.
type Transcript struct {
	FileID    string           `json:"file_id"`
	Block     string           `json:"block"`
	Available []string         `json:"available_blocks"`
	Total     int              `json:"total"`
	Segments  []map[string]any `json:"segments,omitempty"`
	Text      string           `json:"text,omitempty"`
}

// ErrBlockUnavailable is returned when the recording has no such block yet.
var ErrBlockUnavailable = errors.New("block not available for this recording")

// Transcript loads one transcript block (default "transaction": raw speech
// with speaker names and timestamps).
func (c Client) Transcript(ctx context.Context, id, block string) (Transcript, error) {
	if block == "" {
		block = TranscriptBlocks[0]
	}
	file, err := c.GetFile(ctx, id)
	if err != nil {
		return Transcript{}, err
	}
	out := Transcript{FileID: id, Block: block}
	var selected *Block
	for i := range file.SourceList {
		if t := file.SourceList[i].DataType; t != "" {
			out.Available = append(out.Available, t)
			if t == block {
				selected = &file.SourceList[i]
			}
		}
	}
	if selected == nil {
		return out, fmt.Errorf("%w: %q (available: %s)", ErrBlockUnavailable, block, strings.Join(out.Available, ", "))
	}
	content := selected.DataContent
	if content == "" && selected.DataLink != "" {
		cfg := c.Session.Config.withDefaults()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, selected.DataLink, nil)
		if err != nil {
			return out, err
		}
		body, err := cfg.do(req, "fetch block "+block)
		if err != nil {
			return out, err
		}
		content = string(body)
	}
	var segments []map[string]any
	if json.Unmarshal([]byte(content), &segments) == nil && segments != nil {
		out.Segments = segments
		out.Total = len(segments)
		return out, nil
	}
	out.Text = content
	return out, nil
}

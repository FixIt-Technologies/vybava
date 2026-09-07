package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func mockMessagesMetadata(query string, anchor MessageRow, rows []MessageRow) ([]byte, error) {
	if strings.Contains(query, "MAX(ROWID)") {
		last := anchor
		for _, row := range rows {
			if row.ID > last.ID {
				last = row
			}
		}
		return json.Marshal([]MessageRow{last})
	}
	match := regexp.MustCompile(`m.ROWID > (\d+) AND m.ROWID <= (\d+)`).FindStringSubmatch(query)
	if len(match) != 3 {
		return nil, errors.New("unexpected query")
	}
	after, _ := strconv.ParseInt(match[1], 10, 64)
	high, _ := strconv.ParseInt(match[2], 10, 64)
	result := []MessageRow{}
	if anchor.ID > 0 {
		result = append(result, anchor)
	}
	for _, row := range rows {
		if row.ID > after && row.ID <= high && len(result) < 501 {
			result = append(result, row)
		}
	}
	return json.Marshal(result)
}

func TestMessagesBaselineChangesRemovalAndRestart(t *testing.T) {
	s := testStore(t)
	rows := []MessageRow{}
	text := "first body"
	failed := false
	reads := 0
	r := MessagesReader{Binary: "/verified/imsg", Database: "/messages/chat.db"}
	r.run = func(_ context.Context, binary string, args []string, input []byte) ([]byte, error) {
		if args[0] == "--version" {
			return []byte("0.15.2\n"), nil
		}
		if binary == "/usr/bin/sqlite3" {
			if args[0] != "-readonly" {
				t.Fatal("source database not read-only")
			}
			return mockMessagesMetadata(args[3], MessageRow{ID: 10, GUID: "baseline-guid"}, rows)
		}
		reads++
		if failed {
			return nil, errors.New("reader unavailable")
		}
		var req struct {
			Method string
			Params struct {
				Since int64 `json:"since_rowid"`
			}
		}
		if err := json.Unmarshal(input, &req); err != nil {
			t.Fatal(err)
		}
		if req.Method != "messages.after" || req.Params.Since != 10 || args[0] != "rpc" {
			t.Fatal("unexpected RPC method or identity")
		}
		return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"messages": []map[string]any{{"id": 11, "guid": "message-guid", "chat_id": 7, "text": text}}}})
	}
	scan := func(want int) {
		t.Helper()
		ids, err := s.ScanMessages(context.Background(), r)
		if err != nil || len(ids) != want {
			t.Fatalf("scan: %v %v; wanted %d", ids, err, want)
		}
	}
	scan(0) // Baseline old history, never read historical message bodies.
	if reads != 0 {
		t.Fatal("baseline read old messages")
	}
	rows = []MessageRow{{ID: 11, GUID: "message-guid", ChatID: 7, BodyBytes: 100}}
	scan(1)
	s = Store{Dir: s.Dir} // Restart reuses persisted fingerprints.
	scan(0)
	if reads != 1 {
		t.Fatal("unchanged message was reread")
	}
	rows[0].Edited = 12
	text = "edited body"
	failed = true
	scan(0)
	if err := s.View(func(state *State) error {
		if state.Messages.Coverage.Error == "" || state.Messages.Records[11].Edited != 0 {
			t.Fatal("failed capture advanced state")
		}
		if state.LastScanAt != nil {
			t.Fatal("Messages check certified the agent scanner")
		}
		if state.Propose(state.Events[0].ID, "draft") == nil {
			t.Fatal("incomplete coverage allowed a fresh proposal")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	failed = false
	scan(1)
	rows[0].Retracted = 13
	scan(1)
	if reads != 3 {
		t.Fatalf("retraction must not read a removed body: %d", reads)
	}
	rows = nil
	scan(1)
	scan(0) // Local removal is recorded once.
	rows = []MessageRow{{ID: 11, GUID: "message-guid", ChatID: 7, BodyBytes: 100}}
	scan(1) // Restoring an identical row must become current again.
	if err := s.View(func(state *State) error {
		if state.Version != 4 || len(state.Events) != 5 || state.Events[4].Superseded {
			t.Fatal("restored capture missing")
		}
		for _, event := range state.Events[:4] {
			if !event.Superseded {
				t.Fatal("older conversation context stayed current")
			}
		}
		if state.Messages.Coverage.Error != "" || state.Messages.Coverage.LastSuccessAt == nil {
			t.Fatal("coverage did not recover")
		}
		if len(state.Snapshot().Sources) != 1 {
			t.Fatal("companion missing source coverage")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMessagesCatchupIsBoundedAndDoesNotDeleteDeferredRecords(t *testing.T) {
	s := testStore(t)
	r := MessagesReader{Binary: "/verified/imsg", Database: "/messages/chat.db"}
	rows := []MessageRow{}
	r.run = func(_ context.Context, binary string, args []string, input []byte) ([]byte, error) {
		if args[0] == "--version" {
			return []byte("0.15.2\n"), nil
		}
		if binary == "/usr/bin/sqlite3" {
			return mockMessagesMetadata(args[3], MessageRow{}, rows)
		}
		var req struct {
			Params struct {
				Since int64 `json:"since_rowid"`
			}
		}
		if err := json.Unmarshal(input, &req); err != nil {
			t.Fatal(err)
		}
		return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"messages": []map[string]any{{"id": req.Params.Since + 1, "guid": fmt.Sprint(req.Params.Since + 1), "chat_id": 7}}}})
	}
	for _, want := range []int{0, 25, 2, 0} {
		ids, err := s.ScanMessages(context.Background(), r)
		if err != nil || len(ids) != want {
			t.Fatalf("capture got %d %v, wanted %d", len(ids), err, want)
		}
		if len(rows) == 0 {
			for i := 1; i <= 27; i++ {
				rows = append(rows, MessageRow{ID: int64(i), GUID: fmt.Sprint(i), ChatID: 7})
			}
		}
	}
	if err := s.View(func(state *State) error {
		if len(state.Messages.Records) != 27 || state.Messages.Coverage.Error != "" {
			t.Fatal("catch-up lost records or failed to recover")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMessagesReaderRejectsWrongRecordAndMalformedResponse(t *testing.T) {
	r := MessagesReader{Binary: "/verified/imsg", Database: "/messages/chat.db"}
	for _, response := range []string{
		`{"jsonrpc":"2.0","id":1,"result":{"messages":[{"id":12,"guid":"other","chat_id":7}]}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"message":"private source content"}}`,
		`not json`,
	} {
		r.run = func(context.Context, string, []string, []byte) ([]byte, error) { return []byte(response), nil }
		_, err := r.Message(context.Background(), MessageRow{ID: 11, GUID: "expected", ChatID: 7})
		if err == nil || strings.Contains(err.Error(), "private source content") {
			t.Fatalf("unsafe response handling: %v", err)
		}
	}
}

func TestMessagesReaderRejectsChangedOrMissingBaselineAnchor(t *testing.T) {
	r := MessagesReader{Binary: "/verified/imsg", Database: "/messages/chat.db"}
	for _, response := range []string{"", `[{"id":10,"guid":"replacement"}]`, `[{"id":11,"guid":"later","chat_id":7}]`} {
		r.run = func(context.Context, string, []string, []byte) ([]byte, error) { return []byte(response), nil }
		if _, err := r.Page(context.Background(), 10, "baseline-guid", 10, 11); err == nil {
			t.Fatal("changed source anchor accepted as current")
		}
	}
}

func TestMessagesApprovalRequiresCurrentCoverageButDeclineRemainsAvailable(t *testing.T) {
	now := time.Now()
	state := State{Messages: &MessagesState{Initialized: true, Coverage: SourceCoverage{LastSuccessAt: &now}}}
	id, _, err := state.Observe(Observation{Source: Messages, Key: "chat", Revision: "1", Text: "synthetic context"})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Propose(id, "synthetic draft"); err != nil {
		t.Fatal(err)
	}
	stale := now.Add(-3 * time.Minute)
	state.Messages.Coverage.LastSuccessAt = &stale
	if state.Propose(id, "another draft") == nil || state.Decide(id, 1, "approve", "") == nil {
		t.Fatal("stale coverage accepted")
	}
	if err := state.Decide(id, 1, "reject", ""); err != nil {
		t.Fatal(err)
	}
}

func TestMessagesInitialFailureKeepsCoverageAndRetriesBaseline(t *testing.T) {
	s := testStore(t)
	fail := true
	r := MessagesReader{Binary: "/verified/imsg", Database: "/messages/chat.db"}
	r.run = func(_ context.Context, _ string, args []string, _ []byte) ([]byte, error) {
		if args[0] == "--version" {
			return []byte("0.15.2\n"), nil
		}
		if fail {
			return nil, errors.New("database unavailable")
		}
		return []byte(`[{"id":0}]`), nil
	}
	if _, err := s.ScanMessages(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if err := s.View(func(state *State) error {
		if state.Messages.Initialized || state.Messages.Coverage.Error == "" || state.Messages.Coverage.LastSuccessAt != nil {
			t.Fatal("initial failure presented as current coverage")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fail = false
	if _, err := s.ScanMessages(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if err := s.View(func(state *State) error {
		if !state.Messages.Initialized || state.Messages.Baseline != 0 || state.Messages.Coverage.Error != "" {
			t.Fatal("empty-database baseline did not recover")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMessagesPagedSweepFairRetriesAndBoundedRemoval(t *testing.T) {
	s := testStore(t)
	rows := []MessageRow{}
	version := "0.15.2"
	failFront := true
	metadataFailure := false
	r := MessagesReader{Binary: "/verified/imsg", Database: "/messages/chat.db"}
	r.run = func(_ context.Context, binary string, args []string, input []byte) ([]byte, error) {
		if args[0] == "--version" {
			return []byte(version), nil
		}
		if binary == "/usr/bin/sqlite3" {
			if metadataFailure && strings.Contains(args[3], "m.ROWID > 501 ") {
				return nil, errors.New("metadata unavailable")
			}
			return mockMessagesMetadata(args[3], MessageRow{}, rows)
		}
		var request struct {
			Params struct {
				Since int64 `json:"since_rowid"`
			}
		}
		if err := json.Unmarshal(input, &request); err != nil {
			t.Fatal(err)
		}
		id := request.Params.Since + 1
		if failFront && id <= 25 {
			return nil, errors.New("unreadable message")
		}
		return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":{"messages":[{"id":%d,"guid":"%d","chat_id":7}]}}`, id, id)), nil
	}
	scan := func() int {
		t.Helper()
		ids, err := s.ScanMessages(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		return len(ids)
	}
	scan()
	for i := 1; i <= 530; i++ {
		rows = append(rows, MessageRow{ID: int64(i), GUID: fmt.Sprint(i), ChatID: 7})
	}
	if scan() != 0 {
		t.Fatal("failed front unexpectedly captured")
	}
	s = Store{Dir: s.Dir}
	if scan() != 25 {
		t.Fatal("failed front starved later rows after restart")
	}
	version = "wrong"
	scan()
	if err := s.View(func(state *State) error {
		if state.Messages.Coverage.Error == "" || state.Messages.Sweep.After != 50 {
			t.Fatal("restart accepted unverified reader")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	version = "0.15.2"
	for i := 0; i < 22; i++ {
		scan()
	}
	failFront = false
	for i := 0; i < 3; i++ {
		scan()
	}
	if err := s.View(func(state *State) error {
		if len(state.Messages.Records) != 530 || state.Messages.Coverage.Error != "" {
			t.Fatal("fair retry did not recover")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// An error after one full metadata page must retain every record and freshness.
	metadataFailure = true
	scan()
	if err := s.View(func(state *State) error {
		if len(state.Messages.Records) != 530 || state.Messages.Sweep == nil || state.Messages.Sweep.After != 501 || state.Messages.Coverage.Error == "" {
			t.Fatal("incomplete metadata sweep certified removal or coverage")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	metadataFailure = false
	scan()
	rows = nil
	if scan() != 25 {
		t.Fatal("removals were not bounded")
	}
	s = Store{Dir: s.Dir}
	// A record restored between removal passes must not be declared removed.
	rows = []MessageRow{{ID: 26, GUID: "26", ChatID: 7}}
	if scan() != 24 {
		t.Fatal("restored row was removed or removal resumption failed")
	}
	if err := s.View(func(state *State) error {
		if _, ok := state.Messages.Records[26]; !ok {
			t.Fatal("restored row deleted")
		}
		if state.Messages.Coverage.Error == "" || state.Messages.Sweep == nil {
			t.Fatal("deferred removals certified complete")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMessagesRestartCannotRefreshStaleSeenRows(t *testing.T) {
	s := testStore(t)
	rows := []MessageRow{}
	r := MessagesReader{Binary: "/verified/imsg", Database: "/messages/chat.db"}
	r.run = func(_ context.Context, _ string, args []string, _ []byte) ([]byte, error) {
		if args[0] == "--version" {
			return []byte("0.15.2"), nil
		}
		return mockMessagesMetadata(args[3], MessageRow{}, rows)
	}
	scan := func(want int) {
		t.Helper()
		ids, err := s.ScanMessages(context.Background(), r)
		if err != nil || len(ids) != want {
			t.Fatalf("capture got %d %v, wanted %d", len(ids), err, want)
		}
	}
	scan(0)
	for i := 1; i <= 26; i++ {
		rows = append(rows, MessageRow{ID: int64(i), GUID: fmt.Sprint(i), ChatID: 7, Retracted: 1})
	}
	scan(25)
	var priorSuccess time.Time
	if err := s.With(func(state *State) error {
		priorSuccess = *state.Messages.Coverage.LastSuccessAt
		state.Messages.Sweep.StartedAt = time.Now().Add(-10 * time.Minute)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rows = rows[1:] // A previously seen row disappears, without changing MAX(ROWID).
	s = Store{Dir: s.Dir}
	scan(1)
	if err := s.View(func(state *State) error {
		m := state.Messages
		if m.Coverage.Error == "" || !m.Coverage.LastSuccessAt.Equal(priorSuccess) || m.Sweep != nil {
			t.Fatal("finishing an old sweep certified stale seen rows as fresh")
		}
		if state.Propose(state.Events[len(state.Events)-1].ID, "draft") == nil {
			t.Fatal("stale sweep allowed proposal")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	scan(1) // A fresh sweep rechecks the old prefix and observes the removal.
	if err := s.View(func(state *State) error {
		if _, ok := state.Messages.Records[1]; ok {
			t.Fatal("new sweep missed removal")
		}
		if state.Messages.Coverage.Error != "" {
			t.Fatal("new sweep failed to recover")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

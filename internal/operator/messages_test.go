package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

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
			if strings.Contains(args[3], "MAX(ROWID)") {
				return []byte(`[{"id":10,"guid":"baseline-guid"}]`), nil
			}
			return json.Marshal(append([]MessageRow{{ID: 10, GUID: "baseline-guid"}}, rows...))
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
	rows := make([]MessageRow, 27)
	for i := range rows {
		rows[i] = MessageRow{ID: int64(i + 1), GUID: fmt.Sprint(i + 1), ChatID: 7}
	}
	r.run = func(_ context.Context, binary string, args []string, input []byte) ([]byte, error) {
		if args[0] == "--version" {
			return []byte("0.15.2\n"), nil
		}
		if binary == "/usr/bin/sqlite3" {
			if strings.Contains(args[3], "MAX(ROWID)") {
				return []byte(`[{"id":0}]`), nil
			}
			return json.Marshal(rows)
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
		if _, err := r.Rows(context.Background(), 10, "baseline-guid"); err == nil {
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

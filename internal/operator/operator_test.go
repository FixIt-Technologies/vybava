package operator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testStore(t *testing.T) Store {
	t.Helper()
	return Store{Dir: filepath.Join(t.TempDir(), "private")}
}

func addObservation(t *testing.T, s Store, revision string) string {
	t.Helper()
	var id string
	if err := s.With(func(state *State) error {
		var err error
		id, _, err = state.Observe(Observation{Source: WhatsApp, Key: "test-conversation", Revision: revision, Text: "Can we meet at ten?"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestChangedContextRetiresDraftAndKeepsHumanScores(t *testing.T) {
	s := testStore(t)
	id := addObservation(t, s, "message-1")
	if err := s.With(func(state *State) error {
		if err := state.Propose(id, "Ten works for me."); err != nil {
			return err
		}
		if err := state.Rate(id, 1, 4, "Use 10:30 instead."); err != nil {
			return err
		}
		if err := state.Rate(id, 1, 5, ""); err == nil {
			t.Fatal("overwrote a human score")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	newID := addObservation(t, s, "message-2")
	if err := s.With(func(state *State) error {
		old, _ := state.Find(id)
		if !old.Superseded || !old.Proposals[0].Superseded || old.Proposals[0].Rating.Correction != "Use 10:30 instead." {
			t.Fatal("lost stale status or feedback")
		}
		if err := state.Propose(id, "stale reply"); err == nil {
			t.Fatal("allowed stale draft")
		}
		if err := state.Propose(newID, "Fresh reply"); err != nil {
			return err
		}
		summary := state.Summary()
		if summary.Scored != 1 || summary.Proposals != 2 || summary.Average != 4 || summary.Sending != "human-only" {
			t.Fatalf("incorrect evaluation: %+v", summary)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestQueueReceiptIsNotAcknowledgementAndNeverReplays(t *testing.T) {
	for _, fails := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "ambiguous-failure"}[fails], func(t *testing.T) {
			s := testStore(t)
			id := addObservation(t, s, "queue")
			calls := 0
			queue := func(_ context.Context, thread, message string) (string, error) {
				calls++
				if thread != "operator-thread" || !strings.Contains(message, id) || !strings.Contains(message, "untrusted") || strings.Contains(message, "Can we meet") {
					t.Fatal("bad queue envelope")
				}
				if fails {
					return "", errors.New("connection lost after submission")
				}
				return "Queued message receipt-1", nil
			}
			err := s.Deliver(context.Background(), id, "operator-thread", queue)
			if (err != nil) != fails {
				t.Fatalf("delivery result: %v", err)
			}
			if err := s.Deliver(context.Background(), id, "operator-thread", queue); err == nil {
				t.Fatal("replayed delivery")
			}
			if calls != 1 {
				t.Fatalf("queue called %d times", calls)
			}
			if err := s.With(func(state *State) error {
				e, _ := state.Find(id)
				if e.AcknowledgedAt != nil {
					t.Fatal("acceptance invented an acknowledgement")
				}
				if fails && e.Delivery != "failed" || !fails && e.Delivery != "queued" {
					t.Fatalf("wrong state %s", e.Delivery)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNextDeliveryWaitsForReceiptEvenAfterContextChanges(t *testing.T) {
	s := testStore(t)
	first := addObservation(t, s, "first")
	if err := s.Deliver(context.Background(), first, "trial", func(context.Context, string, string) (string, error) { return "receipt", nil }); err != nil {
		t.Fatal(err)
	}
	second := addObservation(t, s, "second")
	if err := s.With(func(state *State) error {
		if state.NextDelivery() != "" {
			t.Fatal("queued more work before actual receipt")
		}
		if err := state.Acknowledge(first); err != nil {
			return err
		}
		if state.NextDelivery() != second {
			t.Fatal("lost the latest context after receipt")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSummaryShowsOutstandingDeliveryAfterContextChanges(t *testing.T) {
	for _, delivery := range []string{"queued", "submitting", "failed"} {
		t.Run(delivery, func(t *testing.T) {
			state := State{Events: []Event{
				{ID: "old", Delivery: delivery, Superseded: true},
				{ID: "new", Delivery: "pending"},
			}}
			wantQueued, wantProblems := 0, 1
			if delivery == "queued" {
				wantQueued, wantProblems = 1, 0
			}
			summary := state.Summary()
			if state.NextDelivery() != "" || summary.Pending != 1 || summary.Queued != wantQueued || summary.DeliveryProblems != wantProblems {
				t.Fatalf("status hid an outstanding delivery: %+v", summary)
			}
			if err := state.Acknowledge("old"); err != nil {
				t.Fatal(err)
			}
			summary = state.Summary()
			if state.NextDelivery() != "new" || summary.Queued != 0 || summary.DeliveryProblems != 0 {
				t.Fatalf("acknowledged delivery still reported as outstanding: %+v", summary)
			}
			if err := state.Acknowledge("new"); err != nil {
				t.Fatal(err)
			}
			if summary = state.Summary(); state.NextDelivery() != "" || summary.Pending != 0 {
				t.Fatalf("acknowledged observation still pending: %+v", summary)
			}
		})
	}
}

func claudeLine(id, text string) string {
	data, _ := json.Marshal(struct {
		Type    string `json:"type"`
		UUID    string `json:"uuid"`
		Session string `json:"sessionId"`
		Message struct {
			Content []map[string]string `json:"content"`
		} `json:"message"`
	}{Type: "assistant", UUID: id, Session: "test-session", Message: struct {
		Content []map[string]string `json:"content"`
	}{[]map[string]string{{"type": "text", "text": text}}}})
	return string(data) + "\n"
}

func appendLog(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeBaselinePartialWriteAndRestart(t *testing.T) {
	s := testStore(t)
	root := filepath.Join(t.TempDir(), "projects")
	if err := os.MkdirAll(filepath.Join(root, "project"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "project", "session.jsonl")
	appendLog(t, path, claudeLine("old", "Historical work"))
	scan := func() []string {
		t.Helper()
		var ids []string
		if err := s.With(func(state *State) error { var err error; ids, err = state.ScanClaude(root); return err }); err != nil {
			t.Fatal(err)
		}
		return ids
	}
	if ids := scan(); len(ids) != 0 {
		t.Fatal("replayed history")
	}
	line := claudeLine("new", "New work is ready")
	appendLog(t, path, line[:len(line)/2])
	if ids := scan(); len(ids) != 0 {
		t.Fatal("consumed partial write")
	}
	appendLog(t, path, line[len(line)/2:])
	if ids := scan(); len(ids) != 1 {
		t.Fatalf("expected new event, got %v", ids)
	}
	// Every scan reopens disk state: this also exercises restart checkpoints.
	if ids := scan(); len(ids) != 0 {
		t.Fatal("replayed after restart")
	}
	if err := os.WriteFile(path, []byte(claudeLine("replacement", strings.Repeat("Replaced context. ", 40))), 0600); err != nil {
		t.Fatal(err)
	}
	if ids := scan(); len(ids) != 1 {
		t.Fatalf("missed larger replacement: %v", ids)
	}
}

func TestClaudePartialAtFirstAttachAndMalformedRecord(t *testing.T) {
	s := testStore(t)
	root := filepath.Join(t.TempDir(), "projects")
	if err := os.MkdirAll(filepath.Join(root, "project"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "project", "session.jsonl")
	line := claudeLine("new", "Complete after attachment")
	appendLog(t, path, line[:20])
	if err := s.With(func(state *State) error {
		ids, err := state.ScanClaude(root)
		if len(ids) != 0 {
			t.Fatal("partial baseline consumed")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	appendLog(t, path, line[20:])
	if err := s.With(func(state *State) error {
		ids, err := state.ScanClaude(root)
		if len(ids) != 1 {
			t.Fatal("lost first completed record")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	appendLog(t, path, "not json\n")
	if err := s.With(func(state *State) error { _, err := state.ScanClaude(root); return err }); err == nil {
		t.Fatal("silently swallowed malformed record")
	}
}

func TestClaudeDecisionObservationRetainsQuestionAndOptions(t *testing.T) {
	line := `{"type":"assistant","uuid":"decision","sessionId":"claude-session","message":{"content":[{"type":"tool_use","name":"AskUserQuestion","input":{"questions":[{"question":"Merge PR 558?","options":[{"label":"Merge now"},{"label":"Wait"}]}]}}]}}`
	o, err := claudeObservation([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(o.Text, "Merge PR 558?") || !strings.Contains(o.Text, "Wait") || !strings.Contains(o.Text, "untrusted source data") {
		t.Fatalf("lost the decision context: %s", o.Text)
	}
}

func TestRemovedSessionDoesNotStopObservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "removed-session.jsonl")
	state := State{Cursors: map[string]Cursor{}}
	ids, err := state.scanFile(path, false, claudeObservation)
	if err != nil || len(ids) != 0 {
		t.Fatalf("removed session interrupted scan: %v, %v", ids, err)
	}
	meta, err := readCodexMetadata(path)
	if err != nil || meta.ID != "" {
		t.Fatalf("removed rollout interrupted metadata read: %+v, %v", meta, err)
	}
	if _, err := state.ScanClaude(path); err == nil {
		t.Fatal("missing configured source root was silently ignored")
	}
}

func TestPrivateStoreSerializesConcurrentWriters(t *testing.T) {
	s := testStore(t)
	var wg sync.WaitGroup
	for _, rev := range []string{"one", "two"} {
		wg.Go(func() {
			if err := s.With(func(state *State) error {
				_, _, err := state.Observe(Observation{Source: Claude, Key: rev, Revision: rev, Text: rev})
				return err
			}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := s.With(func(state *State) error {
		if len(state.Events) != 2 {
			t.Fatal("lost concurrent update")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(s.Dir, "state.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("state privacy: %v %v", info, err)
	}
}

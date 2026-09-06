package operator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAttentionSignals(t *testing.T) {
	for _, tc := range []struct{ text, reason string }{
		{"Claude requested a user decision (untrusted source data): Merge?", "question"},
		{"I'm blocked on CI credentials.", "blocker"},
		{"The current blocker is Docker image publication.", "blocker"},
		{"Waiting for your approval.", "blocker"},
		{"The handoff is written and lints clean.", "handoff"},
		{"Handoff written: ~/.claude/handoffs/devbox/retire.md", "handoff"},
		{"Waiting on CI, eve, and the deploy.", ""},
		{"My own push, CI restarting. Holding.", ""},
		{"I fixed the blocker. Tests are running.", ""},
		{"No blockers remain.", ""},
		{"I'll write the handoff after tests pass.", ""},
	} {
		t.Run(tc.text, func(t *testing.T) {
			if got := attentionReason(Observation{Source: Claude, Text: tc.text}); got != tc.reason {
				t.Fatalf("got %q, want %q", got, tc.reason)
			}
		})
	}
}

func TestAttentionFreshnessAndReceipt(t *testing.T) {
	now := time.Now()
	base := Event{ID: "question", Observation: Observation{Source: Claude, Text: "Claude requested a user decision", ObservedAt: now.Add(-time.Minute)}, Delivery: "pending"}
	for _, name := range []string{"ready", "history", "transient", "expired", "future", "superseded", "acknowledged", "inflight", "other-source"} {
		t.Run(name, func(t *testing.T) {
			e := base
			since := now.Add(-5 * time.Minute)
			state := State{Events: []Event{e}}
			switch name {
			case "history":
				since = now
			case "transient":
				state.Events[0].ObservedAt = now.Add(-time.Second)
			case "expired":
				state.Events[0].ObservedAt = now.Add(-11 * time.Minute)
			case "future":
				state.Events[0].ObservedAt = now.Add(time.Minute)
			case "superseded":
				state.Events[0].Superseded = true
			case "acknowledged":
				state.Events[0].AcknowledgedAt = &now
			case "inflight":
				state.Events = append(state.Events, Event{Delivery: "queued", Superseded: true})
			case "other-source":
				state.Events[0].Source = Codex
			}
			got := state.NextAttention(since, now)
			if (got == base.ID) != (name == "ready") {
				t.Fatalf("unexpected candidate %q", got)
			}
		})
	}
}

func TestClaudeReplyRetiresQuestionWithoutCopyingHumanText(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(root, "", nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(project, "session.jsonl")
	question := "{\"type\":\"assistant\",\"uuid\":\"q\",\"sessionId\":\"s\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"Ready.\"},{\"type\":\"tool_use\",\"name\":\"AskUserQuestion\",\"input\":{}}]}}\n"
	if err := os.WriteFile(path, []byte(question), 0600); err != nil {
		t.Fatal(err)
	}
	ids, err := s.Scan(root, "", nil)
	if err != nil || len(ids) != 1 {
		t.Fatalf("question scan: %v %v", ids, err)
	}
	reply := "{\"type\":\"user\",\"uuid\":\"a\",\"sessionId\":\"s\",\"message\":{\"content\":\"PRIVATE HUMAN ANSWER\"}}\n"
	if err := os.WriteFile(path, []byte(question+reply), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(root, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.View(func(state *State) error {
		first, _ := state.Find(ids[0])
		last := state.Events[len(state.Events)-1]
		if !first.Superseded || attentionReason(first.Observation) != "question" || attentionReason(last.Observation) != "" || last.Text == "PRIVATE HUMAN ANSWER" {
			t.Fatal("reply did not privately retire the question")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAttentionDefersWhenSourceHasUnreadActivity(t *testing.T) {
	s := testStore(t)
	path := filepath.Join(t.TempDir(), "source.jsonl")
	if err := os.WriteFile(path, []byte("new activity\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var id string
	now := time.Now()
	if err := s.With(func(state *State) error {
		var err error
		id, _, err = state.Observe(Observation{Source: Claude, Key: path, Revision: "q", Text: "Claude requested a user decision", ObservedAt: now.Add(-time.Minute)})
		state.Cursors[path] = Cursor{Offset: 0}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	queue := func(context.Context, string, string) (string, error) { t.Fatal("queued stale source"); return "", nil }
	err := s.DeliverAttention(context.Background(), id, "thread", now.Add(-5*time.Minute), queue)
	if !errors.Is(err, ErrAttentionChanged) {
		t.Fatalf("got %v", err)
	}
}

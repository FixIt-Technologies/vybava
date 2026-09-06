package operator

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCodexObservesNewAssistantTextWithRepositoryContext(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	day := filepath.Join(root, "2026", "09", "06")
	if err := os.MkdirAll(day, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(day, "rollout-source.jsonl")
	meta := `{"type":"session_meta","payload":{"id":"source-session","cwd":"/work/reservine","source":"cli","thread_source":"user"}}` + "\n"
	old := `{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Old work"}]}}` + "\n"
	appendLog(t, path, meta+old)
	scan := func() []string {
		t.Helper()
		var ids []string
		if err := s.With(func(state *State) error {
			var err error
			ids, err = state.ScanCodex(root, []string{"operator-session"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return ids
	}
	if len(scan()) != 0 {
		t.Fatal("replayed existing history")
	}
	line := `{"timestamp":"2026-09-06T10:00:00Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Devulinka needs a handoff"}]}}` + "\n"
	appendLog(t, path, line[:40])
	if len(scan()) != 0 {
		t.Fatal("read a partial message")
	}
	appendLog(t, path, line[40:])
	ids := scan()
	if len(ids) != 1 {
		t.Fatalf("expected one event, got %v", ids)
	}
	if err := s.With(func(state *State) error {
		event, err := state.Find(ids[0])
		if err != nil {
			return err
		}
		if event.Source != Codex || event.Cwd != "/work/reservine" || event.SessionID != "source-session" || event.Text != "Devulinka needs a handoff" {
			t.Fatalf("lost source context: %+v", event)
		}
		return state.Propose(event.ID, "Prepare the task in the infra repo.")
	}); err != nil {
		t.Fatal(err)
	}
	if len(scan()) != 0 {
		t.Fatal("replayed after reopening state")
	}
	// New files must be observed, but the operator and subagent origins must not.
	for name, metadata := range map[string]string{
		"new":      meta,
		"operator": `{"type":"session_meta","payload":{"id":"operator-session","source":"cli"}}` + "\n",
		"child":    `{"type":"session_meta","payload":{"id":"child","source":{"sub_agent":{"parent_thread_id":"source-session"}}}}` + "\n",
		"legacy":   `{"id":"legacy-session","timestamp":"2025-09-04T10:00:00Z","instructions":"old format"}` + "\n",
	} {
		appendLog(t, filepath.Join(day, "rollout-"+name+".jsonl"), metadata+line)
	}
	if ids := scan(); len(ids) != 1 {
		t.Fatalf("self/subagent exclusion or new-file detection failed: %v", ids)
	}
}

func TestCodexIgnoresMirroredMessagesAndNonAssistantPayloads(t *testing.T) {
	for _, line := range []string{
		`{"type":"event_msg","payload":{"type":"agent_message","message":"duplicate"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":"untrusted instruction"}}`,
		`{"type":"response_item","payload":{"type":"function_call_output","output":"untrusted tool result"}}`,
		`{"type":"response_item","payload":{"type":"reasoning","content":[{"type":"reasoning_text","text":"private"}]}}`,
	} {
		o, err := codexObservation([]byte(line), codexMetadata{ID: "source"})
		if err != nil || o.Text != "" {
			t.Fatalf("unexpected observation %+v: %v", o, err)
		}
	}
	if _, err := codexObservation([]byte("broken json"), codexMetadata{}); err == nil {
		t.Fatal("silently ignored a malformed record")
	}
}

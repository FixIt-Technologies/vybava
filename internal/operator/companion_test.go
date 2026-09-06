package operator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLargeCompactionDoesNotBlockFollowingAssistantMessage(t *testing.T) {
	s := testStore(t)
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	// Real Codex compaction records exceeded the old 4 MiB sweep limit.
	compact := `{"type":"compacted","payload":{"text":"` + strings.Repeat("x", 5*1024*1024) + `"}}` + "\n"
	message := `{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Ready for your review"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(compact+message), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.With(func(state *State) error {
		parse := func(line []byte) (Observation, error) { return codexObservation(line, codexMetadata{ID: "fixture"}) }
		if _, err := state.scanFile(path, false, parse); err != nil {
			return err
		}
		ids, err := state.scanFile(path, false, parse)
		if err != nil {
			return err
		}
		if len(ids) != 1 || len(state.Events) != 1 || state.Events[0].Text != "Ready for your review" {
			t.Fatal("compaction blocked or leaked into observations")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCompanionPreservesUnscoredHumanFeedbackAfterContextChange(t *testing.T) {
	s := testStore(t)
	id := addObservation(t, s, "first")
	if err := s.With(func(state *State) error {
		if err := state.Propose(id, "Draft"); err != nil {
			return err
		}
		return state.AddFeedback(id, 1, "Looks nice; don't send it")
	}); err != nil {
		t.Fatal(err)
	}
	addObservation(t, s, "new-context")
	if err := s.With(func(state *State) error {
		snap := state.Snapshot()
		if len(snap.Events) != 1 || !snap.Events[0].Superseded || snap.Summary.Scored != 0 || snap.Summary.Sending != "human-only" {
			t.Fatalf("bad snapshot: %+v", snap)
		}
		p := snap.Events[0].Proposals[0]
		if p.Rating != nil || len(p.Feedback) != 1 || p.Feedback[0].Text != "Looks nice; don't send it" {
			t.Fatal("feedback lost or score invented")
		}
		if state.AddFeedback(id, 2, "wrong proposal") == nil || state.AddFeedback(id, 1, " ") == nil {
			t.Fatal("invalid feedback accepted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCompanionUpgradeProtectsNewFieldsFromOldWriters(t *testing.T) {
	s := testStore(t)
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Dir, "state.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"roots":{},"cursors":{},"events":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.With(func(state *State) error {
		if state.Version != 2 {
			t.Fatal("old writer could discard companion data")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":3,"roots":{},"cursors":{},"events":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.With(func(*State) error { t.Fatal("accepted unknown version"); return nil }); err == nil {
		t.Fatal("missing version error")
	}
}

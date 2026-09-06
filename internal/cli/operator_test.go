package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FixIt-Technologies/vybava/internal/operator"
)

func TestOperatorObserveProposeScoreJourney(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	run := func(input string, args ...string) (string, error) {
		var out bytes.Buffer
		cmd, err := (App{Stdout: &out, Stderr: &out, Stdin: strings.NewReader(input)}).Command("vybava")
		if err != nil {
			return "", err
		}
		cmd.SetArgs(append([]string{"operator", "--state-dir", dir, "--json"}, args...))
		err = cmd.Execute()
		return out.String(), err
	}
	if _, err := run("", "watch", "--once", "--codex-root", t.TempDir()); err == nil || !strings.Contains(err.Error(), "--exclude-codex-session") {
		t.Fatalf("Codex observation did not require self-exclusion: %v", err)
	}
	if _, err := run("", "watch", "--once", "--thread", "fixture", "--attention-since", "0001-01-01T00:00:00Z"); err == nil {
		t.Fatal("zero attention boundary fell back to unfiltered delivery")
	}
	out, err := run(`{"source":"whatsapp","key":"fixture-only","revision":"1","text":"Are we meeting at ten?"}`, "observe")
	if err != nil {
		t.Fatal(err)
	}
	var observed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &observed); err != nil {
		t.Fatal(err)
	}
	if _, err := run("Ten works for me.", "propose", observed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := run("Good wording; leave it unsent.", "feedback", observed.ID, "--proposal", "1"); err != nil {
		t.Fatal(err)
	}
	out, err = run("", "snapshot")
	if err != nil || !strings.Contains(out, "Good wording; leave it unsent.") || !strings.Contains(out, `"scored": 0`) {
		t.Fatalf("qualitative feedback did not survive snapshot: %s: %v", out, err)
	}
	if _, err := run("", "watch", "--once", "--claude-root", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	out, err = run("", "snapshot")
	if err != nil || !strings.Contains(out, `"last_scan_at":`) {
		t.Fatalf("source scan missing: %s: %v", out, err)
	}
	if _, err := run("", "score", observed.ID, "--proposal", "1", "--score", "5"); err != nil {
		t.Fatal(err)
	}
	out, err = run("", "status")
	if err != nil || !strings.Contains(out, `"scored": 1`) || !strings.Contains(out, `"sending": "human-only"`) {
		t.Fatalf("status %s: %v", out, err)
	}
	if _, err := run("", "send", observed.ID); err == nil {
		t.Fatal("send command exists")
	}
}

func TestOperatorCooldownSurvivesAcknowledgementInSameWatcher(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("fake queue executable uses a POSIX shell")
	}
	root, bin := t.TempDir(), t.TempDir()
	store := operator.Store{Dir: filepath.Join(t.TempDir(), "state")}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nprintf 'Queued message fixture receipt\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Scan(root, "", nil); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC()
	for _, id := range []string{"a", "b"} {
		line := fmt.Sprintf("{\"type\":\"assistant\",\"uuid\":%q,\"sessionId\":%q,\"timestamp\":%q,\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"Handoff written: fixture\"}]}}\n", id, id, start.Add(-time.Minute).Format(time.RFC3339Nano))
		if err := os.WriteFile(filepath.Join(project, id+".jsonl"), []byte(line), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var clock atomic.Int64
	clock.Store(start.UnixNano())
	rt := &runtime{stdout: io.Discard, stderr: io.Discard, stdin: strings.NewReader(""), json: true}
	cmd := rt.operatorCommandWithClock("operator", func() time.Time { return time.Unix(0, clock.Load()) })
	cmd.SetArgs([]string{"--state-dir", store.Dir, "watch", "--claude-root", root, "--thread", "fixture", "--attention-since", start.Add(-5 * time.Minute).Format(time.RFC3339), "--interval", "1s"})
	ctx, cancel := context.WithCancel(context.Background())
	cmd.SetContext(ctx)
	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("watcher did not stop")
		}
	})
	wait := func(ready func(*operator.State) bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			ok := false
			if err := store.View(func(state *operator.State) error { ok = ready(state); return nil }); err != nil {
				t.Fatal(err)
			}
			if ok {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("watcher did not reach expected state")
	}
	queued := func(state *operator.State) int {
		n := 0
		for _, e := range state.Events {
			if e.Delivery == "queued" {
				n++
			}
		}
		return n
	}
	wait(func(state *operator.State) bool { return queued(state) == 1 })
	var queuedScan time.Time
	if err := store.View(func(state *operator.State) error { queuedScan = *state.LastScanAt; return nil }); err != nil {
		t.Fatal(err)
	}
	wait(func(state *operator.State) bool { return state.LastScanAt != nil && state.LastScanAt.After(queuedScan) })
	if err := store.With(func(state *operator.State) error {
		for _, e := range state.Events {
			if e.Delivery == "queued" {
				return state.Acknowledge(e.ID)
			}
		}
		return fmt.Errorf("missing first submission")
	}); err != nil {
		t.Fatal(err)
	}
	clock.Store(start.Add(119 * time.Second).UnixNano())
	advancedAt := time.Now()
	wait(func(state *operator.State) bool { return state.LastScanAt != nil && state.LastScanAt.After(advancedAt) })
	var firstScan time.Time
	if err := store.View(func(state *operator.State) error { firstScan = *state.LastScanAt; return nil }); err != nil {
		t.Fatal(err)
	}
	// The next publication proves the preceding cycle finished its dispatch step.
	wait(func(state *operator.State) bool { return state.LastScanAt != nil && state.LastScanAt.After(firstScan) })
	if err := store.View(func(state *operator.State) error {
		if queued(state) != 1 {
			t.Fatal("acknowledgement bypassed cooldown")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	clock.Store(start.Add(121 * time.Second).UnixNano())
	wait(func(state *operator.State) bool { return queued(state) == 2 })
}

func TestOperatorSelectiveWatchJourney(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("fake queue executable uses a POSIX shell")
	}
	dir, root, bin := filepath.Join(t.TempDir(), "state"), t.TempDir(), t.TempDir()
	logPath := filepath.Join(t.TempDir(), "queue.log")
	t.Setenv("OPERATOR_QUEUE_TEST_LOG", logPath)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$OPERATOR_QUEUE_TEST_LOG\"\nprintf 'Queued message fixture receipt\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	since := now.Add(-5 * time.Minute).Format(time.RFC3339)
	run := func() error {
		var out bytes.Buffer
		cmd, err := (App{Stdout: &out, Stderr: &out}).Command("vybava")
		if err != nil {
			return err
		}
		cmd.SetArgs([]string{"operator", "--state-dir", dir, "--json", "watch", "--once", "--claude-root", root, "--thread", "fixture-thread", "--attention-since", since})
		return cmd.Execute()
	}
	if err := run(); err != nil {
		t.Fatal(err)
	} // baseline empty source
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	record := func(id, text string, at time.Time) string {
		return fmt.Sprintf("{\"type\":\"assistant\",\"uuid\":%q,\"sessionId\":%q,\"timestamp\":%q,\"message\":{\"content\":[{\"type\":\"text\",\"text\":%q}]}}\n", id, id, at.Format(time.RFC3339Nano), text)
	}
	for name, content := range map[string]string{
		"a-partial": record("partial", "Handoff written: incomplete source", now.Add(-time.Minute)) + "{",
		"old":       record("old", "Handoff written: OLD PRIVATE HISTORY", now.Add(-6*time.Minute)),
		"progress":  record("progress", "Waiting on CI, eve, and the deploy.", now.Add(-time.Minute)),
		"question":  record("question", "Claude requested a user decision: PRIVATE SOURCE TEXT", now.Add(-time.Minute)),
	} {
		if err := os.WriteFile(filepath.Join(project, name+".jsonl"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	// A separate eligible source cannot cause a second submission before receipt.
	if err := os.WriteFile(filepath.Join(project, "handoff.jsonl"), []byte(record("handoff", "Handoff written: task", now.Add(-time.Minute))), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if strings.Count(got, "\n") != 1 || !strings.Contains(got, "fixture-thread") || !strings.Contains(got, "operator show") || strings.Contains(got, "PRIVATE") {
		t.Fatalf("unexpected queue envelope: %s", got)
	}
	store := operator.Store{Dir: dir}
	if err := store.View(func(state *operator.State) error {
		found := false
		for _, event := range state.Events {
			if event.Revision == "question" {
				found = true
				if event.Delivery != "queued" || !strings.Contains(got, `"event":"`+event.ID+`"`) {
					t.Fatal("queue reference does not identify the intended question")
				}
			} else if event.Delivery != "pending" {
				t.Fatalf("suppressed source %s was submitted", event.Revision)
			}
		}
		if !found {
			t.Fatal("intended question missing from state")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

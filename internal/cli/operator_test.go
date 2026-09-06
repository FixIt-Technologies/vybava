package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
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

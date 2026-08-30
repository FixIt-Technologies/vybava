package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// run drives the press applet exactly as the installed link does, so these
// tests exercise the real argument path rather than a hand-built command.
func runPress(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	command, cmdErr := (App{Stdout: &out, Stderr: &errOut}).Command("press")
	if cmdErr != nil {
		t.Fatalf("building the press applet: %v", cmdErr)
	}
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetArgs(args)
	err = command.Execute()
	return out.String(), errOut.String(), err
}

// Human-readable by default, stable JSON only on --json, per CONTRIBUTING.
func TestPressOutputIsHumanReadableByDefault(t *testing.T) {
	exports := t.TempDir()
	t.Setenv("PRESS_EXPORTS", exports)

	stdout, _, err := runPress(t, "init", "--project", "acme")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if strings.HasPrefix(strings.TrimSpace(stdout), "{") {
		t.Fatalf("init emitted JSON without --json: %q", stdout)
	}
	if !strings.Contains(stdout, "acme") || !strings.Contains(stdout, "created") {
		t.Fatalf("init text output is not informative: %q", stdout)
	}

	stdout, _, err = runPress(t, "init", "--project", "acme", "--json")
	if err != nil {
		t.Fatalf("init --json: %v", err)
	}
	var payload struct {
		Project string `json:"project"`
		Dir     string `json:"dir"`
		Created bool   `json:"created"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("init --json is not valid JSON (%v): %q", err, stdout)
	}
	if payload.Project != "acme" || payload.Created {
		t.Fatalf("init --json payload = %+v, want acme and created=false on the second run", payload)
	}
	if payload.Dir != filepath.Join(exports, "acme") {
		t.Fatalf("init --json dir = %q, want it under PRESS_EXPORTS", payload.Dir)
	}
}

func TestPressIndexAndConfigTextOutput(t *testing.T) {
	t.Setenv("PRESS_EXPORTS", t.TempDir())
	if _, _, err := runPress(t, "init", "--project", "acme"); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runPress(t, "index", "list", "--project", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(strings.TrimSpace(stdout), "{") {
		t.Fatalf("index list emitted JSON without --json: %q", stdout)
	}
	if !strings.Contains(stdout, "no artifacts") {
		t.Fatalf("an empty index should say so plainly, got %q", stdout)
	}

	if _, _, err := runPress(t, "index", "add", "--project", "acme",
		"--kind", "pdf", "--file", "offer/demo.pdf", "--title", "Demo"); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = runPress(t, "index", "list", "--project", "acme")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pdf", "demo", "Demo"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("index list %q missing %q", stdout, want)
		}
	}

	// A scalar prints bare so it is directly usable in a shell.
	stdout, _, err = runPress(t, "config", "get", "project.name", "--project", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout) != "acme" {
		t.Fatalf("config get scalar = %q, want a bare value", stdout)
	}
	// A structure stays JSON even without --json: there is no better text form.
	stdout, _, err = runPress(t, "config", "get", "pdf", "--project", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout), "{") {
		t.Fatalf("config get structure = %q, want JSON", stdout)
	}
}

func TestPressRejectsProjectNamesThatEscape(t *testing.T) {
	t.Setenv("PRESS_EXPORTS", t.TempDir())
	if _, _, err := runPress(t, "init", "--project", "../../escape"); err == nil {
		t.Fatal("init accepted a traversing --project")
	}
	if _, _, err := runPress(t, "index", "add", "--project", "acme", "--file", "../../x.pdf", "--title", "x"); err == nil {
		t.Fatal("index add accepted a traversing --file")
	}
}

func TestPressLintExitsNonZeroOnFindings(t *testing.T) {
	t.Setenv("PRESS_EXPORTS", t.TempDir())
	if _, _, err := runPress(t, "init", "--project", "acme"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runPress(t, "index", "add", "--project", "acme",
		"--kind", "pdf", "--file", "offer/never-built.pdf", "--title", "Ghost"); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runPress(t, "lint", "--project", "acme")
	if err == nil {
		t.Fatal("lint should fail while a recorded artifact is missing from disk")
	}
	if !strings.Contains(stdout, "problem:") {
		t.Fatalf("lint output should name the problem, got %q", stdout)
	}
}

func TestPressDoctrineServesEmbeddedLaw(t *testing.T) {
	stdout, _, err := runPress(t, "doctrine")
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(stdout)) == 0 {
		t.Fatal("press doctrine printed nothing")
	}
	stdout, _, err = runPress(t, "doctrine", "--schema")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(stdout), &schema); err != nil {
		t.Fatalf("press doctrine --schema is not valid JSON: %v", err)
	}
}

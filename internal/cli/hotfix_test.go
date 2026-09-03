package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/FixIt-Technologies/vybava/internal/runx"
)

func runHotfix(t *testing.T, dir string, args ...string) (runx.Envelope, error) {
	t.Helper()
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()
	var out, errOut bytes.Buffer
	command, err := (App{Stdout: &out, Stderr: &errOut}).Command("hotfix")
	if err != nil {
		t.Fatal(err)
	}
	command.SetArgs(append(args, "--json"))
	err = command.Execute()
	var env runx.Envelope
	if jerr := json.Unmarshal(out.Bytes(), &env); jerr != nil {
		t.Fatalf("%v: stdout is not one envelope: %q (%v)", args, out.String(), jerr)
	}
	return env, err
}

// TestHotfixEnvelopeSurface walks every verb and asserts the envelope shape
// on a forced failure (outside a repo, then without a manifest) — the
// cli-craft contract: JSON parseable on every path, a closed code, a fix,
// and the exit code carried back to the multicall main.
func TestHotfixEnvelopeSurface(t *testing.T) {
	verbs := [][]string{{"init"}, {"start", "x"}, {"status", "x"}, {"pr", "x"}, {"deploy", "x"}, {"forward", "x"}, {"finish", "x"}}
	empty := t.TempDir()
	for _, verb := range verbs {
		env, err := runHotfix(t, empty, verb...)
		if env.V != runx.EnvelopeVersion || env.OK || env.Verb != verb[0] {
			t.Fatalf("%v outside a repo: %+v", verb, env)
		}
		if len(env.Diagnostics) != 1 || env.Diagnostics[0].Code != "CONTEXT_NOT_A_REPO" {
			t.Fatalf("%v: diagnostics = %+v", verb, env.Diagnostics)
		}
		if ExitCode(err) != 2 || ErrorText(err) != "" {
			t.Fatalf("%v: exit=%d text=%q", verb, ExitCode(err), ErrorText(err))
		}
	}

	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	for _, verb := range verbs[1:] {
		env, err := runHotfix(t, repo, verb...)
		if env.OK || env.Diagnostics[0].Code != "CONFIG_MISSING" || len(env.Next) != 1 || env.Next[0] != "hotfix init" {
			t.Fatalf("%v without manifest: %+v", verb, env)
		}
		var ec runx.ExitCoder
		if !errors.As(err, &ec) || ec.ExitCode() != 2 {
			t.Fatalf("%v: exit code not carried: %v", verb, err)
		}
	}

	env, err := runHotfix(t, repo, "init")
	if err != nil || !env.OK || env.Verb != "init" || len(env.Next) != 2 {
		t.Fatalf("init: %+v %v", env, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "hotfix.yaml")); err != nil {
		t.Fatal("init did not write hotfix.yaml")
	}
	// Misuse answers with the corrected invocation, never a usage dump.
	env, _ = runHotfix(t, repo, "status")
	if env.OK || env.Diagnostics[0].Code != "SLUG_REQUIRED" || env.Next[0] != "hotfix status <slug>" {
		t.Fatalf("status without slug on main: %+v", env)
	}
}

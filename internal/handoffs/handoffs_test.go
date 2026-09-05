package handoffs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

const handoff = `---
name: %s
description: Finish the thing.
status: open
created: 2026-08-01
created-by: unknown
sessions:
  - unknown
---

# Handoff

%s
`

// fixture builds a home with a registry-backed `fixit` checkout and a
// walk-discovered `forge` one; git and gh are answered from the maps.
func fixture(t *testing.T, liveRefs map[string]bool, prs map[string]string) Env {
	t.Helper()
	root := t.TempDir()
	projects := filepath.Join(root, "Work", "Projects")
	for _, dir := range []string{"FixIt-Technologies/FixIt", "ADF/forge"} {
		if err := os.MkdirAll(filepath.Join(projects, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return Env{
		Home: filepath.Join(root, ".claude", "handoffs"), UserHome: root, Projects: projects, Now: now,
		Registry: func() ([]byte, error) {
			return []byte("| Path | Client |\n|---|---|\n| `~/Work/Projects/FixIt-Technologies/FixIt` | FixIt |\n"), nil
		},
		Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
			switch name {
			case "git":
				switch args[2] {
				case "rev-parse":
					if liveRefs[args[len(args)-1]] {
						return []byte("abc\n"), nil
					}
					return nil, errors.New("exit status 1")
				case "worktree":
					return []byte(""), nil
				case "remote":
					return []byte("git@github.com:LEFTEQ/FixIt.git\n"), nil
				}
			case "gh":
				if state, ok := prs[args[4]+"#"+args[2]]; ok {
					return []byte(state + "\n"), nil
				}
				return nil, errors.New("not found")
			}
			return nil, errors.New("unexpected " + name + " " + strings.Join(args, " "))
		},
	}
}

func write(t *testing.T, path, content string, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, now.Add(-age), now.Add(-age)); err != nil {
		t.Fatal(err)
	}
}

func TestVerdicts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, project, body string
		age                 time.Duration
		verdict, reason     string
	}{
		{"live branch", "fixit", "**Branch:** work/alive @ abc1234", 0, VerdictLive, "branch fixit@work/alive live"},
		{"branch gone and PR merged", "fixit", "**Branch:** work/gone @ abc1234 (PR #10)", 0, VerdictDead, "branch fixit@work/gone gone; PR LEFTEQ/FixIt#10 merged"},
		{"open PR by owner/repo", "fixit", "Shipped in LEFTEQ/FixIt#11.", 0, VerdictLive, "PR LEFTEQ/FixIt#11 open"},
		{"main only and fresh", "fixit", "**Branch:** main @ abc1234", 3 * 24 * time.Hour, VerdictUnknown, "no branch/PR evidence"},
		{"no branch line and stale", "fixit", "Just prose.", 20 * 24 * time.Hour, VerdictDead, "no branch/PR evidence, untouched 20 days"},
		{"marked merged", "fixit", "**Branch:** main @ abc1234 — MERGED to main", 0, VerdictDead, "Branch line marked MERGED"},
		{"repo not found", "mystery", "**Branch:** work/alive @ abc1234", 0, VerdictUnknown, "repo not found: mystery"},
		{"PR state unknown", "fixit", "**Branch:** work/gone @ abc1234 (PR #99)", 0, VerdictUnknown, "PR LEFTEQ/FixIt#99 state unknown"},
		{"walked repo", "forge", "**Branch:** feat/product @ abc1234", 0, VerdictLive, "branch forge@feat/product live"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			env := fixture(t, map[string]bool{
				"refs/heads/work/alive": true, "refs/remotes/origin/feat/product": true,
			}, map[string]string{"LEFTEQ/FixIt#10": "MERGED", "LEFTEQ/FixIt#11": "OPEN"})
			write(t, filepath.Join(env.Home, c.project, "task.md"), sprintf(handoff, "task", c.body), c.age)
			report, err := Reconcile(context.Background(), env, Options{})
			if err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if len(report.Items) != 1 {
				t.Fatalf("items = %#v, want 1", report.Items)
			}
			got := report.Items[0]
			if got.Verdict != c.verdict || got.Reason != c.reason {
				t.Errorf("verdict = %q %q, want %q %q", got.Verdict, got.Reason, c.verdict, c.reason)
			}
		})
	}
}

func TestApplyArchivesDeadOnly(t *testing.T) {
	t.Parallel()
	env := fixture(t, nil, nil)
	stale := 30 * 24 * time.Hour
	write(t, filepath.Join(env.Home, "fixit", "old.md"), sprintf(handoff, "old", "Prose."), stale)
	write(t, filepath.Join(env.Home, "fixit", "archive", "old.md"), sprintf(handoff, "old", "Already here."), stale)
	write(t, filepath.Join(env.Home, "fixit", "big", "handoff.md"), sprintf(handoff, "big", "Prose."), stale)
	write(t, filepath.Join(env.Home, "fixit", "big", "web.md"), "# Context\n", stale)
	write(t, filepath.Join(env.Home, "fixit", "fresh.md"), sprintf(handoff, "fresh", "Prose."), 0)
	write(t, filepath.Join(env.Home, "fixit", "done.md"), strings.Replace(sprintf(handoff, "done", "Prose."), "status: open", "status: done", 1), stale)

	report, err := Reconcile(context.Background(), env, Options{Apply: true})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if got := report.Summary; got != (Summary{Dead: 2, Unknown: 1, Archived: 2}) {
		t.Fatalf("summary = %+v", got)
	}
	moved := filepath.Join(env.Home, "fixit", "archive", "old-20260905.md")
	data, err := os.ReadFile(moved)
	if err != nil {
		t.Fatalf("collision suffix: %v", err)
	}
	if want := strings.Replace(sprintf(handoff, "old", "Prose."), "status: open", "status: abandoned", 1); string(data) != want {
		t.Errorf("rewritten handoff = %q, want %q", data, want)
	}
	for _, path := range []string{
		filepath.Join(env.Home, "fixit", "archive", "big", "handoff.md"),
		filepath.Join(env.Home, "fixit", "archive", "big", "web.md"),
		filepath.Join(env.Home, "fixit", "fresh.md"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s: %v", path, err)
		}
	}
	for _, path := range []string{filepath.Join(env.Home, "fixit", "old.md"), filepath.Join(env.Home, "fixit", "big")} {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("%s should have moved", path)
		}
	}
	if report.Items[0].Archived != filepath.Join(env.Home, "fixit", "archive", "big", "handoff.md") {
		t.Errorf("Archived = %q", report.Items[0].Archived)
	}
}

func sprintf(format string, args ...any) string {
	return strings.TrimSpace(fmt.Sprintf(format, args...)) + "\n"
}

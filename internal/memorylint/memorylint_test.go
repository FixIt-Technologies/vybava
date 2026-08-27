package memorylint_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FixIt-Technologies/vybava/internal/memorylint"
)

func TestLintFindsBrokenLinksLegacyFrontmatterAndFixtures(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	write(t, filepath.Join(root, "MEMORY.md"), "# Memory\n\n- [Broken](missing.md)\n- [Actual](project-actual.md)\n")
	write(t, filepath.Join(root, "project-actual.md"), `---
name: project-actual
description: When testing memorylint.
metadata:
  type: project
---

See [[missing-memory]]. Contact qa@fixit.invalid from 10.0.0.8.
`)

	report, err := memorylint.Lint([]string{root})
	if err != nil {
		t.Fatalf("Lint() error = %v", err)
	}
	wanted := map[string]bool{"M001": false, "M005": false, "M006": false, "M007": false, "M008": false}
	for _, finding := range report.Findings {
		if _, exists := wanted[finding.Rule]; exists {
			wanted[finding.Rule] = true
		}
	}
	for rule, found := range wanted {
		if !found {
			t.Errorf("Lint() did not report %s: %#v", rule, report.Findings)
		}
	}
}

func TestLintAcceptsHealthyMemoryAndAllowlist(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	write(t, filepath.Join(root, ".memorylint.yaml"), "version: 1\nallowed_emails: [qa@fixit.test]\nallowed_ips: [10.0.0.8]\n")
	write(t, filepath.Join(root, "MEMORY.md"), "# Memory\n\n- [Actual](project-actual.md) — When testing memorylint.\n")
	write(t, filepath.Join(root, "project-actual.md"), `---
name: project-actual
description: When testing memorylint.
type: project
status: active
---

Contact qa@fixit.test from 10.0.0.8.
`)

	report, err := memorylint.Lint([]string{root})
	if err != nil {
		t.Fatalf("Lint() error = %v", err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("Lint() findings = %#v, want none", report.Findings)
	}
}

func TestLintEnforcesTheProvisionalLifecycle(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	note := func(name, status, expires string) {
		front := "---\nname: " + name + "\ndescription: Use when testing lifecycle.\ntype: project\nstatus: " + status + "\n"
		if expires != "" {
			front += "expires: " + expires + "\n"
		}
		write(t, filepath.Join(root, name+".md"), front+"---\n\nBody.\n")
	}
	note("project-no-expires", "provisional", "")
	note("project-bad-expires", "provisional", "someday")
	note("project-expired", "provisional", "2001-01-02")
	note("project-promoted", "active", "2999-01-02")
	note("project-live", "provisional", "2999-01-02")
	index := "# Memory\n"
	for _, name := range []string{"project-no-expires", "project-bad-expires", "project-expired", "project-promoted", "project-live"} {
		index += "- [" + name + "](" + name + ".md) — Use when testing lifecycle.\n"
	}
	write(t, filepath.Join(root, "MEMORY.md"), index)

	report, err := memorylint.Lint([]string{root})
	if err != nil {
		t.Fatalf("Lint() error = %v", err)
	}
	got := map[string]memorylint.Finding{}
	for _, finding := range report.Findings {
		got[filepath.Base(finding.Path)] = finding
	}
	for _, name := range []string{"project-no-expires.md", "project-bad-expires.md", "project-promoted.md"} {
		finding, exists := got[name]
		if !exists || finding.Rule != "M012" || finding.Severity != memorylint.SeverityError {
			t.Errorf("%s: want an M012 error, got %#v", name, got[name])
		}
	}
	expired, exists := got["project-expired.md"]
	if !exists || expired.Rule != "M013" || expired.Severity != memorylint.SeverityWarning {
		t.Errorf("project-expired.md: want an M013 warning, got %#v", expired)
	}
	if exists && !strings.Contains(expired.Message, "deletable on sight") {
		t.Errorf("M013 message must make prune mechanical, got %q", expired.Message)
	}
	if finding, exists := got["project-live.md"]; exists {
		t.Errorf("a live provisional must lint clean, got %#v", finding)
	}
}

func write(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

package memorylint_test

import (
	"os"
	"path/filepath"
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

func write(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

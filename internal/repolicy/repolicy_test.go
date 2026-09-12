package repolicy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fake scripts gh output per command and records every invocation.
type fake struct {
	out   map[string]string
	fail  map[string]error
	calls []string
}

func (f *fake) Run(_, name string, args ...string) (string, error) {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	for prefix, err := range f.fail {
		if strings.Contains(call, prefix) {
			return "", err
		}
	}
	for prefix, out := range f.out {
		if strings.Contains(call, prefix) {
			return out, nil
		}
	}
	return "[]", nil
}

func repos(rows ...string) string { return "[" + strings.Join(rows, ",") + "]" }

func row(name string, deleteOnMerge bool) string {
	return fmt.Sprintf(`{"nameWithOwner":%q,"deleteBranchOnMerge":%t}`, name, deleteOnMerge)
}

func TestAuditReportsOnlyOffPolicyRepos(t *testing.T) {
	f := &fake{out: map[string]string{
		"repo list acme": repos(row("acme/on", true), row("acme/off", false), row("acme/also-off", false)),
	}}

	report, err := Audit(f, DefaultPolicy(), []string{"acme"}, Options{})
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if report.Checked != 3 {
		t.Fatalf("checked = %d, want 3", report.Checked)
	}
	if len(report.Drift) != 2 {
		t.Fatalf("drift = %+v, want 2 rows", report.Drift)
	}
	if report.Drift[0].Repo != "acme/also-off" || report.Drift[0].Want != true || report.Drift[0].Got != false {
		t.Fatalf("drift[0] = %+v, want acme/also-off false→true (sorted)", report.Drift[0])
	}
	// Archived repositories are out of scope: they cannot be patched at all.
	if !strings.Contains(f.calls[0], "--no-archived") {
		t.Fatalf("list call %q must exclude archived repositories", f.calls[0])
	}
	// Only the declared field is requested — never the whole repository object.
	if strings.Contains(f.calls[0], "mergeCommitAllowed") {
		t.Fatalf("list call %q asked for an undeclared field", f.calls[0])
	}
}

func TestApplyPatchesEachDriftingRepoOnce(t *testing.T) {
	f := &fake{out: map[string]string{
		"repo list acme": repos(row("acme/on", true), row("acme/off", false)),
	}}
	policy := Policy{Settings: map[string]bool{"deleteBranchOnMerge": true, "allowSquashMerge": false}}
	f.out["repo list acme"] = `[{"nameWithOwner":"acme/off","deleteBranchOnMerge":false,"squashMergeAllowed":true}]`

	report, err := Apply(f, policy, []string{"acme"}, Options{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(report.Applied) != 1 || !report.Applied[0].OK {
		t.Fatalf("applied = %+v, want one successful change", report.Applied)
	}
	var patches []string
	for _, call := range f.calls {
		if strings.Contains(call, "-X PATCH") {
			patches = append(patches, call)
		}
	}
	if len(patches) != 1 {
		t.Fatalf("patches = %v, want exactly one (both settings in one call)", patches)
	}
	for _, want := range []string{"repos/acme/off", "delete_branch_on_merge=true", "allow_squash_merge=false"} {
		if !strings.Contains(patches[0], want) {
			t.Fatalf("patch %q missing %q", patches[0], want)
		}
	}
}

func TestApplyReportsUnadministrableRepoAndKeepsGoing(t *testing.T) {
	f := &fake{
		out:  map[string]string{"repo list acme": repos(row("acme/denied", false), row("acme/fine", false))},
		fail: map[string]error{"repos/acme/denied": fmt.Errorf("HTTP 403: Must have admin rights")},
	}

	report, err := Apply(f, DefaultPolicy(), []string{"acme"}, Options{})
	if err != nil {
		t.Fatalf("apply must not abort on one refusal: %v", err)
	}
	if len(report.Applied) != 2 {
		t.Fatalf("applied = %+v, want both repositories attempted", report.Applied)
	}
	failures := report.Failures()
	if len(failures) != 1 || failures[0].Repo != "acme/denied" {
		t.Fatalf("failures = %+v, want acme/denied only", failures)
	}
}

func TestExcludedRepoIsNeitherReportedNorPatched(t *testing.T) {
	f := &fake{out: map[string]string{"repo list acme": repos(row("acme/off", false))}}
	policy := DefaultPolicy()
	policy.Exclude = []string{"acme/off"}

	report, err := Apply(f, policy, []string{"acme"}, Options{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(report.Drift) != 0 || len(report.Applied) != 0 {
		t.Fatalf("excluded repo acted on: drift=%+v applied=%+v", report.Drift, report.Applied)
	}
	if report.Checked != 0 || len(report.Skipped) != 1 {
		t.Fatalf("checked=%d skipped=%v, want 0 checked and one skip", report.Checked, report.Skipped)
	}
}

func TestPolicyWithUnknownSettingIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(path, []byte("settings:\n  deleteBranchOnMerg: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadPolicy(path)
	if err == nil || !strings.Contains(err.Error(), "deleteBranchOnMerg") {
		t.Fatalf("err = %v, want the typo named", err)
	}
}

func TestHittingTheLimitWarnsInsteadOfSilentlyTruncating(t *testing.T) {
	f := &fake{out: map[string]string{"repo list acme": repos(row("acme/a", true), row("acme/b", true))}}

	report, err := Audit(f, DefaultPolicy(), []string{"acme"}, Options{Limit: 2})
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "--limit 2") {
		t.Fatalf("warnings = %v, want one about the limit", report.Warnings)
	}
}

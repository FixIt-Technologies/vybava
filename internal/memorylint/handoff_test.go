package memorylint_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/FixIt-Technologies/vybava/internal/memorylint"
)

const goodHandoff = `---
name: %s
description: Finish the thing.
status: %s
created: 2026-09-03
created-by: fcb1420c-5515-420c-bf8a-be94d5d6c247
sessions:
  - fcb1420c-5515-420c-bf8a-be94d5d6c247
  - 85db0d52-5f04-4143-9a13-78781b261750
---

# Handoff
`

func handoffHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), ".claude", "handoffs")
	return home
}

func rules(report memorylint.Report) map[string]int {
	out := map[string]int{}
	for _, f := range report.Findings {
		out[f.Rule]++
	}
	return out
}

func TestHandoffHomeAcceptsBothShapesAndArchive(t *testing.T) {
	t.Parallel()
	home := handoffHome(t)
	writeDeep(t, filepath.Join(home, "cpi", "polish.md"), sprintf(goodHandoff, "polish", "open"))
	writeDeep(t, filepath.Join(home, "cpi", "big", "handoff.md"), sprintf(goodHandoff, "big", "in-progress"))
	writeDeep(t, filepath.Join(home, "cpi", "big", "web.md"), "# Context\n\nNo frontmatter needed here.\n")
	writeDeep(t, filepath.Join(home, "cpi", "archive", "old.md"), sprintf(goodHandoff, "old", "done"))
	writeDeep(t, filepath.Join(home, "cpi", "archive", "gone", "handoff.md"), sprintf(goodHandoff, "gone", "abandoned"))

	report, err := memorylint.Lint([]string{home})
	if err != nil {
		t.Fatalf("Lint() error = %v", err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("Lint() findings = %#v", report.Findings)
	}
	if report.Files != 5 {
		t.Errorf("Files = %d, want 5", report.Files)
	}
}

func TestHandoffSchemaAndPlacement(t *testing.T) {
	t.Parallel()
	home := handoffHome(t)
	writeDeep(t, filepath.Join(home, "cpi", "legacy.md"), "# Handoff: legacy\n\n**Created:** 2026-05-18\n")
	writeDeep(t, filepath.Join(home, "cpi", "misplaced.md"), sprintf(goodHandoff, "misplaced", "done"))
	writeDeep(t, filepath.Join(home, "cpi", "archive", "live.md"), sprintf(goodHandoff, "live", "open"))
	writeDeep(t, filepath.Join(home, "cpi", "bad.md"), `---
name: other
status: paused
created: yesterday
created-by: me
sessions: []
---
`)
	writeDeep(t, filepath.Join(home, "cpi", "leak.md"), sprintf(goodHandoff, "leak", "open")+"\ntoken ghp_abcdefghijklmnopqrstuvwxyz0123\n")
	writeDeep(t, filepath.Join(home, "cpi", "long.md"), sprintf(goodHandoff, "long", "open")+strings.Repeat("line\n", 200))

	report, err := memorylint.Lint([]string{home})
	if err != nil {
		t.Fatalf("Lint() error = %v", err)
	}
	got := rules(report)
	if got["H002"] != 2 {
		t.Errorf("H002 = %d, want 2 (done outside archive, open inside): %#v", got["H002"], report.Findings)
	}
	if got["M011"] != 1 {
		t.Errorf("M011 = %d, want 1", got["M011"])
	}
	if got["H003"] != 1 {
		t.Errorf("H003 = %d, want 1", got["H003"])
	}
	// legacy: missing frontmatter (1); bad: name, description, status, created, created-by, sessions (6)
	if got["H001"] != 7 {
		t.Errorf("H001 = %d, want 7: %#v", got["H001"], report.Findings)
	}
}

func TestHandoffLegacyCreatorIsAccepted(t *testing.T) {
	t.Parallel()
	home := handoffHome(t)
	writeDeep(t, filepath.Join(home, "cpi", "old.md"), `---
name: old
description: Pre-schema handoff.
status: open
created: 2026-05-18
created-by: unknown
sessions:
  - unknown
---
`)
	report, err := memorylint.Lint([]string{home})
	if err != nil {
		t.Fatalf("Lint() error = %v", err)
	}
	if len(report.Findings) != 0 {
		t.Errorf("findings = %#v", report.Findings)
	}
}

func TestHookTargetsHandoffs(t *testing.T) {
	t.Parallel()
	home := handoffHome(t)
	var p memorylint.HookPayload
	p.ToolInput.FilePath = filepath.Join(home, "cpi", "polish.md")
	if got := memorylint.HookTargets(p); len(got) != 1 {
		t.Errorf("HookTargets() = %v, want the handoff", got)
	}
	p.ToolInput.FilePath = filepath.Join(t.TempDir(), "docs", "handoffs", "x.md")
	if got := memorylint.HookTargets(p); len(got) != 0 {
		t.Errorf("HookTargets() = %v, want none for a non-canonical handoffs dir", got)
	}
}

func sprintf(format string, args ...any) string {
	return strings.TrimSpace(fmtSprintf(format, args...)) + "\n"
}

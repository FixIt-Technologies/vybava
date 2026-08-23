package memorylint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The exact shape Claude Code writes back when its Edit tool touches a note in
// the agent-managed memory home (captured 2026-08-22).
const harnessRewritten = `---
name: zz-probe
description: Temporary probe note.
metadata: 
  node_type: memory
  type: reference
  status: active
  originSessionId: fb778e3b-6574-44fa-a2f5-f063c7890e87
  modified: 2026-08-22T20:15:15.860Z
---

# Probe

MARKER
`

func TestNormalizeRepairsHarnessRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zz-probe.md")
	if err := os.WriteFile(path, []byte(harnessRewritten), 0o644); err != nil {
		t.Fatal(err)
	}
	got, changed, err := Normalize(path)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected the legacy envelope to be rewritten")
	}
	out := string(got)
	for _, want := range []string{"name: zz-probe", "type: reference", "status: active", `last-verified: "2026-08-22"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "metadata:") || strings.Contains(out, "originSessionId") || strings.Contains(out, "node_type") {
		t.Errorf("legacy envelope survived:\n%s", out)
	}
	if !strings.Contains(out, "# Probe") || !strings.Contains(out, "MARKER") {
		t.Errorf("body lost:\n%s", out)
	}
	if strings.Contains(out, "---\n---") {
		t.Errorf("body extraction mangled the delimiters:\n%s", out)
	}
}

func TestNormalizeIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zz-probe.md")
	if err := os.WriteFile(path, []byte(harnessRewritten), 0o644); err != nil {
		t.Fatal(err)
	}
	first, _, err := Normalize(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, first, 0o644); err != nil {
		t.Fatal(err)
	}
	second, changed, err := Normalize(path)
	if err != nil {
		t.Fatal(err)
	}
	if changed || string(second) != string(first) {
		t.Fatalf("second pass changed a normalized note:\n%s", second)
	}
}

func TestRefuseManagedBlocksOnlyTheAgentManagedHome(t *testing.T) {
	managed := "/Users/x/.claude/projects/-Users-x-repo/memory/feedback-a.md"
	team := "/Users/x/repo/.claude/memory/project-a.md"

	edit := HookPayload{ToolName: "Edit"}
	if _, refused := RefuseManaged(edit, []string{managed}); !refused {
		t.Error("Edit on the agent-managed home must be refused")
	}
	if _, refused := RefuseManaged(edit, []string{team}); refused {
		t.Error("Edit on the committed team home must NOT be refused — the harness leaves it alone")
	}
	for _, tool := range []string{"Bash", "apply_patch"} {
		if _, refused := RefuseManaged(HookPayload{ToolName: tool}, []string{managed}); refused {
			t.Errorf("%s writes the bytes it is given; it must not be refused", tool)
		}
	}
}

func TestRefsFlagsOnlyUnresolvedReferences(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "project-payments.md"), []byte("---\nname: project-payments\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "service.ts")
	body := "// see `notes/memory/project-payments.md`\n" +
		"// see `notes/memory/" + goneFixture + "`\n" +
		"// see notes/docs/payments/overview.md\n" +
		"// see " + goneFixture + "\n"
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := Refs(home, []string{src}, RefOptions{Prefix: "notes/memory"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 1 || report.Findings[0].Line != 2 {
		t.Fatalf("path mode should flag exactly the qualified path on line 2: %#v", report.Findings)
	}

	bare, err := Refs(home, []string{src}, RefOptions{Prefix: "notes/memory", Bare: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(bare.Findings) != 2 {
		t.Fatalf("bare mode should add the unqualified name on line 4: %#v", bare.Findings)
	}
}

func TestNoteNamePatternIgnoresBarePrefixWords(t *testing.T) {
	// `reference.md` is a real sibling document in several skills.
	for _, quiet := range []string{" see reference.md#anchor", " see user.md", " see project.md", " see .claude/docs/project-old.md"} {
		if m := noteNamePattern.FindStringSubmatch(quiet); m != nil {
			t.Errorf("false positive on %q: %v", quiet, m)
		}
	}
	if noteNamePattern.FindStringSubmatch(" see project-a.md") == nil {
		t.Error("a separator-qualified name must still match")
	}
}

func TestReindexIsDeterministicAndSkipsSuperseded(t *testing.T) {
	home := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(home, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("project-b.md", "---\nname: project-b\ndescription: Use when b.\ntype: project\nstatus: active\n---\n")
	write("project-a.md", "---\nname: project-a\ndescription: Use when a.\ntype: project\nstatus: active\n---\n")
	write("project-old.md", "---\nname: project-old\ndescription: Gone.\ntype: project\nstatus: superseded\n---\n")

	first, err := Reindex(home, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Reindex(home, false)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("reindex is not deterministic")
	}
	out := string(first)
	if strings.Index(out, "project-a") > strings.Index(out, "project-b") {
		t.Errorf("entries must be sorted:\n%s", out)
	}
	if strings.Contains(out, "project-old") {
		t.Errorf("superseded notes must not be indexed:\n%s", out)
	}
}

// Assembled at runtime so a repo-wide `refs --bare` sweep does not read this
// synthetic name as a real reference from committed source.
var goneFixture = "project" + "_gone_from_the_home.md"

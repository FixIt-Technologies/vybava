package press

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testRuntime(t *testing.T) Runtime {
	t.Helper()
	stamps := 0
	return Runtime{
		ExportsRoot: t.TempDir(),
		Dir:         t.TempDir(), // not a git repo: forces explicit project names
		Now: func() string {
			stamps++
			return "2026-01-0" + string(rune('1'+stamps%9)) + "T00:00:00Z"
		},
	}
}

func TestResolveRefusesOutsideGitRepository(t *testing.T) {
	r := testRuntime(t)
	if _, err := r.Resolve(""); err == nil {
		t.Fatal("expected a refusal outside a git repository, got none")
	}
	name, err := r.Resolve("acme")
	if err != nil {
		t.Fatalf("explicit project should always win: %v", err)
	}
	if name != "acme" {
		t.Fatalf("project = %q, want acme", name)
	}
}

func TestInitIsIdempotent(t *testing.T) {
	r := testRuntime(t)
	created, err := r.Init("acme")
	if err != nil || !created {
		t.Fatalf("first init: created=%v err=%v", created, err)
	}
	confPath := r.confPath("acme")
	before, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatal(err)
	}
	created, err = r.Init("acme")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("second init reported creation; it must be idempotent")
	}
	after, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("second init rewrote the config; it must not touch existing state")
	}
}

func TestIndexAddSeedsNoteAndIndexThenUpdatesInPlace(t *testing.T) {
	r := testRuntime(t)
	if _, err := r.Init("acme"); err != nil {
		t.Fatal(err)
	}
	id, created, err := r.IndexAdd("acme", Entry{
		Kind: "pdf", Type: "offer", File: "offer/demo.pdf", Title: "Demo", Status: "draft",
	})
	if err != nil || !created || id != "demo" {
		t.Fatalf("add: id=%q created=%v err=%v", id, created, err)
	}
	note := filepath.Join(r.ProjectDir("acme"), "offer", "demo.md")
	if _, err := os.Stat(note); err != nil {
		t.Fatalf("expected a seeded context note at %s: %v", note, err)
	}
	if err := os.WriteFile(note, []byte("human prose\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, created, err = r.IndexAdd("acme", Entry{Kind: "pdf", File: "offer/demo.pdf", Status: "sent"})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("re-adding the same id must update in place, not create a second entry")
	}
	kept, err := os.ReadFile(note)
	if err != nil || string(kept) != "human prose\n" {
		t.Fatalf("an existing note must never be overwritten, got %q (%v)", kept, err)
	}
	entries, err := r.IndexList("acme", "pdf")
	if err != nil {
		t.Fatal(err)
	}
	list, _ := entries["pdf"].([]any)
	if len(list) != 1 {
		t.Fatalf("want exactly one pdf entry, got %d", len(list))
	}
	if got := list[0].(map[string]any)["status"]; got != "sent" {
		t.Fatalf("status = %v, want sent", got)
	}
}

func TestWritePressMdPreservesProseOutsideMarkers(t *testing.T) {
	r := testRuntime(t)
	if _, err := r.Init("acme"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.ProjectDir("acme"), indexName)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := string(original) + "\n## Hand-written section\n\nKeep me.\n"
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.IndexAdd("acme", Entry{Kind: "pdf", File: "a.pdf", Title: "A"}); err != nil {
		t.Fatal(err)
	}
	regenerated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(regenerated), "Keep me.") {
		t.Fatal("regenerating the index destroyed prose outside the markers")
	}
	if strings.Count(string(regenerated), markerStart) != 1 {
		t.Fatal("expected exactly one autogen block after regeneration")
	}
}

func TestWritePressMdAppendsWhenMarkersAreLost(t *testing.T) {
	r := testRuntime(t)
	if _, err := r.Init("acme"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.ProjectDir("acme"), indexName)
	if err := os.WriteFile(path, []byte("only prose, no markers\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.IndexAdd("acme", Entry{Kind: "pdf", File: "a.pdf", Title: "A"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "only prose, no markers") {
		t.Fatal("prose was destroyed when the markers had been lost")
	}
	if !strings.Contains(string(got), markerStart) {
		t.Fatal("a fresh autogen block should have been appended")
	}
}

func TestLintReportsAndFixes(t *testing.T) {
	r := testRuntime(t)
	if _, err := r.Init("acme"); err != nil {
		t.Fatal(err)
	}
	conf, err := r.loadConf("acme")
	if err != nil {
		t.Fatal(err)
	}
	delete(conf, "design")
	setPath(conf, "project.name", "wrong")
	setPath(conf, "pdf.documents", []any{map[string]any{"file": "ghost.pdf"}})
	if err := r.saveConf("acme", conf); err != nil {
		t.Fatal(err)
	}

	report, err := r.Lint("acme", false)
	if err != nil {
		t.Fatal(err)
	}
	if report.OK() {
		t.Fatal("expected problems from a deliberately broken config")
	}
	joined := strings.Join(report.Problems, "\n")
	for _, want := range []string{"missing section: design", "project.name", "missing id", "not found on disk"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("problems %q missing %q", joined, want)
		}
	}

	report, err = r.Lint("acme", true)
	if err != nil {
		t.Fatal(err)
	}
	// The referenced file genuinely does not exist, so --fix cannot clear it.
	if len(report.Fixed) == 0 {
		t.Fatal("--fix reported nothing fixed")
	}
	after, err := r.loadConf("acme")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := getPath(after, "project.name"); v != "acme" {
		t.Fatalf("project.name = %v after --fix, want acme", v)
	}
	if _, ok := after["design"]; !ok {
		t.Fatal("--fix did not restore the missing section")
	}
}

func TestConfigSetStoresJSONWhenItParses(t *testing.T) {
	r := testRuntime(t)
	if _, err := r.Init("acme"); err != nil {
		t.Fatal(err)
	}
	if err := r.ConfigSet("acme", "design.palette", `["#111","#222"]`); err != nil {
		t.Fatal(err)
	}
	if err := r.ConfigSet("acme", "project.description", "a plain sentence"); err != nil {
		t.Fatal(err)
	}
	value, err := r.ConfigGet("acme", "design.palette")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := value.([]any); !ok {
		t.Fatalf("palette = %#v, want a JSON list", value)
	}
	value, err = r.ConfigGet("acme", "project.description")
	if err != nil {
		t.Fatal(err)
	}
	if value != "a plain sentence" {
		t.Fatalf("description = %#v, want the raw string", value)
	}
}

func TestSaveConfIsDeterministic(t *testing.T) {
	r := testRuntime(t)
	r.Now = func() string { return "2026-01-01T00:00:00Z" }
	if _, err := r.Init("acme"); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(r.confPath("acme"))
	if err != nil {
		t.Fatal(err)
	}
	conf, err := r.loadConf("acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.saveConf("acme", conf); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(r.confPath("acme"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("re-saving an unchanged config produced a different file")
	}
	var probe map[string]any
	if err := json.Unmarshal(second, &probe); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
}

func TestAresRejectsMalformedICO(t *testing.T) {
	r := testRuntime(t)
	for _, bad := range []string{"", "123", "1234567a", "123456789"} {
		if _, err := r.Ares("", bad); err == nil {
			t.Fatalf("IČO %q should have been rejected before any network call", bad)
		}
	}
}

func TestDoctrineIsEmbedded(t *testing.T) {
	if !strings.Contains(Conventions(), "press") {
		t.Fatal("embedded conventions look empty")
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(ConfSchema()), &schema); err != nil {
		t.Fatalf("embedded config schema is not valid JSON: %v", err)
	}
}

func TestProjectNamesCannotEscapeTheExportsRoot(t *testing.T) {
	r := testRuntime(t)
	for _, bad := range []string{"../escape", "../../.config", "a/b", "..", ".", "", "/abs"} {
		if _, err := r.Resolve(bad); err == nil {
			t.Fatalf("Resolve(%q) was accepted; it can escape the exports root", bad)
		}
		if _, err := r.Init(bad); err == nil {
			t.Fatalf("Init(%q) was accepted; it can write outside the exports root", bad)
		}
	}
	if _, err := r.Resolve("acme-2026_v2"); err != nil {
		t.Fatalf("a plain project name must still be accepted: %v", err)
	}
}

func TestArtifactFilesCannotEscapeTheProjectDirectory(t *testing.T) {
	r := testRuntime(t)
	if _, err := r.Init("acme"); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(r.ProjectDir("acme")), "escaped.md")
	for _, bad := range []string{"../escaped.pdf", "../../escaped.pdf", "/etc/escaped.pdf"} {
		if _, _, err := r.IndexAdd("acme", Entry{Kind: "pdf", File: bad, Title: "x"}); err == nil {
			t.Fatalf("IndexAdd accepted --file %q; it can write outside the project", bad)
		}
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatalf("a note was created outside the project at %s", outside)
	}
	if _, _, err := r.IndexAdd("acme", Entry{Kind: "pdf", File: "offer/ok.pdf", Title: "ok"}); err != nil {
		t.Fatalf("a normal relative file must still be accepted: %v", err)
	}
}

func TestIndexAddReportsNoteFailuresInsteadOfSwallowingThem(t *testing.T) {
	r := testRuntime(t)
	if _, err := r.Init("acme"); err != nil {
		t.Fatal(err)
	}
	// A regular file where the note's parent directory must go: MkdirAll fails.
	blocker := filepath.Join(r.ProjectDir("acme"), "offer")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.IndexAdd("acme", Entry{Kind: "pdf", File: "offer/demo.pdf", Title: "Demo"}); err == nil {
		t.Fatal("IndexAdd reported success even though the promised note could not be written")
	}
}

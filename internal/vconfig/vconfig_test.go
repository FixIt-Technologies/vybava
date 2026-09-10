package vconfig

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestFindAndLoadJSON(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "apps", "web")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, FileJSON), []byte(`{"lok":{"catalogs":{"m":{"style":"english-as-key","files":"l/{locale}.json","locales":["en"]}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(sub)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Root != root {
		t.Fatalf("root %q", cfg.Root)
	}
	var lok map[string]map[string]map[string]any
	if err := cfg.Section("lok", &lok); err != nil || lok["catalogs"]["m"]["style"] != "english-as-key" {
		t.Fatalf("section: %v %+v", err, lok)
	}
	if err := cfg.Section("nope", &lok); err == nil {
		t.Fatal("missing section must error")
	}
	if _, err := Load(t.TempDir()); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestLoadTSViaBun(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not installed")
	}
	root := t.TempDir()
	if _, err := WriteHelpers(root, false); err != nil {
		t.Fatal(err)
	}
	src := `import { defineConfig, englishAsKey } from './.vybava/config';
export default defineConfig({ lok: { catalogs: { mobile: englishAsKey('apps/client/locales/{locale}.json', ['en', 'cs'], { required: ['en', 'cs'] }) } } });
`
	if err := os.WriteFile(filepath.Join(root, FileTS), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	var lok struct {
		Catalogs map[string]struct {
			Style    string   `json:"style"`
			Files    string   `json:"files"`
			Locales  []string `json:"locales"`
			Required []string `json:"required"`
		} `json:"catalogs"`
	}
	if err := cfg.Section("lok", &lok); err != nil {
		t.Fatal(err)
	}
	if got := lok.Catalogs["mobile"]; got.Style != "english-as-key" || len(got.Locales) != 2 || got.Required[1] != "cs" {
		t.Fatalf("unexpected %+v", got)
	}
	// second load is served from cache — no bun on PATH needed
	t.Setenv("PATH", "")
	if _, err := Load(root); err != nil {
		t.Fatalf("cached load failed: %v", err)
	}
	if err := CheckHelpers(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(HelperPath(root), []byte("// drift"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckHelpers(root); err != ErrHelperDrift {
		t.Fatalf("want drift, got %v", err)
	}
}

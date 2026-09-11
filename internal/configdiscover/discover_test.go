package configdiscover

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/henderson-tech/vybava/internal/lok"
	"github.com/henderson-tech/vybava/internal/vconfig"
)

func TestDiscoverAndDrift(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	git("init", "-q")
	files := map[string]string{
		"apps/client/locales/en.json":   `{"Save":"Save","One_one":"One_one"}`,
		"apps/client/locales/cs.json":   `{"Save":"Uložit","One_one":"Jeden"}`,
		"apps/web/i18n/strings.cs.json": `{"Save":"Uložit"}`,
		"apps/web/i18n/strings.uk.json": `{"Save":"Зберегти"}`,
		"apps/api/openapi.json":         "{}",
		"vybava.config.json":            `{"guards":{"noRead":["apps/api/openapi.json"]},"lok":{"catalogs":{"mobile":{"style":"english-as-key","files":"apps/client/locales/{locale}.json","locales":["en","cs"]},"webStrings":{"style":"path","files":"apps/web/i18n/strings.{locale}.json","locales":["cs","uk"]}}}}`,
	}
	for p, raw := range files {
		p = filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	r, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if c := r.Lok.Catalogs["mobile"]; c.Style != lok.StyleEnglishAsKey || len(c.Plurals) != 1 {
		t.Fatalf("mobile: %+v", c)
	}
	cfg, err := vconfig.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	warnings, err := r.Drift(cfg)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("drift: %v %v", warnings, err)
	}
	p := filepath.Join(root, "apps/web/i18n/strings.de.json")
	if err := os.WriteFile(p, []byte(`{"Save":"Speichern"}`), 0600); err != nil {
		t.Fatal(err)
	}
	r, err = Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	warnings, err = r.Drift(cfg)
	if err != nil || len(warnings) != 0 {
		t.Fatal("untracked file counted", warnings, err)
	}
	git("add", "apps/web/i18n/strings.de.json")
	r, err = Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	warnings, err = r.Drift(cfg)
	if err != nil || len(warnings) != 1 || !strings.Contains(warnings[0], "strings.de.json") {
		t.Fatalf("new locale: %v %v", warnings, err)
	}
}

func TestWriteAbsentPreservesSections(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, vconfig.FileJSON)
	if err := os.WriteFile(p, []byte(`{"guards":{"maxDumpLines":70}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := vconfig.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	r := Result{Lok: lok.Config{Catalogs: map[string]lok.CatalogConfig{}}}
	written, err := r.WriteAbsent(cfg)
	if err != nil || len(written) != 1 || written[0] != "lok" {
		t.Fatal(written, err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"guards":{"maxDumpLines":70}`) {
		t.Fatal("existing section rewritten")
	}
	cfg, err = vconfig.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if written, err = r.WriteAbsent(cfg); err != nil || len(written) != 0 {
		t.Fatal(written, err)
	}
}

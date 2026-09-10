package lok

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) *Tool {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("locales/en.json", "{\n  \"Archive\": \"Archive\",\n  \"Save\": \"Save\",\n  \"{{count}} hour_one\": \"{{count}} hour\",\n  \"{{count}} hour_other\": \"{{count}} hours\"\n}\n")
	write("locales/cs.json", "{\n  \"Archive\": \"Archivovat\",\n  \"Save\": \"Uložit\",\n  \"{{count}} hour_one\": \"{{count}} hodina\",\n  \"{{count}} hour_few\": \"{{count}} hodiny\",\n  \"{{count}} hour_other\": \"{{count}} hodin\"\n}\n")
	write("dict/en.json", "{\n  \"meta\": {\n    \"title\": \"FixIt\"\n  }\n}\n")
	write("dict/cs.json", "{\n  \"meta\": {\n    \"title\": \"FixIt CZ\"\n  }\n}\n")
	write("src/a.tsx", "const x = t('Save');\nconst y = t(\n  'Brand new'\n);\nconst z = t(`dyn ${k}`);\n")
	write("vybava.config.json", `{"lok":{"catalogs":{
	  "mobile":{"style":"english-as-key","files":"locales/{locale}.json","locales":["en","cs"],"required":["en","cs"],"scan":{"roots":["src"]}},
	  "dict":{"style":"path","files":"dict/{locale}.json","locales":["en","cs"]}}}}`)
	tool, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func TestGetGrepMissingCheck(t *testing.T) {
	tool := fixture(t)
	v, err := tool.Get("", "Save")
	if err != nil || v.Values["cs"] != "Uložit" || v.Catalog != "mobile" {
		t.Fatalf("get: %v %+v", err, v)
	}
	if _, err := tool.Get("", "Nope"); err == nil || err.(*Diag).Code != DiagKeyMissing {
		t.Fatalf("missing key must be KEY_MISSING, got %v", err)
	}
	g, err := tool.Grep("", "ulož", nil, 10)
	if err != nil || g.Total != 1 || g.Hits[0].Key != "Save" {
		t.Fatalf("grep: %v %+v", err, g)
	}
	g, _ = tool.Grep("", ".", nil, 2)
	if !g.Truncated || len(g.Hits) != 2 {
		t.Fatalf("grep cap: %+v", g)
	}
	gaps, total, err := tool.Missing("", nil, true, 10)
	if err != nil || total != 0 {
		t.Fatalf("missing: %v %+v", err, gaps)
	}
	problems, err := tool.Check("")
	if err != nil || len(problems) != 0 {
		t.Fatalf("check: %v %+v", err, problems)
	}
	// nested path lookup
	if v, err := tool.Get("dict", "meta.title"); err != nil || v.Values["cs"] != "FixIt CZ" {
		t.Fatalf("path get: %v %+v", err, v)
	}
}

func TestAddSetRmPreserveOrder(t *testing.T) {
	tool := fixture(t)
	if _, err := tool.Add("mobile", "Cancel", map[string]string{}); err == nil || err.(*Diag).Code != DiagLocaleRequired {
		t.Fatalf("add without cs must fail LOCALE_REQUIRED, got %v", err)
	}
	if _, err := tool.Add("mobile", "Cancel", map[string]string{"cs": "Zrušit", "de": "x"}); err == nil || err.(*Diag).Code != DiagLocaleUnknown {
		t.Fatalf("unknown locale, got %v", err)
	}
	res, err := tool.Add("mobile", "Cancel", map[string]string{"cs": "Zrušit"})
	if err != nil || len(res.Written) != 2 {
		t.Fatalf("add: %v %+v", err, res)
	}
	en, _ := os.ReadFile(filepath.Join(tool.Root, "locales/en.json"))
	want := "{\n  \"Archive\": \"Archive\",\n  \"Cancel\": \"Cancel\",\n  \"Save\": \"Save\","
	if !strings.HasPrefix(string(en), want) {
		t.Fatalf("insertion slot wrong:\n%s", en)
	}
	if _, err := tool.Add("mobile", "Cancel", map[string]string{"cs": "x"}); err == nil || err.(*Diag).Code != DiagKeyExists {
		t.Fatalf("duplicate add, got %v", err)
	}
	if _, err := tool.Set("", "Cancel", map[string]string{"cs": "Storno"}); err != nil {
		t.Fatal(err)
	}
	if v, _ := tool.Get("", "Cancel"); v.Values["cs"] != "Storno" || v.Values["en"] != "Cancel" {
		t.Fatalf("set: %+v", v)
	}
	if _, err := tool.Rm("", "{{count}} hour"); err != nil {
		t.Fatal(err)
	}
	cs, _ := os.ReadFile(filepath.Join(tool.Root, "locales/cs.json"))
	if strings.Contains(string(cs), "hour_few") {
		t.Fatalf("rm must drop plural variants:\n%s", cs)
	}
	// path-style add creates the nesting
	if _, err := tool.Add("dict", "pages.home.title", map[string]string{"en": "Home", "cs": "Domů"}); err != nil {
		t.Fatal(err)
	}
	if v, err := tool.Get("dict", "pages.home.title"); err != nil || v.Values["cs"] != "Domů" {
		t.Fatalf("nested add: %v %+v", err, v)
	}
	// only non-zero diff files are rewritten
	res, _ = tool.Set("dict", "pages.home.title", map[string]string{"cs": "Domů"})
	if len(res.Written) != 0 {
		t.Fatalf("no-op set must write nothing, got %v", res.Written)
	}
}

func TestScan(t *testing.T) {
	tool := fixture(t)
	res, err := tool.Scan("mobile", false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.Calls != 2 || len(res.Missing) != 1 || res.Missing[0] != "Brand new" {
		t.Fatalf("scan: %+v", res)
	}
	if res.OrphanTotal != 2 || !contains(res.Orphans, "Archive") { // Archive + the hour plural base are unused
		t.Fatalf("orphans: %+v", res)
	}
	res, err = tool.Scan("mobile", true, 10)
	if err != nil || len(res.Added) != 1 {
		t.Fatalf("scan --write: %v %+v", err, res)
	}
	gaps, total, _ := tool.Missing("mobile", nil, true, 10)
	if total != 1 || gaps[0].Key != "Brand new" || gaps[0].Locale != "cs" {
		t.Fatalf("after scan, cs must be reported missing: %+v", gaps)
	}
	problems, _ := tool.Check("mobile")
	if len(problems) != 1 || problems[0].Kind != "missing" {
		t.Fatalf("check must flag the gap: %+v", problems)
	}
}

func TestPlaceholderCheck(t *testing.T) {
	tool := fixture(t)
	if _, err := tool.Add("mobile", "Hi {{name}}", map[string]string{"cs": "Ahoj {{jmeno}}"}); err != nil {
		t.Fatal(err)
	}
	problems, _ := tool.Check("mobile")
	if len(problems) != 1 || problems[0].Kind != "placeholders" {
		t.Fatalf("placeholder mismatch must be flagged: %+v", problems)
	}
}

func TestArraysAndScalarsRoundTrip(t *testing.T) {
	src := "{\n  \"hero\": {\n    \"chips\": [\n      \"No fees\",\n      {\n        \"label\": \"Live\",\n        \"count\": 4,\n        \"on\": true,\n        \"none\": null\n      }\n    ]\n  }\n}\n"
	obj, err := ParseObject([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(obj.Marshal()); got != src {
		t.Fatalf("round trip differs:\n%s", got)
	}
	leaves := obj.Leaves()
	if len(leaves) != 2 || leaves[1].Key != "hero.chips.1.label" {
		t.Fatalf("leaves: %+v", leaves)
	}
	c := &Catalog{Config: CatalogConfig{Style: StylePath, Locales: []string{"en"}}, Locales: map[string]*Locale{"en": {Code: "en", Object: obj, Exists: true}}}
	if v, ok := c.Lookup("en", "hero.chips.1.label"); !ok || v != "Live" {
		t.Fatalf("array lookup: %q %v", v, ok)
	}
	if err := c.Put("en", "hero.chips.2", "Third"); err != nil {
		t.Fatal(err)
	}
	if err := c.Put("en", "hero.chips.9", "gap"); err == nil {
		t.Fatal("index past end must fail")
	}
	if !c.Remove("en", "hero.chips.0") {
		t.Fatal("array element remove")
	}
	if v, _ := c.Lookup("en", "hero.chips.1"); v != "Third" {
		t.Fatalf("after remove: %q", v)
	}
}

func TestExemptKeys(t *testing.T) {
	tool := fixture(t)
	tool.Config.Catalogs["mobile"] = withExempt(tool.Config.Catalogs["mobile"], "_help$")
	c, _ := LoadCatalog(tool.Root, "mobile", tool.Config.Catalogs["mobile"])
	_ = c.Put("en", "Join_help", "if you're an employee.")
	_ = c.Put("cs", "Join_help", "pokud jste zaměstnanec.")
	if _, err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if problems, _ := tool.Check("mobile"); len(problems) != 0 {
		t.Fatalf("exempt key must not be flagged: %+v", problems)
	}
	tool.Config.Catalogs["mobile"] = withExempt(tool.Config.Catalogs["mobile"])
	if problems, _ := tool.Check("mobile"); len(problems) != 1 || problems[0].Kind != "english-as-key" {
		t.Fatalf("without exempt it must be flagged: %+v", problems)
	}
}

func withExempt(c CatalogConfig, e ...string) CatalogConfig { c.Exempt = e; return c }

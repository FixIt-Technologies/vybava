package lok

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/henderson-tech/vybava/internal/vconfig"
)

// Closed diagnostic codes. Each fires for one reason and names its fix.
const (
	// DiagConfigMissing — no vybava.config.* above cwd, or no `lok` section.
	DiagConfigMissing = "CONFIG_MISSING"
	// DiagConfigInvalid — the lok section fails Validate().
	DiagConfigInvalid = "CONFIG_INVALID"
	// DiagCatalogAmbiguous — a key or file matches several catalogs; pass --catalog.
	DiagCatalogAmbiguous = "CATALOG_AMBIGUOUS"
	// DiagCatalogUnknown — --catalog names no configured catalog.
	DiagCatalogUnknown = "CATALOG_UNKNOWN"
	// DiagKeyMissing — the key exists in no locale of the catalog.
	DiagKeyMissing = "KEY_MISSING"
	// DiagKeyExists — add on a key that already exists; use set.
	DiagKeyExists = "KEY_EXISTS"
	// DiagLocaleRequired — a write omitted a required locale's value.
	DiagLocaleRequired = "LOCALE_REQUIRED"
	// DiagLocaleUnknown — a --tr names a locale the catalog does not ship.
	DiagLocaleUnknown = "LOCALE_UNKNOWN"
	// DiagCheckFailed — `check` found parity or invariant violations.
	DiagCheckFailed = "CHECK_FAILED"
	// DiagAfterWriteFailed — the catalog's afterWrite command exited non-zero.
	DiagAfterWriteFailed = "AFTER_WRITE_FAILED"
)

// Diag is one diagnostic; Fix is the exact next command when one exists.
type Diag struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

func (d *Diag) Error() string { return d.Code + ": " + d.Detail }

// Tool is one repo's lok configuration, ready to run verbs.
type Tool struct {
	Root   string
	Config Config
}

// Open loads the lok section for cwd.
func Open(cwd string) (*Tool, error) {
	cfg, err := vconfig.Load(cwd)
	if err != nil {
		if errors.Is(err, vconfig.ErrNotFound) {
			return nil, &Diag{Code: DiagConfigMissing, Detail: err.Error(), Fix: "vybava config init"}
		}
		return nil, &Diag{Code: DiagConfigInvalid, Detail: err.Error()}
	}
	var lc Config
	if err := cfg.Section("lok", &lc); err != nil {
		if errors.Is(err, vconfig.ErrNoSection) {
			return nil, &Diag{Code: DiagConfigMissing, Detail: cfg.Path + " has no lok section", Fix: "add lok: { catalogs: { … } } to " + cfg.Path}
		}
		return nil, &Diag{Code: DiagConfigInvalid, Detail: err.Error()}
	}
	if err := lc.Validate(); err != nil {
		return nil, &Diag{Code: DiagConfigInvalid, Detail: err.Error()}
	}
	return &Tool{Root: cfg.Root, Config: lc}, nil
}

// CatalogIDs in stable order.
func (t *Tool) CatalogIDs() []string {
	ids := make([]string, 0, len(t.Config.Catalogs))
	for id := range t.Config.Catalogs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// CatalogFor resolves --catalog, or the single catalog holding key.
func (t *Tool) CatalogFor(id, key string) (*Catalog, error) {
	if id != "" {
		cfg, ok := t.Config.Catalogs[id]
		if !ok {
			return nil, &Diag{Code: DiagCatalogUnknown, Detail: fmt.Sprintf("no catalog %q (have %s)", id, strings.Join(t.CatalogIDs(), ", ")), Fix: "lok catalogs"}
		}
		return LoadCatalog(t.Root, id, cfg)
	}
	if len(t.Config.Catalogs) == 1 {
		for id, cfg := range t.Config.Catalogs {
			return LoadCatalog(t.Root, id, cfg)
		}
	}
	var hits []*Catalog
	for _, cid := range t.CatalogIDs() {
		c, err := LoadCatalog(t.Root, cid, t.Config.Catalogs[cid])
		if err != nil {
			return nil, err
		}
		if key != "" && c.has(key) {
			hits = append(hits, c)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		if key == "" {
			return nil, &Diag{Code: DiagCatalogAmbiguous, Detail: "several catalogs are configured", Fix: "pass --catalog <" + strings.Join(t.CatalogIDs(), "|") + ">"}
		}
		return nil, &Diag{Code: DiagKeyMissing, Detail: fmt.Sprintf("%q is in no catalog", key), Fix: "lok grep " + shellQuote(key)}
	default:
		var ids []string
		for _, h := range hits {
			ids = append(ids, h.ID)
		}
		return nil, &Diag{Code: DiagCatalogAmbiguous, Detail: fmt.Sprintf("%q exists in %s", key, strings.Join(ids, " and ")), Fix: "pass --catalog " + ids[0]}
	}
}

func (c *Catalog) has(key string) bool {
	for _, code := range c.Config.Locales {
		if _, ok := c.Lookup(code, key); ok {
			return true
		}
		for _, s := range c.Config.PluralSuffixes() {
			if _, ok := c.Lookup(code, key+s); ok {
				return true
			}
		}
	}
	return false
}

// CatalogInfo is one row of `lok catalogs`.
type CatalogInfo struct {
	ID       string         `json:"id"`
	Style    Style          `json:"style"`
	Files    string         `json:"files"`
	Locales  []string       `json:"locales"`
	Required []string       `json:"required"`
	Keys     int            `json:"keys"`
	Missing  map[string]int `json:"missing"`
}

// Catalogs describes every catalog with key counts and gaps per locale.
func (t *Tool) Catalogs() ([]CatalogInfo, error) {
	var out []CatalogInfo
	for _, id := range t.CatalogIDs() {
		c, err := LoadCatalog(t.Root, id, t.Config.Catalogs[id])
		if err != nil {
			return nil, err
		}
		info := CatalogInfo{ID: id, Style: c.Config.Style, Files: c.Config.Files, Locales: c.Config.Locales, Required: c.Config.RequiredLocales(), Missing: map[string]int{}}
		keys := c.Keys()
		info.Keys = len(keys)
		for _, code := range c.Config.Locales {
			for _, k := range keys {
				if _, ok := c.Lookup(code, k); !ok && c.expectedIn(code, k) {
					info.Missing[code]++
				}
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// expectedIn: plural variants are locale-specific (Czech has _few/_many,
// English does not), so a plural key missing from another locale is not a gap.
func (c *Catalog) expectedIn(locale, key string) bool {
	if _, plural := c.Config.BaseKey(key); plural {
		return false
	}
	return true
}

// Values is a key's value per locale ("" and absent=false where missing).
type Values struct {
	Catalog string            `json:"catalog"`
	Key     string            `json:"key"`
	Values  map[string]string `json:"values"`
	Missing []string          `json:"missing"`
}

func (t *Tool) values(c *Catalog, key string) Values {
	v := Values{Catalog: c.ID, Key: key, Values: map[string]string{}}
	for _, code := range c.Config.Locales {
		found := false
		if s, ok := c.Lookup(code, key); ok {
			v.Values[code], found = s, true
		}
		for _, sfx := range c.Config.PluralSuffixes() {
			if s, ok := c.Lookup(code, key+sfx); ok {
				v.Values[code+sfx], found = s, true
			}
		}
		if !found && c.expectedIn(code, key) {
			v.Missing = append(v.Missing, code)
		}
	}
	if v.Missing == nil {
		v.Missing = []string{}
	}
	return v
}

// Get returns one key across locales.
func (t *Tool) Get(catalogID, key string) (Values, error) {
	c, err := t.CatalogFor(catalogID, key)
	if err != nil {
		return Values{}, err
	}
	if !c.has(key) {
		return Values{}, &Diag{Code: DiagKeyMissing, Detail: fmt.Sprintf("%q is not in catalog %s", key, c.ID), Fix: "lok grep " + shellQuote(key)}
	}
	return t.values(c, key), nil
}

// Hit is one grep match.
type Hit struct {
	Catalog string `json:"catalog"`
	Key     string `json:"key"`
	Locale  string `json:"locale"`
	Value   string `json:"value"`
}

// GrepResult caps output so a broad pattern never floods the context.
type GrepResult struct {
	Hits      []Hit `json:"hits"`
	Total     int   `json:"total"`
	Truncated bool  `json:"truncated"`
}

// Grep matches a regex (case-insensitive) against keys and values.
func (t *Tool) Grep(catalogID, pattern string, locales []string, limit int) (GrepResult, error) {
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return GrepResult{}, &Diag{Code: DiagConfigInvalid, Detail: "bad pattern: " + err.Error()}
	}
	ids := t.CatalogIDs()
	if catalogID != "" {
		if _, ok := t.Config.Catalogs[catalogID]; !ok {
			return GrepResult{}, &Diag{Code: DiagCatalogUnknown, Detail: "no catalog " + catalogID, Fix: "lok catalogs"}
		}
		ids = []string{catalogID}
	}
	res := GrepResult{Hits: []Hit{}}
	for _, id := range ids {
		c, err := LoadCatalog(t.Root, id, t.Config.Catalogs[id])
		if err != nil {
			return res, err
		}
		for _, code := range c.Config.Locales {
			if len(locales) > 0 && !contains(locales, code) {
				continue
			}
			for _, e := range c.Locales[code].Object.Leaves() {
				val := e.Value.(string)
				if re.MatchString(e.Key) || re.MatchString(val) {
					res.Total++
					if len(res.Hits) < limit {
						res.Hits = append(res.Hits, Hit{Catalog: id, Key: e.Key, Locale: code, Value: val})
					}
				}
			}
		}
	}
	res.Truncated = res.Total > len(res.Hits)
	return res, nil
}

// WriteResult reports a write: files touched and the afterWrite outcome.
type WriteResult struct {
	Catalog    string   `json:"catalog"`
	Key        string   `json:"key"`
	Written    []string `json:"written"`
	AfterWrite string   `json:"afterWrite,omitempty"`
}

// Add inserts a new key with per-locale values. english-as-key catalogs
// derive `en` from the key; every required locale needs a value.
func (t *Tool) Add(catalogID, key string, tr map[string]string) (WriteResult, error) {
	c, err := t.CatalogFor(catalogID, "")
	if err != nil {
		return WriteResult{}, err
	}
	if c.has(key) {
		return WriteResult{}, &Diag{Code: DiagKeyExists, Detail: fmt.Sprintf("%q already exists in %s", key, c.ID), Fix: "lok set " + shellQuote(key) + " --tr <locale>=<value>"}
	}
	return t.write(c, key, tr, true)
}

// Set updates an existing key in the given locales (others untouched).
func (t *Tool) Set(catalogID, key string, tr map[string]string) (WriteResult, error) {
	c, err := t.CatalogFor(catalogID, key)
	if err != nil {
		return WriteResult{}, err
	}
	if !c.has(key) {
		return WriteResult{}, &Diag{Code: DiagKeyMissing, Detail: fmt.Sprintf("%q is not in %s", key, c.ID), Fix: "lok add " + shellQuote(key) + " --tr <locale>=<value>"}
	}
	return t.write(c, key, tr, false)
}

func (t *Tool) write(c *Catalog, key string, tr map[string]string, requireAll bool) (WriteResult, error) {
	tr = cloneMap(tr)
	if c.Config.Style == StyleEnglishAsKey && contains(c.Config.Locales, "en") {
		if v, ok := tr["en"]; ok && v != key {
			return WriteResult{}, &Diag{Code: DiagConfigInvalid, Detail: "english-as-key: the en value IS the key; do not pass --tr en=…"}
		}
		tr["en"] = key
	}
	for code := range tr {
		if !contains(c.Config.Locales, code) {
			return WriteResult{}, &Diag{Code: DiagLocaleUnknown, Detail: fmt.Sprintf("catalog %s has no locale %q (have %s)", c.ID, code, strings.Join(c.Config.Locales, ", ")), Fix: "lok catalogs"}
		}
	}
	if requireAll {
		var missing []string
		for _, code := range c.Config.RequiredLocales() {
			if _, ok := tr[code]; !ok {
				missing = append(missing, code)
			}
		}
		if len(missing) > 0 {
			var flags []string
			for _, m := range missing {
				flags = append(flags, "--tr "+m+"=<value>")
			}
			return WriteResult{}, &Diag{Code: DiagLocaleRequired, Detail: fmt.Sprintf("%s requires %s", c.ID, strings.Join(missing, ", ")), Fix: "lok add " + shellQuote(key) + " " + strings.Join(flags, " ")}
		}
	}
	if len(tr) == 0 {
		return WriteResult{}, &Diag{Code: DiagLocaleRequired, Detail: "nothing to write", Fix: "pass --tr <locale>=<value>"}
	}
	for code, val := range tr {
		if err := c.Put(code, key, val); err != nil {
			return WriteResult{}, &Diag{Code: DiagConfigInvalid, Detail: err.Error()}
		}
	}
	return t.commit(c, key)
}

// Rm deletes a key (and, for english-as-key, its plural variants) everywhere.
func (t *Tool) Rm(catalogID, key string) (WriteResult, error) {
	c, err := t.CatalogFor(catalogID, key)
	if err != nil {
		return WriteResult{}, err
	}
	removed := false
	for _, code := range c.Config.Locales {
		if c.Remove(code, key) {
			removed = true
		}
		for _, s := range c.Config.PluralSuffixes() {
			if c.Remove(code, key+s) {
				removed = true
			}
		}
	}
	if !removed {
		return WriteResult{}, &Diag{Code: DiagKeyMissing, Detail: fmt.Sprintf("%q is not in %s", key, c.ID), Fix: "lok grep " + shellQuote(key)}
	}
	return t.commit(c, key)
}

func (t *Tool) commit(c *Catalog, key string) (WriteResult, error) {
	written, err := c.Save()
	if err != nil {
		return WriteResult{}, err
	}
	res := WriteResult{Catalog: c.ID, Key: key, Written: rel(t.Root, written)}
	if c.Config.AfterWrite != "" && len(written) > 0 {
		cmd := exec.Command("sh", "-c", c.Config.AfterWrite)
		cmd.Dir = t.Root
		if out, err := cmd.CombinedOutput(); err != nil {
			return res, &Diag{Code: DiagAfterWriteFailed, Detail: fmt.Sprintf("%s: %s", c.Config.AfterWrite, lastLines(string(out), 5)), Fix: "(cd " + t.Root + " && " + c.Config.AfterWrite + ")"}
		}
		res.AfterWrite = c.Config.AfterWrite
	}
	return res, nil
}

// Gap is one missing translation.
type Gap struct {
	Catalog string `json:"catalog"`
	Key     string `json:"key"`
	Locale  string `json:"locale"`
	Source  string `json:"source,omitempty"`
}

// Missing lists keys absent from a locale that should carry them.
func (t *Tool) Missing(catalogID string, locales []string, requiredOnly bool, limit int) ([]Gap, int, error) {
	ids := t.CatalogIDs()
	if catalogID != "" {
		ids = []string{catalogID}
	}
	var gaps []Gap
	total := 0
	for _, id := range ids {
		cfg, ok := t.Config.Catalogs[id]
		if !ok {
			return nil, 0, &Diag{Code: DiagCatalogUnknown, Detail: "no catalog " + id, Fix: "lok catalogs"}
		}
		c, err := LoadCatalog(t.Root, id, cfg)
		if err != nil {
			return nil, 0, err
		}
		targets := c.Config.Locales
		if requiredOnly {
			targets = c.Config.RequiredLocales()
		}
		for _, k := range c.Keys() {
			for _, code := range targets {
				if len(locales) > 0 && !contains(locales, code) {
					continue
				}
				if _, ok := c.Lookup(code, k); ok || !c.expectedIn(code, k) {
					continue
				}
				total++
				if len(gaps) < limit {
					gaps = append(gaps, Gap{Catalog: id, Key: k, Locale: code, Source: t.sourceValue(c, k)})
				}
			}
		}
	}
	if gaps == nil {
		gaps = []Gap{}
	}
	return gaps, total, nil
}

func (t *Tool) sourceValue(c *Catalog, key string) string {
	if c.Config.Style == StyleEnglishAsKey {
		return key
	}
	for _, code := range c.Config.Locales {
		if v, ok := c.Lookup(code, key); ok {
			return v
		}
	}
	return ""
}

// Problem is one `check` finding.
type Problem struct {
	Catalog string `json:"catalog"`
	Kind    string `json:"kind"`
	Key     string `json:"key,omitempty"`
	Locale  string `json:"locale,omitempty"`
	Detail  string `json:"detail"`
}

var rePlaceholder = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_.]+)\s*\}\}`)

// Check enforces: every locale file exists; required locales carry every
// key; english-as-key en[key]==key; placeholders agree across locales.
func (t *Tool) Check(catalogID string) ([]Problem, error) {
	ids := t.CatalogIDs()
	if catalogID != "" {
		ids = []string{catalogID}
	}
	problems := []Problem{}
	for _, id := range ids {
		cfg, ok := t.Config.Catalogs[id]
		if !ok {
			return nil, &Diag{Code: DiagCatalogUnknown, Detail: "no catalog " + id, Fix: "lok catalogs"}
		}
		c, err := LoadCatalog(t.Root, id, cfg)
		if err != nil {
			return nil, err
		}
		for _, code := range c.Config.Locales {
			if !c.Locales[code].Exists {
				problems = append(problems, Problem{Catalog: id, Kind: "file-missing", Locale: code, Detail: c.Locales[code].Path})
			}
		}
		for _, k := range c.Keys() {
			for _, code := range c.Config.RequiredLocales() {
				if _, ok := c.Lookup(code, k); !ok && c.expectedIn(code, k) {
					problems = append(problems, Problem{Catalog: id, Kind: "missing", Key: k, Locale: code, Detail: "required locale has no value"})
				}
			}
			if _, plural := c.Config.BaseKey(k); c.Config.Style == StyleEnglishAsKey && !plural && !c.Config.Exempted(k) {
				if v, ok := c.Lookup("en", k); ok && v != k {
					problems = append(problems, Problem{Catalog: id, Kind: "english-as-key", Key: k, Locale: "en", Detail: fmt.Sprintf("en value %q must equal the key", v)})
				}
			}
			var ref []string
			refLocale := ""
			for _, code := range c.Config.Locales {
				v, ok := c.Lookup(code, k)
				if !ok {
					continue
				}
				ph := placeholders(v)
				if refLocale == "" {
					ref, refLocale = ph, code
					continue
				}
				if strings.Join(ph, ",") != strings.Join(ref, ",") {
					problems = append(problems, Problem{Catalog: id, Kind: "placeholders", Key: k, Locale: code, Detail: fmt.Sprintf("{{…}} set %v differs from %s %v", ph, refLocale, ref)})
				}
			}
		}
	}
	return problems, nil
}

func placeholders(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range rePlaceholder.FindAllStringSubmatch(s, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

func cloneMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func rel(root string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if r, err := filepath.Rel(root, p); err == nil {
			out = append(out, r)
		} else {
			out = append(out, p)
		}
	}
	return out
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func shellQuote(s string) string {
	if !strings.ContainsAny(s, " '\"$`\\{}") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ensure os is used on all platforms
var _ = os.ErrNotExist

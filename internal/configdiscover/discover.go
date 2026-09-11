// Package configdiscover composes lok parsing and vconfig without making the
// config loader depend on its consumers. Discovery is advisory, never policy.
package configdiscover

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/henderson-tech/vybava/internal/claudeguards"
	"github.com/henderson-tech/vybava/internal/lok"
	"github.com/henderson-tech/vybava/internal/vconfig"
)

type Candidate struct {
	Path    string   `json:"path"`
	Reasons []string `json:"reasons"`
}
type Result struct {
	Guards struct {
		NoRead []string `json:"noRead"`
	} `json:"guards"`
	Lok        lok.Config  `json:"lok"`
	Candidates []Candidate `json:"candidates"`
	Warnings   []string    `json:"warnings"`
}

var localeRE = regexp.MustCompile(`(^|[/.])([a-z]{2}(?:-[A-Z]{2})?)(\.json$|/)`)
var generatedPath = regexp.MustCompile(`(?i)(generated|__generated__|\.gen\.|openapi|schema|\.lock$|\.min\.)`)

func Discover(root string) (Result, error) {
	r := Result{Lok: lok.Config{Catalogs: map[string]lok.CatalogConfig{}}, Candidates: []Candidate{}, Warnings: []string{}}
	r.Guards.NoRead = []string{}
	cmd := exec.Command("git", "-C", root, "ls-files", "-z")
	out, err := cmd.Output()
	if err != nil {
		return r, fmt.Errorf("tracked files: %w", err)
	}
	files := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	attrs := exec.Command("git", "-C", root, "check-attr", "-z", "--stdin", "linguist-generated")
	attrs.Stdin = bytes.NewReader(out)
	attrOut, err := attrs.Output()
	if err != nil {
		return r, fmt.Errorf("generated attributes: %w", err)
	}
	generated := map[string]bool{}
	a := strings.Split(string(attrOut), "\x00")
	for i := 0; i+2 < len(a); i += 3 {
		generated[a[i]] = a[i+2] == "set" || a[i+2] == "true"
	}
	ignores := []string{}
	for _, name := range []string{".prettierignore", ".biomeignore"} {
		b, err := os.ReadFile(filepath.Join(root, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return r, err
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				ignores = append(ignores, line)
			}
		}
	}
	// Biome 2 keeps exclusion globs in files.includes. Parsing failure stays
	// visible because JSONC needs manual review instead of silent omission.
	for _, name := range []string{"biome.json", "biome.jsonc"} {
		b, err := os.ReadFile(filepath.Join(root, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return r, err
		}
		var config struct {
			Files struct {
				Includes []string `json:"includes"`
				Ignore   []string `json:"ignore"`
			} `json:"files"`
		}
		if err := json.Unmarshal(b, &config); err != nil {
			r.Warnings = append(r.Warnings, name+": formatter exclusions could not be parsed; review JSONC exclusions manually")
			continue
		}
		ignores = append(ignores, config.Files.Ignore...)
		for _, pattern := range config.Files.Includes {
			if strings.HasPrefix(pattern, "!") {
				ignores = append(ignores, strings.TrimLeft(pattern, "!"))
			}
		}
	}
	type catalogFile struct {
		locale string
		leaves []lok.Entry
	}
	groups := map[string][]catalogFile{}
	for _, name := range files {
		if name == "" {
			continue
		}
		p := filepath.Join(root, name)
		st, err := os.Lstat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return r, err
		}
		if !st.Mode().IsRegular() {
			continue
		}
		// Discovery never follows links or loads arbitrary-size binary payloads.
		var raw []byte
		if st.Size() <= 16<<20 {
			raw, err = os.ReadFile(p)
			if err != nil {
				return r, err
			}
		}
		reasons := []string{}
		if st.Size() > 256<<10 {
			reasons = append(reasons, "size > 256 KiB")
		}
		if bytes.Count(raw, []byte{'\n'}) > 2000 {
			reasons = append(reasons, "lines > 2000")
		}
		if generatedPath.MatchString(name) {
			reasons = append(reasons, "generated path")
		}
		first := bytes.SplitN(raw, []byte{'\n'}, 4)
		if len(first) > 3 {
			first = first[:3]
		}
		header := strings.ToLower(string(bytes.Join(first, []byte{'\n'})))
		if strings.Contains(header, "generated") || strings.Contains(header, "do not edit") {
			reasons = append(reasons, "generated header")
		}
		if generated[name] {
			reasons = append(reasons, "linguist-generated")
		}
		for _, pattern := range ignores {
			if claudeguards.MatchNoRead(strings.TrimPrefix(pattern, "/"), name) {
				reasons = append(reasons, "formatter ignore")
				break
			}
		}
		if len(reasons) > 0 {
			r.Candidates = append(r.Candidates, Candidate{Path: name, Reasons: reasons})
			// A schema mention or a large source file is evidence to review,
			// not sufficient reason to forbid reading its source.
			strongPath := strings.Contains(name, "/generated/") || strings.Contains(name, "/__generated__/") || strings.Contains(name, ".gen.") || strings.HasSuffix(name, ".lock") || strings.Contains(name, ".min.") || strings.Contains(filepath.Base(name), "openapi.") || filepath.Base(name) == "translation-keys.d.ts"
			if strongPath || generated[name] || (st.Size() > 256<<10 && generatedPath.MatchString(name)) {
				r.Guards.NoRead = append(r.Guards.NoRead, name)
			}
		}
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		m := localeRE.FindStringSubmatchIndex(name)
		if m == nil {
			continue
		}
		locale := name[m[4]:m[5]]
		pattern := name[:m[4]] + "{locale}" + name[m[5]:]
		o, err := lok.ParseObject(raw)
		if err != nil {
			continue
		}
		leaves := o.Leaves()
		if len(leaves) == 0 {
			continue
		}
		groups[pattern] = append(groups[pattern], catalogFile{locale: locale, leaves: leaves})
	}
	patterns := make([]string, 0, len(groups))
	for p := range groups {
		patterns = append(patterns, p)
	}
	sort.Strings(patterns)
	for _, p := range patterns {
		g := groups[p]
		if len(g) < 2 {
			continue
		}
		sort.Slice(g, func(i, j int) bool { return g[i].locale < g[j].locale })
		c := lok.CatalogConfig{Files: p, Style: lok.StylePath, Locales: []string{}}
		most := g[0]
		plural := map[string]bool{}
		for _, f := range g {
			c.Locales = append(c.Locales, f.locale)
			if len(f.leaves) > len(most.leaves) {
				most = f
			}
			equal := 0
			for _, e := range f.leaves {
				if value, ok := e.Value.(string); ok && e.Key == value {
					equal++
				}
				for _, suffix := range []string{"_zero", "_one", "_two", "_few", "_many", "_other"} {
					if strings.HasSuffix(e.Key, suffix) {
						plural[suffix] = true
					}
				}
			}
			if f.locale == "en" && equal*10 > len(f.leaves)*9 {
				c.Style = lok.StyleEnglishAsKey
			}
		}
		c.Required = []string{most.locale}
		for suffix := range plural {
			c.Plurals = append(c.Plurals, suffix)
		}
		sort.Strings(c.Plurals)
		id := catalogID(p)
		base := id
		for n := 2; ; n++ {
			if _, exists := r.Lok.Catalogs[id]; !exists {
				break
			}
			id = fmt.Sprintf("%s%d", base, n)
		}
		r.Lok.Catalogs[id] = c
		r.Warnings = append(r.Warnings, fmt.Sprintf("%s: required locale %s is a guess based on the largest key count; review style and locale policy", id, most.locale))
	}
	// Catalogs already have a scoped reader; avoid proposing a second policy.
	kept := r.Guards.NoRead[:0]
	for _, name := range r.Guards.NoRead {
		catalog := false
		for _, c := range r.Lok.Catalogs {
			for _, loc := range c.Locales {
				if strings.ReplaceAll(c.Files, "{locale}", loc) == name {
					catalog = true
				}
			}
		}
		if !catalog {
			kept = append(kept, name)
		}
	}
	unique := map[string]bool{}
	r.Guards.NoRead = []string{}
	for _, name := range kept {
		pattern := name
		if filepath.Base(name) == "translation-keys.d.ts" {
			pattern = "**/translation-keys.d.ts"
		}
		for _, part := range []string{"/generated/", "/__generated__/"} {
			if i := strings.Index(name, part); i >= 0 {
				pattern = name[:i+len(part)] + "**"
				break
			}
		}
		if !unique[pattern] {
			unique[pattern] = true
			r.Guards.NoRead = append(r.Guards.NoRead, pattern)
		}
	}
	return r, nil
}

func catalogID(pattern string) string {
	p := strings.TrimSuffix(strings.ReplaceAll(pattern, "{locale}", ""), ".json")
	parts := strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '.' || r == '-' || r == '_' })
	var words []string
	for _, part := range parts {
		if part == "apps" || part == "packages" || part == "i18n" || part == "locales" {
			continue
		}
		if part == "client" {
			part = "mobile"
		}
		words = append(words, part)
	}
	if len(words) == 0 {
		return "catalog"
	}
	for i := 1; i < len(words); i++ {
		words[i] = strings.ToUpper(words[i][:1]) + words[i][1:]
	}
	return strings.Join(words, "")
}

func (r Result) Snippet() string {
	sections := struct {
		Guards struct {
			NoRead []string `json:"noRead"`
		} `json:"guards"`
		Lok lok.Config `json:"lok"`
	}{r.Guards, r.Lok}
	raw, _ := json.MarshalIndent(sections, "", "  ")
	return "// Suggestions only. Required locales and catalog styles need review.\nexport default " + string(raw) + ";\n"
}

func (r Result) Drift(cfg *vconfig.Config) ([]string, error) {
	var guards struct {
		NoRead []string `json:"noRead"`
	}
	if raw, ok := cfg.Sections["guards"]; ok {
		if err := json.Unmarshal(raw, &guards); err != nil {
			return nil, err
		}
	}
	var catalogs lok.Config
	if raw, ok := cfg.Sections["lok"]; ok {
		if err := json.Unmarshal(raw, &catalogs); err != nil {
			return nil, err
		}
	}
	warnings := []string{}
	for _, name := range r.Guards.NoRead {
		covered := false
		for _, p := range guards.NoRead {
			if claudeguards.MatchNoRead(p, name) {
				covered = true
			}
		}
		if !covered {
			warnings = append(warnings, "unconfigured generated file: "+name)
		}
	}
	for _, discovered := range r.Lok.Catalogs {
		for _, locale := range discovered.Locales {
			file := strings.ReplaceAll(discovered.Files, "{locale}", locale)
			covered := false
			for _, c := range catalogs.Catalogs {
				for _, loc := range c.Locales {
					if strings.ReplaceAll(c.Files, "{locale}", loc) == file {
						covered = true
					}
				}
			}
			if !covered {
				warnings = append(warnings, "unconfigured locale catalog: "+file)
			}
		}
	}
	sort.Strings(warnings)
	return warnings, nil
}

// Package press is the determinism layer of the press skill family
// (press-pdf, press-logo, press-offer, press-email).
//
// Every state mutation of ~/Exports/<project>/ — .press.conf.json, PRESS.md,
// artifact notes — goes through this package so an agent never hand-edits
// state. Creation is idempotent and never destructive: existing prose in
// PRESS.md survives regeneration, and an artifact note is written only when it
// does not already exist.
package press

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Version is the config-schema version stamped into meta.skillVersion. It
// tracks the press payload contract, not the Výbava binary version.
const Version = "1.0.0"

const (
	confName     = ".press.conf.json"
	indexName    = "PRESS.md"
	markerStart  = "<!-- press:index:start -->"
	markerEnd    = "<!-- press:index:end -->"
	aresEndpoint = "https://ares.gov.cz/ekonomicke-subjekty-v-be/rest/ekonomicke-subjekty/"
)

// Runtime carries the environment a press command runs against. The zero value
// is not usable — build one with New.
type Runtime struct {
	// ExportsRoot is the deliverable home, ~/Exports unless PRESS_EXPORTS says
	// otherwise. Tests point it at a temp dir.
	ExportsRoot string
	// Now returns the timestamp stamped into config and index entries.
	// Overridable so tests can assert byte-identical output.
	Now func() string
	// Dir is the working directory used to resolve the project from git.
	Dir string
	// HTTP is the client used for ARES lookups.
	HTTP *http.Client
	// AresEndpoint is the registry base URL; tests point it at a stub.
	AresEndpoint string
	Out          io.Writer
}

// New builds a Runtime from the process environment.
func New(out io.Writer) Runtime {
	return Runtime{
		ExportsRoot:  DefaultExportsRoot(),
		Now:          func() string { return time.Now().UTC().Format(time.RFC3339) },
		HTTP:         &http.Client{Timeout: 15 * time.Second},
		AresEndpoint: aresEndpoint,
		Out:          out,
	}
}

// DefaultExportsRoot is ~/Exports unless PRESS_EXPORTS overrides it.
func DefaultExportsRoot() string {
	if v := os.Getenv("PRESS_EXPORTS"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Exports")
}

func (r Runtime) stamp() string {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now().UTC().Format(time.RFC3339)
}

// ---------- project resolution ----------

func (r Runtime) gitTopLevel() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = r.Dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (r Runtime) gitOrigin(dir string) string {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// checkProjectName rejects anything that would let a project name walk out of
// the exports root. The name becomes a single directory under ~/Exports, so a
// separator or a dot-segment in it is never legitimate — and `--project` is
// caller-supplied, which is exactly the value an agent is most likely to pass
// through from somewhere else.
func checkProjectName(name string) error {
	switch {
	case name == "":
		return errors.New("project name is empty")
	case name == "." || name == "..":
		return fmt.Errorf("invalid project name %q", name)
	case strings.ContainsAny(name, `/\`+"\x00"):
		return fmt.Errorf("invalid project name %q: must be a single directory name, not a path", name)
	}
	return nil
}

// escapes reports whether target sits outside base, given both as clean paths.
func escapes(base, target string) bool {
	inside, err := filepath.Rel(base, target)
	return err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator))
}

// resolveInside joins rel under base and proves the result stayed there, so a
// caller-supplied path such as "../../target" cannot reach outside the project.
//
// The lexical check alone is not enough: a symlink inside the project — say
// <exports>/acme/offer pointing elsewhere — satisfies it while still writing
// outside. So the real paths are compared too, resolving the deepest ancestor
// that exists yet (the leaf usually does not, since this runs before creating
// it).
func resolveInside(base, rel string) (string, error) {
	if rel == "" {
		return "", errors.New("empty path")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be relative to the project directory", rel)
	}
	joined := filepath.Join(base, rel)
	if escapes(base, joined) {
		return "", fmt.Errorf("path %q escapes the project directory", rel)
	}

	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return joined, nil // nothing exists to traverse yet
		}
		return "", fmt.Errorf("resolve project directory: %w", err)
	}
	probe := joined
	for {
		if _, err := os.Lstat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe || len(parent) < len(base) {
			return joined, nil // no existing ancestor below base to resolve
		}
		probe = parent
	}
	realProbe, err := filepath.EvalSymlinks(probe)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", rel, err)
	}
	if realProbe != realBase && escapes(realBase, realProbe) {
		return "", fmt.Errorf("path %q escapes the project directory through a symlink", rel)
	}
	return joined, nil
}

// Resolve returns the project name. An explicit override always wins;
// otherwise it is the git repository name. Outside a git repository there is
// no fallback by design — the caller must ask the human and pass a name.
func (r Runtime) Resolve(override string) (string, error) {
	if override != "" {
		if err := checkProjectName(override); err != nil {
			return "", err
		}
		return override, nil
	}
	top, err := r.gitTopLevel()
	if err != nil {
		return "", errors.New("not inside a git repository — ask the user and pass --project <name>")
	}
	name := filepath.Base(top)
	if err := checkProjectName(name); err != nil {
		return "", err
	}
	return name, nil
}

// ProjectDir is the deliverable home for one project.
func (r Runtime) ProjectDir(name string) string { return filepath.Join(r.ExportsRoot, name) }

func (r Runtime) confPath(name string) string { return filepath.Join(r.ProjectDir(name), confName) }

// ---------- config ----------

func (r Runtime) defaultConf(name string) map[string]any {
	dir, git := "", ""
	if top, err := r.gitTopLevel(); err == nil {
		dir = top
		git = r.gitOrigin(top)
	}
	return map[string]any{
		"project": map[string]any{
			"name":        name,
			"type":        "",
			"description": "",
			"git":         git,
			"dir":         dir,
		},
		"logo":   map[string]any{},
		"pdf":    map[string]any{"documents": []any{}},
		"design": map[string]any{},
		"meta": map[string]any{
			"skillVersion": Version,
			"createdAt":    r.stamp(),
			"updatedAt":    r.stamp(),
		},
	}
}

func (r Runtime) loadConf(name string) (map[string]any, error) {
	b, err := os.ReadFile(r.confPath(name))
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", r.confPath(name), err)
	}
	return m, nil
}

// saveConf writes with sorted keys — MarshalIndent on maps is alphabetical —
// for deterministic output and clean diffs.
func (r Runtime) saveConf(name string, m map[string]any) error {
	if meta, ok := m["meta"].(map[string]any); ok {
		meta["updatedAt"] = r.stamp()
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.confPath(name), append(b, '\n'), 0o644)
}

// ---------- dot-path access ----------

func getPath(m map[string]any, path string) (any, bool) {
	var cur any = m
	for _, p := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func setPath(m map[string]any, path string, val any) {
	parts := strings.Split(path, ".")
	cur := m
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = val
}

// ---------- index entries ----------

func entriesKey(kind string) (section, key string) {
	switch kind {
	case "pdf":
		return "pdf", "documents"
	case "logo":
		return "logo", "artifacts"
	case "design":
		return "design", "artifacts"
	}
	return "", ""
}

var slugPattern = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSuffix(filepath.Base(s), filepath.Ext(s)))
	return strings.Trim(slugPattern.ReplaceAllString(s, "-"), "-")
}

// ---------- PRESS.md ----------

func renderIndexBlock(conf map[string]any) string {
	var b strings.Builder
	b.WriteString(markerStart + "\n")
	b.WriteString("_Auto-generated by `press` — edit outside the markers only._\n")
	for _, kind := range []string{"logo", "pdf", "design"} {
		section, key := entriesKey(kind)
		raw, _ := getPath(conf, section+"."+key)
		list, _ := raw.([]any)
		if len(list) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n## %s\n\n", strings.ToUpper(kind[:1])+kind[1:])
		for _, e := range list {
			ent, _ := e.(map[string]any)
			if ent == nil {
				continue
			}
			str := func(k string) string { v, _ := ent[k].(string); return v }
			link := strings.TrimSuffix(str("file"), filepath.Ext(str("file")))
			if link == "" {
				link = str("id")
			}
			line := "- [[" + link + "]]"
			for _, part := range []struct{ prefix, value string }{
				{" — ", str("type")}, {" — ", str("title")}, {" v", str("version")},
				{" — ", str("status")}, {" → ", str("target")},
			} {
				if part.value != "" {
					line += part.prefix + part.value
				}
			}
			if u := str("updatedAt"); len(u) >= 10 {
				line += " — " + u[:10]
			}
			b.WriteString(line + "\n")
		}
	}
	b.WriteString("\n" + markerEnd)
	return b.String()
}

// writePressMd regenerates the autogen block in place. Prose outside the
// markers is never touched; if the markers were lost the block is appended
// rather than overwriting anything.
func (r Runtime) writePressMd(name string, conf map[string]any) error {
	path := filepath.Join(r.ProjectDir(name), indexName)
	block := renderIndexBlock(conf)
	existing, err := os.ReadFile(path)
	if err == nil {
		s := string(existing)
		si, ei := strings.Index(s, markerStart), strings.Index(s, markerEnd)
		if si >= 0 && ei > si {
			return os.WriteFile(path, []byte(s[:si]+block+s[ei+len(markerEnd):]), 0o644)
		}
		return os.WriteFile(path, []byte(s+"\n"+block+"\n"), 0o644)
	}
	head := fmt.Sprintf("---\nproject: %s\n---\n\n# %s — press index\n\n", name, name)
	return os.WriteFile(path, []byte(head+block+"\n"), 0o644)
}

// ---------- artifact notes ----------

// writeNoteIfMissing seeds a context note beside a PDF. A failure is returned,
// never swallowed: IndexAdd has already written config and index by this point,
// so silently skipping the note would report success over half-applied state.
func (r Runtime) writeNoteIfMissing(name string, ent map[string]any) error {
	file, _ := ent["file"].(string)
	if file == "" {
		return nil
	}
	notePath, err := resolveInside(r.ProjectDir(name), strings.TrimSuffix(file, filepath.Ext(file))+".md")
	if err != nil {
		return err
	}
	if _, err := os.Stat(notePath); err == nil {
		return nil // never overwrite an existing note
	}
	if err := os.MkdirAll(filepath.Dir(notePath), 0o755); err != nil {
		return fmt.Errorf("create note directory: %w", err)
	}
	var b strings.Builder
	b.WriteString("---\n")
	for _, k := range []string{"id", "kind", "type", "title", "version", "issuer", "target", "status", "createdAt"} {
		if v, ok := ent[k].(string); ok && v != "" {
			fmt.Fprintf(&b, "%s: %q\n", k, v)
		}
	}
	b.WriteString("supersedes: \"\"\n---\n\n")
	b.WriteString("Context notes for [[" + strings.TrimSuffix(filepath.Base(file), filepath.Ext(file)) + "]].\n")
	if err := os.WriteFile(notePath, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write note: %w", err)
	}
	return nil
}

// ---------- commands ----------

// Init creates ~/Exports/<project>/ with a config and a PRESS.md index. It is
// idempotent: an existing config or index is left exactly as it is.
func (r Runtime) Init(name string) (created bool, err error) {
	if err := checkProjectName(name); err != nil {
		return false, err
	}
	dir := r.ProjectDir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("create project home: %w", err)
	}
	if _, statErr := os.Stat(r.confPath(name)); os.IsNotExist(statErr) {
		if err := r.saveConf(name, r.defaultConf(name)); err != nil {
			return false, fmt.Errorf("write config: %w", err)
		}
		created = true
	}
	conf, err := r.loadConf(name)
	if err != nil {
		return created, err
	}
	if _, statErr := os.Stat(filepath.Join(dir, indexName)); os.IsNotExist(statErr) {
		if err := r.writePressMd(name, conf); err != nil {
			return created, fmt.Errorf("write %s: %w", indexName, err)
		}
	}
	return created, nil
}

// ConfigGet reads one dot-path out of the project config.
func (r Runtime) ConfigGet(name, path string) (any, error) {
	conf, err := r.loadConf(name)
	if err != nil {
		return nil, fmt.Errorf("no config for %q — run `press init` first: %w", name, err)
	}
	v, ok := getPath(conf, path)
	if !ok {
		return nil, fmt.Errorf("path %q not found", path)
	}
	return v, nil
}

// ConfigSet writes one dot-path. A value that parses as JSON is stored as
// JSON; anything else is stored as a plain string.
func (r Runtime) ConfigSet(name, path, value string) error {
	conf, err := r.loadConf(name)
	if err != nil {
		return fmt.Errorf("no config for %q — run `press init` first: %w", name, err)
	}
	var val any
	if err := json.Unmarshal([]byte(value), &val); err != nil {
		val = value
	}
	setPath(conf, path, val)
	if err := r.saveConf(name, conf); err != nil {
		return err
	}
	return r.writePressMd(name, conf)
}

// IndexList returns the recorded artifacts, optionally narrowed to one kind.
func (r Runtime) IndexList(name, kind string) (map[string]any, error) {
	conf, err := r.loadConf(name)
	if err != nil {
		return nil, fmt.Errorf("no config for %q — run `press init` first: %w", name, err)
	}
	if kind != "" {
		if section, _ := entriesKey(kind); section == "" {
			return nil, fmt.Errorf("unknown kind %q: want pdf, logo or design", kind)
		}
	}
	out := map[string]any{}
	for _, k := range []string{"pdf", "logo", "design"} {
		if kind != "" && kind != k {
			continue
		}
		section, key := entriesKey(k)
		v, _ := getPath(conf, section+"."+key)
		out[k] = v
	}
	return out, nil
}

// Entry describes one artifact being recorded in the index.
type Entry struct {
	Kind, Type, File, Title, Version, Issuer, Target, Status, ID string
}

// IndexAdd records or updates an artifact, regenerates PRESS.md, and for PDFs
// seeds a context note when none exists yet.
func (r Runtime) IndexAdd(name string, e Entry) (id string, created bool, err error) {
	section, key := entriesKey(e.Kind)
	if section == "" {
		return "", false, fmt.Errorf("unknown kind %q", e.Kind)
	}
	if e.File == "" && e.Title == "" {
		return "", false, errors.New("--file or --title required")
	}
	// Validate before any write: a rejected path must leave config and index
	// untouched rather than half-updated.
	if e.File != "" {
		if _, err := resolveInside(r.ProjectDir(name), e.File); err != nil {
			return "", false, fmt.Errorf("--file %q: %w", e.File, err)
		}
	}
	conf, err := r.loadConf(name)
	if err != nil {
		return "", false, fmt.Errorf("no config for %q — run `press init` first: %w", name, err)
	}
	id = e.ID
	if id == "" {
		if e.File != "" {
			id = slugify(e.File)
		} else {
			id = slugify(e.Title)
		}
	}
	raw, _ := getPath(conf, section+"."+key)
	list, _ := raw.([]any)
	var ent map[string]any
	for _, item := range list {
		if m, ok := item.(map[string]any); ok && m["id"] == id {
			ent = m
			break
		}
	}
	created = ent == nil
	if created {
		ent = map[string]any{"id": id, "createdAt": r.stamp()}
		list = append(list, ent)
	}
	ent["kind"] = e.Kind
	ent["updatedAt"] = r.stamp()
	for k, v := range map[string]string{
		"type": e.Type, "file": e.File, "title": e.Title, "version": e.Version,
		"issuer": e.Issuer, "target": e.Target, "status": e.Status,
	} {
		if v != "" {
			ent[k] = v
		}
	}
	setPath(conf, section+"."+key, list)
	if err := r.saveConf(name, conf); err != nil {
		return "", false, err
	}
	if err := r.writePressMd(name, conf); err != nil {
		return "", false, err
	}
	if e.Kind == "pdf" {
		if err := r.writeNoteIfMissing(name, ent); err != nil {
			return "", false, fmt.Errorf("seed context note: %w", err)
		}
	}
	return id, created, nil
}

var icoPattern = regexp.MustCompile(`^\d{8}$`)

// Ares resolves a Czech company by IČO through the public ARES registry, so a
// legal document's counterparty identity is never retyped by hand. Results are
// cached in the project config when one exists.
func (r Runtime) Ares(name, ico string) (map[string]any, error) {
	ico = strings.TrimSpace(ico)
	if !icoPattern.MatchString(ico) {
		return nil, fmt.Errorf("IČO must be exactly 8 digits, got %q", ico)
	}
	if name != "" {
		if conf, err := r.loadConf(name); err == nil {
			if v, ok := getPath(conf, "ares."+ico); ok {
				if cached, ok := v.(map[string]any); ok {
					return cached, nil
				}
			}
		}
	}
	client := r.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	endpoint := r.AresEndpoint
	if endpoint == "" {
		endpoint = aresEndpoint
	}
	resp, err := client.Get(endpoint + ico)
	if err != nil {
		return nil, fmt.Errorf("ARES request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("ARES: no subject with IČO %s", ico)
	default:
		return nil, fmt.Errorf("ARES returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ARES response unreadable: %w", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("ARES response unparsable: %w", err)
	}
	str := func(path string) string {
		v, _ := getPath(raw, path)
		s, _ := v.(string)
		return s
	}
	info := map[string]any{
		"ico":       ico,
		"name":      str("obchodniJmeno"),
		"dic":       str("dic"),
		"legalForm": str("pravniForma"),
		"address":   str("sidlo.textovaAdresa"),
		"fetchedAt": r.stamp(),
	}
	// The command documents that a successful lookup is cached. If the write
	// fails, say so rather than returning data that later calls will re-fetch
	// while believing they are served from cache.
	if name != "" {
		if conf, err := r.loadConf(name); err == nil {
			setPath(conf, "ares."+ico, info)
			if err := r.saveConf(name, conf); err != nil {
				return nil, fmt.Errorf("cache ARES result for %s: %w", ico, err)
			}
		}
	}
	return info, nil
}

// ---------- lint ----------

// LintReport is the outcome of validating one project's press state.
type LintReport struct {
	Fixed    []string `json:"fixed"`
	Problems []string `json:"problems"`
}

// OK reports whether the project is clean.
func (l LintReport) OK() bool { return len(l.Problems) == 0 }

// Lint validates the project config and index. With fix set it self-corrects
// everything that can be derived; anything ambiguous is reported instead.
func (r Runtime) Lint(name string, fix bool) (LintReport, error) {
	var report LintReport
	conf, err := r.loadConf(name)
	if err != nil {
		return report, fmt.Errorf("no config for %q — run `press init` first: %w", name, err)
	}
	changed := false
	problem := func(format string, a ...any) { report.Problems = append(report.Problems, fmt.Sprintf(format, a...)) }
	repaired := func(format string, a ...any) {
		report.Fixed = append(report.Fixed, fmt.Sprintf(format, a...))
		changed = true
	}

	for _, k := range []string{"project", "logo", "pdf", "design", "meta"} {
		if _, ok := conf[k]; !ok {
			if fix {
				conf[k] = r.defaultConf(name)[k]
				repaired("added missing section %s", k)
			} else {
				problem("missing section: %s", k)
			}
		}
	}
	if v, _ := getPath(conf, "project.name"); v != name {
		if fix {
			setPath(conf, "project.name", name)
			repaired("project.name corrected to %s", name)
		} else {
			problem("project.name %v != folder %q", v, name)
		}
	}
	if v, ok := getPath(conf, "meta.skillVersion"); !ok || v == "" {
		if fix {
			setPath(conf, "meta.skillVersion", Version)
			repaired("meta.skillVersion set to %s", Version)
		} else {
			problem("meta.skillVersion missing")
		}
	}
	if v, ok := getPath(conf, "pdf.documents"); ok {
		if _, isList := v.([]any); !isList {
			problem("pdf.documents is not a list (unfixable automatically)")
		}
	} else if fix {
		setPath(conf, "pdf.documents", []any{})
		repaired("pdf.documents initialised")
	}
	for _, kind := range []string{"pdf", "logo", "design"} {
		section, key := entriesKey(kind)
		raw, _ := getPath(conf, section+"."+key)
		list, _ := raw.([]any)
		for i, e := range list {
			ent, _ := e.(map[string]any)
			if ent == nil {
				continue
			}
			if ent["id"] == nil || ent["id"] == "" {
				if f, _ := ent["file"].(string); f != "" && fix {
					ent["id"] = slugify(f)
					repaired("%s[%d]: id derived from file", kind, i)
				} else {
					problem("%s[%d]: missing id", kind, i)
				}
			}
			if ent["createdAt"] == nil {
				if fix {
					ent["createdAt"] = r.stamp()
					repaired("%s[%d]: createdAt stamped", kind, i)
				} else {
					problem("%s[%d]: missing createdAt", kind, i)
				}
			}
			if f, _ := ent["file"].(string); f != "" {
				resolved, err := resolveInside(r.ProjectDir(name), f)
				if err != nil {
					problem("%s[%d]: file %q escapes the project directory", kind, i, f)
				} else if _, err := os.Stat(resolved); err != nil {
					problem("%s[%d]: file %q not found on disk", kind, i, f)
				}
			}
		}
	}
	mdPath := filepath.Join(r.ProjectDir(name), indexName)
	md, mdErr := os.ReadFile(mdPath)
	if mdErr != nil || !strings.Contains(string(md), markerStart) {
		if fix {
			if err := r.writePressMd(name, conf); err != nil {
				return report, err
			}
			report.Fixed = append(report.Fixed, indexName+" regenerated")
		} else {
			problem("%s missing or lost its autogen markers", indexName)
		}
	}
	if changed {
		if err := r.saveConf(name, conf); err != nil {
			return report, err
		}
		if err := r.writePressMd(name, conf); err != nil {
			return report, err
		}
	}
	sort.Strings(report.Problems)
	return report, nil
}

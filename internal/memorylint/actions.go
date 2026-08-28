package memorylint

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

// properties is the v2 note schema: flat, Obsidian-native. Obsidian's Properties
// view cannot edit nested keys, which is why nothing here is allowed to nest.
type properties struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	Type         string   `yaml:"type"`
	Status       string   `yaml:"status"`
	Expires      string   `yaml:"expires,omitempty"`
	Tags         []string `yaml:"tags,omitempty"`
	Aliases      []string `yaml:"aliases,omitempty"`
	LastUsed     string   `yaml:"last-used,omitempty"`
	LastVerified string   `yaml:"last-verified,omitempty"`
}

// legacyEnvelope is the pre-v2 shape, and also what Claude Code rewrites a note
// back into when its Edit/Write tools touch the agent-managed home.
type legacyEnvelope struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	Type         string   `yaml:"type"`
	Status       string   `yaml:"status"`
	Expires      string   `yaml:"expires"`
	Tags         []string `yaml:"tags"`
	Aliases      []string `yaml:"aliases"`
	LastUsed     string   `yaml:"last-used"`
	LastVerified string   `yaml:"last-verified"`
	Metadata     struct {
		Type     string   `yaml:"type"`
		NodeType string   `yaml:"node_type"`
		Status   string   `yaml:"status"`
		Tags     []string `yaml:"tags"`
		Modified string   `yaml:"modified"`
	} `yaml:"metadata"`
}

// Normalize converts one note to the v2 schema, reporting whether it changed.
func Normalize(path string) ([]byte, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	normalized := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	if !bytes.HasPrefix(normalized, []byte("---\n")) {
		return nil, false, fmt.Errorf("missing YAML frontmatter")
	}
	end := bytes.Index(normalized[4:], []byte("\n---"))
	if end < 0 {
		return nil, false, fmt.Errorf("unterminated YAML frontmatter")
	}
	front := normalized[4 : 4+end]
	// rest begins at the closing delimiter: "\n---" then an optional newline.
	rest := bytes.TrimPrefix(normalized[4+end:], []byte("\n---"))
	body := bytes.TrimPrefix(rest, []byte("\n"))

	var legacy legacyEnvelope
	if err := yaml.Unmarshal(front, &legacy); err != nil {
		return nil, false, fmt.Errorf("invalid YAML: %w", err)
	}
	// Anything outside the v2 schema is carried through untouched. `fix` is the
	// documented repair for a drifted note, and no lint rule forbids extra keys —
	// so dropping them silently would make the recommended remedy destroy data
	// the linter considers perfectly legal.
	var all map[string]any
	if err := yaml.Unmarshal(front, &all); err != nil {
		return nil, false, fmt.Errorf("invalid YAML: %w", err)
	}
	for _, k := range []string{"name", "description", "type", "status", "expires", "tags", "aliases", "last-used", "last-verified", "metadata"} {
		delete(all, k)
	}

	props := properties{
		Name:         legacy.Name,
		Description:  legacy.Description,
		Type:         firstNonEmpty(legacy.Type, legacy.Metadata.Type),
		Status:       firstNonEmpty(legacy.Status, legacy.Metadata.Status),
		Expires:      legacy.Expires,
		Tags:         firstNonEmptySlice(legacy.Tags, legacy.Metadata.Tags),
		Aliases:      legacy.Aliases,
		LastUsed:     legacy.LastUsed,
		LastVerified: legacy.LastVerified,
	}
	// The schema requires name == filename stem, and `fix` is the documented
	// remedy for a note that drifted — so the stem always wins. Only repairing an
	// EMPTY name let `fix` report success on a note `check` still rejects.
	props.Name = strings.TrimSuffix(filepath.Base(path), ".md")
	if props.Status == "" {
		props.Status = "active"
	}
	if props.LastVerified == "" && legacy.Metadata.Modified != "" {
		if t, err := time.Parse(time.RFC3339, legacy.Metadata.Modified); err == nil {
			props.LastVerified = t.Format("2006-01-02")
		} else if len(legacy.Metadata.Modified) >= 10 {
			props.LastVerified = legacy.Metadata.Modified[:10]
		}
	}

	rendered, err := renderNote(props, string(body), all)
	if err != nil {
		return nil, false, err
	}
	return rendered, !bytes.Equal(rendered, raw), nil
}

func renderNote(props properties, body string, extra map[string]any) ([]byte, error) {
	front, err := yaml.Marshal(props)
	if err != nil {
		return nil, err
	}
	// Sorted, so a note round-trips to the same bytes every time.
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		rest, err := yaml.Marshal(map[string]any{k: extra[k]})
		if err != nil {
			return nil, err
		}
		front = append(front, rest...)
	}
	return []byte("---\n" + string(front) + "---\n\n" + strings.TrimRight(strings.TrimLeft(body, "\r\n"), "\r\n") + "\n"), nil
}

// Fix normalizes every note in each home. With dryRun it reports without writing.
//
// A note it cannot parse is reported and SKIPPED, never fatal: a single
// non-note .md in a home (a README) would otherwise make every other note
// unfixable, and because the file list is sorted, whether the abort landed
// before or after some writes was decided by filename order — a home whose bad
// file sorted last was left half-normalized.
func Fix(homes []string, dryRun bool) (changed []string, failures []string, err error) {
	for _, home := range homes {
		files, err := noteFiles(home)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", home, err))
			continue
		}
		for _, path := range files {
			rendered, didChange, err := Normalize(path)
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", path, err))
				continue
			}
			if !didChange {
				continue
			}
			if dryRun {
				changed = append(changed, path)
				continue
			}
			// Only after the write lands: reporting FIXED for a note still on disk
			// in its old shape is the one lie this command must not tell.
			if err := atomicWrite(path, rendered); err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", path, err))
				continue
			}
			changed = append(changed, path)
		}
	}
	return changed, failures, nil
}

// slugPattern is the documented note-name shape. Validating it is also what
// confines the new note to the home: `--name ../victim/project-pwned` would
// otherwise join straight out of it.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// NewNote scaffolds a note that already satisfies the schema. With provisional
// it is born `status: provisional` with `expires` 60 days out, per the
// lifecycle doctrine: a single-incident lesson must earn promotion to active.
func NewNote(home, noteType, name, description string, provisional bool) (string, error) {
	if name == "" || description == "" || noteType == "" {
		return "", fmt.Errorf("new requires --type, --name and --description")
	}
	if !slugPattern.MatchString(name) {
		return "", fmt.Errorf("name %q must be a lowercase kebab-case slug", name)
	}
	// `new` must not be able to create a note `check` then complains about: the
	// filename rule is `<type>-<slug>.md`, so a bare `topic` is invalid.
	if !strings.HasPrefix(name, noteType+"-") {
		return "", fmt.Errorf("name %q must start with %q so the filename matches the <type>-<slug>.md rule", name, noteType+"-")
	}
	config, err := loadConfig(home)
	if err != nil {
		return "", err
	}
	if !contains(config.AllowedTypes, noteType) {
		return "", fmt.Errorf("type %q is not allowed", noteType)
	}
	path := filepath.Join(home, name+".md")
	title := []rune(strings.ReplaceAll(strings.ReplaceAll(name, "-", " "), "_", " "))
	title[0] = unicode.ToUpper(title[0])
	props := properties{Name: name, Description: description, Type: noteType, Status: "active"}
	if provisional {
		props.Status = "provisional"
		props.Expires = time.Now().AddDate(0, 0, 60).Format("2006-01-02")
	}
	rendered, err := renderNote(props, "# "+string(title)+"\n", nil)
	if err != nil {
		return "", err
	}
	// O_EXCL rather than stat-then-write: the gap between the two let another
	// writer — or a symlink planted in a shared home — be followed and
	// overwritten. An intervening entry now fails the command.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return "", fmt.Errorf("%s already exists", path)
		}
		return "", err
	}
	if _, err := f.Write(rendered); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return path, nil
}

// Reindex renders MEMORY.md deterministically from the notes in home.
//
// teamIndex, when set, emits the routing line that tells a reader which OTHER
// home owns project/reference memory. Dropping it silently is not an option:
// the personal home's index carries that line, and regenerating without it
// deletes the only pointer to the team home.
//
// A note whose frontmatter cannot be parsed, or whose type is unknown, is an
// ERROR rather than an omission. The index is the sole discovery mechanism —
// "open only the notes whose descriptions match the current task" — so a note
// missing from it is a note that no longer exists as far as any reader is
// concerned. Silently dropping one is worse than refusing to write the index.
func Reindex(home, teamIndex string, write bool) ([]byte, error) {
	files, err := noteFiles(home)
	if err != nil {
		return nil, err
	}
	// A malformed or unreadable .memorylint.yaml is an error, not a licence to
	// rewrite the index under the default policy: Lint fails on the same input,
	// so the two would otherwise disagree about which notes are valid. An absent
	// config already yields the defaults without an error.
	config, err := loadConfig(home)
	if err != nil {
		return nil, err
	}
	known := config.AllowedTypes
	grouped := map[string][]properties{}
	links := map[string]string{}
	var problems []string
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		// Read through the legacy envelope the same way Normalize does, so a note
		// that has not been `fix`ed yet still indexes instead of being reported as
		// typeless. Refusing on a corpus the tool can already read is not safety.
		var legacy legacyEnvelope
		normalized := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
		if !bytes.HasPrefix(normalized, []byte("---\n")) {
			problems = append(problems, fmt.Sprintf("%s: missing YAML frontmatter", path))
			continue
		}
		endFront := bytes.Index(normalized[4:], []byte("\n---"))
		if endFront < 0 {
			problems = append(problems, fmt.Sprintf("%s: unterminated YAML frontmatter", path))
			continue
		}
		if err := yaml.Unmarshal(normalized[4:4+endFront], &legacy); err != nil {
			problems = append(problems, fmt.Sprintf("%s: invalid YAML: %v", path, err))
			continue
		}
		props := properties{
			Name:        legacy.Name,
			Description: legacy.Description,
			Type:        firstNonEmpty(legacy.Type, legacy.Metadata.Type),
			Status:      firstNonEmpty(legacy.Status, legacy.Metadata.Status),
		}
		if props.Name == "" {
			props.Name = strings.TrimSuffix(filepath.Base(path), ".md")
		}
		if props.Status == "superseded" {
			continue
		}
		if !contains(known, props.Type) {
			problems = append(problems, fmt.Sprintf("%s: type %q is not one of %s — it would be dropped from the index (run `memorylint fix` if the note still carries a legacy envelope)",
				path, props.Type, strings.Join(known, ", ")))
			continue
		}
		rel, relErr := filepath.Rel(home, path)
		if relErr != nil {
			rel = filepath.Base(path)
		}
		links[props.Name] = filepath.ToSlash(rel)
		grouped[props.Type] = append(grouped[props.Type], props)
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("refusing to write an index that would omit %d note(s):\n  %s",
			len(problems), strings.Join(problems, "\n  "))
	}

	var out bytes.Buffer
	out.WriteString("# Memory Index\n\nOpen only the notes whose descriptions match the current task.\n")
	if teamIndex != "" {
		fmt.Fprintf(&out, "\nTeam memory: `%s` — open it when the task touches project code.\n", teamIndex)
	}
	for _, group := range known {
		notes := grouped[group]
		if len(notes) == 0 || group == "" {
			continue
		}
		sort.Slice(notes, func(i, j int) bool { return notes[i].Name < notes[j].Name })
		out.WriteString("\n## " + strings.ToUpper(group[:1]) + group[1:] + "\n\n")
		for _, n := range notes {
			fmt.Fprintf(&out, "- [%s](%s) — %s\n", n.Name, links[n.Name], n.Description)
		}
	}
	if write {
		index := filepath.Join(home, "MEMORY.md")
		if _, statErr := os.Stat(index); statErr == nil {
			if err := atomicWrite(index, out.Bytes()); err != nil {
				return nil, err
			}
		} else if err := os.WriteFile(index, out.Bytes(), 0o644); err != nil {
			return nil, err
		}
	}
	return out.Bytes(), nil
}

// GraphReport is the stable JSON shape of a graph run. Nodes are listed as well
// as edges: the DOT form renders unlinked notes, so a JSON consumer looking for
// orphans would otherwise get a silent zero.
type GraphReport struct {
	Nodes []string    `json:"nodes,omitempty"`
	Edges []GraphEdge `json:"edges,omitempty"`
	Pairs []GraphPair `json:"pairs,omitempty"`
}

// GraphEdge is one wikilink from a note to another note's name.
type GraphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// GraphPair is two notes similar enough to be worth reviewing as duplicates.
type GraphPair struct {
	Score float64 `json:"score"`
	A     string  `json:"a"`
	B     string  `json:"b"`
}

// GraphData returns the same information Graph renders, for --json consumers.
func GraphData(homes []string, similar bool) (GraphReport, error) {
	notes, err := loadForGraph(homes)
	if err != nil {
		return GraphReport{}, err
	}
	var report GraphReport
	if similar {
		sets := make([]map[string]bool, len(notes))
		for i, n := range notes {
			sets[i] = tokenize(n.body)
		}
		for i := 0; i < len(notes); i++ {
			for j := i + 1; j < len(notes); j++ {
				if score := jaccard(sets[i], sets[j]); score >= 0.42 {
					report.Pairs = append(report.Pairs, GraphPair{score, notes[i].path, notes[j].path})
				}
			}
		}
		return report, nil
	}
	for _, n := range notes {
		report.Nodes = append(report.Nodes, n.name)
		for _, m := range wikiLinkPattern.FindAllStringSubmatch(n.body, -1) {
			report.Edges = append(report.Edges, GraphEdge{n.name, strings.TrimSuffix(filepath.Base(strings.TrimSpace(m[1])), ".md")})
		}
	}
	return report, nil
}

// graphNote is one note as the graph commands need it.
type graphNote struct {
	name, path, body string
}

func loadForGraph(homes []string) ([]graphNote, error) {
	var notes []graphNote
	for _, home := range homes {
		files, err := noteFiles(home)
		if err != nil {
			return nil, err
		}
		for _, path := range files {
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			var props properties
			normalized := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
			body := string(normalized)
			if bytes.HasPrefix(normalized, []byte("---\n")) {
				if e := bytes.Index(normalized[4:], []byte("\n---")); e >= 0 {
					_ = yaml.Unmarshal(normalized[4:4+e], &props)
					body = string(normalized[4+e:])
				}
			}
			if props.Name == "" {
				props.Name = strings.TrimSuffix(filepath.Base(path), ".md")
			}
			notes = append(notes, graphNote{props.Name, path, props.Description + " " + body})
		}
	}
	return notes, nil
}

// Graph renders the wikilink graph as DOT, or with similar the likely-duplicate
// pairs as TSV.
func Graph(homes []string, similar bool) (string, error) {
	report, err := GraphData(homes, similar)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	if similar {
		for _, p := range report.Pairs {
			fmt.Fprintf(&out, "%.2f\t%s\t%s\n", p.Score, p.A, p.B)
		}
		return out.String(), nil
	}
	notes, err := loadForGraph(homes)
	if err != nil {
		return "", err
	}
	out.WriteString("digraph memory {\n")
	for _, n := range notes {
		fmt.Fprintf(&out, "  %q [label=%q];\n", n.name, n.name)
	}
	for _, e := range report.Edges {
		fmt.Fprintf(&out, "  %q -> %q;\n", e.From, e.To)
	}
	out.WriteString("}\n")
	return out.String(), nil
}

var wordPattern = regexp.MustCompile(`[a-z0-9]{3,}`)

func tokenize(text string) map[string]bool {
	out := map[string]bool{}
	for _, w := range wordPattern.FindAllString(strings.ToLower(text), -1) {
		out[w] = true
	}
	return out
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	shared := 0
	for w := range a {
		if b[w] {
			shared++
		}
	}
	return float64(shared) / float64(len(a)+len(b)-shared)
}

// noteFiles walks the home recursively, because Lint and the write hook both do.
// While this read the top level only, a note in a subdirectory was an error to
// `check`, invisible to `fix` ("0 note(s) changed"), and silently omitted from
// `reindex` — without the omission guard firing, which is precisely the outcome
// that guard exists to prevent.
func noteFiles(home string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(home, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != home && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".md") || entry.Name() == "MEMORY.md" {
			return nil
		}
		out = append(out, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func atomicWrite(path string, data []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".memorylint-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Chmod(name, info.Mode()); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func firstNonEmptySlice(values ...[]string) []string {
	for _, v := range values {
		if len(v) > 0 {
			return v
		}
	}
	return nil
}

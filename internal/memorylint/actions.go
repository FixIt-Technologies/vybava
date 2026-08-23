package memorylint

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// properties is the v2 note schema: flat, Obsidian-native. Obsidian's Properties
// view cannot edit nested keys, which is why nothing here is allowed to nest.
type properties struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	Type         string   `yaml:"type"`
	Status       string   `yaml:"status"`
	Tags         []string `yaml:"tags,omitempty"`
	Aliases      []string `yaml:"aliases,omitempty"`
	LastVerified string   `yaml:"last-verified,omitempty"`
}

// legacyEnvelope is the pre-v2 shape, and also what Claude Code rewrites a note
// back into when its Edit/Write tools touch the agent-managed home.
type legacyEnvelope struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	Type         string   `yaml:"type"`
	Status       string   `yaml:"status"`
	Tags         []string `yaml:"tags"`
	Aliases      []string `yaml:"aliases"`
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

	props := properties{
		Name:         legacy.Name,
		Description:  legacy.Description,
		Type:         firstNonEmpty(legacy.Type, legacy.Metadata.Type),
		Status:       firstNonEmpty(legacy.Status, legacy.Metadata.Status),
		Tags:         firstNonEmptySlice(legacy.Tags, legacy.Metadata.Tags),
		Aliases:      legacy.Aliases,
		LastVerified: legacy.LastVerified,
	}
	if props.Name == "" {
		props.Name = strings.TrimSuffix(filepath.Base(path), ".md")
	}
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

	rendered, err := renderNote(props, string(body))
	if err != nil {
		return nil, false, err
	}
	return rendered, !bytes.Equal(rendered, raw), nil
}

func renderNote(props properties, body string) ([]byte, error) {
	front, err := yaml.Marshal(props)
	if err != nil {
		return nil, err
	}
	return []byte("---\n" + string(front) + "---\n\n" + strings.TrimRight(strings.TrimLeft(body, "\r\n"), "\r\n") + "\n"), nil
}

// Fix normalizes every note in each home. With dryRun it reports without writing.
func Fix(homes []string, dryRun bool) ([]string, error) {
	var changed []string
	for _, home := range homes {
		files, err := noteFiles(home)
		if err != nil {
			return nil, err
		}
		for _, path := range files {
			rendered, didChange, err := Normalize(path)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			if !didChange {
				continue
			}
			changed = append(changed, path)
			if dryRun {
				continue
			}
			if err := atomicWrite(path, rendered); err != nil {
				return nil, err
			}
		}
	}
	return changed, nil
}

// NewNote scaffolds a note that already satisfies the schema.
func NewNote(home, noteType, name, description string) (string, error) {
	if name == "" || description == "" || noteType == "" {
		return "", fmt.Errorf("new requires --type, --name and --description")
	}
	if !contains(DefaultConfig().AllowedTypes, noteType) {
		return "", fmt.Errorf("type %q is not allowed", noteType)
	}
	path := filepath.Join(home, name+".md")
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("%s already exists", path)
	}
	title := strings.ReplaceAll(strings.ReplaceAll(name, "-", " "), "_", " ")
	rendered, err := renderNote(properties{
		Name: name, Description: description, Type: noteType, Status: "active",
	}, "# "+strings.ToUpper(title[:1])+title[1:]+"\n")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, rendered, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Reindex renders MEMORY.md deterministically from the notes in home.
func Reindex(home string, write bool) ([]byte, error) {
	files, err := noteFiles(home)
	if err != nil {
		return nil, err
	}
	grouped := map[string][]properties{}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var props properties
		normalized := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
		if bytes.HasPrefix(normalized, []byte("---\n")) {
			if end := bytes.Index(normalized[4:], []byte("\n---")); end >= 0 {
				_ = yaml.Unmarshal(normalized[4:4+end], &props)
			}
		}
		if props.Name == "" {
			props.Name = strings.TrimSuffix(filepath.Base(path), ".md")
		}
		if props.Status == "superseded" {
			continue
		}
		grouped[props.Type] = append(grouped[props.Type], props)
	}

	var out bytes.Buffer
	out.WriteString("# Memory Index\n\nOpen only the notes whose descriptions match the current task.\n")
	for _, group := range []string{"user", "feedback", "project", "reference"} {
		notes := grouped[group]
		if len(notes) == 0 {
			continue
		}
		sort.Slice(notes, func(i, j int) bool { return notes[i].Name < notes[j].Name })
		out.WriteString("\n## " + strings.ToUpper(group[:1]) + group[1:] + "\n\n")
		for _, n := range notes {
			fmt.Fprintf(&out, "- [%s](%s.md) — %s\n", n.Name, n.Name, n.Description)
		}
	}
	if write {
		if err := os.WriteFile(filepath.Join(home, "MEMORY.md"), out.Bytes(), 0o644); err != nil {
			return nil, err
		}
	}
	return out.Bytes(), nil
}

func noteFiles(home string) ([]string, error) {
	entries, err := os.ReadDir(home)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") || e.Name() == "MEMORY.md" {
			continue
		}
		out = append(out, filepath.Join(home, e.Name()))
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

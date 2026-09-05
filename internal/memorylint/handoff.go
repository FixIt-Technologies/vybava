package memorylint

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Handoff homes (`~/.claude/handoffs/`) hold /handoff runbooks: single-use
// session scaffolding that is archived, never deleted, once /continue drives it
// to done. They share the memory write hook and secret scan but carry their own
// schema — lifecycle status plus the sessions that created and worked them.
//
// Layout under the home: `<project>/<slug>.md`, `<project>/<slug>/handoff.md`
// (with sibling context files), and the same two shapes under
// `<project>/archive/`. Only those two shapes are handoffs; every other .md is a
// context file and gets the secret scan alone.

const maxHandoffLines = 200

var (
	handoffHomePattern = regexp.MustCompile(`/\.claude/handoffs(?:/|$)`)
	sessionIDPattern   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	handoffStatuses    = []string{"open", "in-progress", "done", "abandoned"}
	archivedStatuses   = []string{"done", "abandoned"}
	// featurePattern is the ledger key: a vitrinka project slug and an epic id.
	featurePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*/[0-9]+$`)
	// featureRequiredFrom: handoffs created on or after this date must name
	// their feature (or `none`); the 345 older ones stay exempt.
	featureRequiredFrom = time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
)

// legacySession is the one non-uuid created-by the schema accepts: handoffs
// written before sessions were recorded, whose creator no transcript names.
const legacySession = "unknown"

type handoffFrontmatter struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Status      string   `yaml:"status"`
	Created     string   `yaml:"created"`
	Feature     string   `yaml:"feature"`
	CreatedBy   string   `yaml:"created-by"`
	Sessions    []string `yaml:"sessions"`
}

func IsHandoffHome(path string) bool {
	return handoffHomePattern.MatchString(filepath.ToSlash(path))
}

// handoffHomeRoot returns the `handoffs` directory a path sits under.
func handoffHomeRoot(path string) string {
	for cur := filepath.Dir(path); ; cur = filepath.Dir(cur) {
		if filepath.Base(cur) == "handoffs" || filepath.Dir(cur) == cur {
			return cur
		}
	}
}

// HandoffSlug returns the slug a handoff file must be named after, and whether
// the file is a handoff (as opposed to a context file) and archived.
func HandoffSlug(relative string) (slug string, isHandoff, archived bool) {
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) < 2 {
		return "", false, false
	}
	rest := parts[1:]
	if rest[0] == "archive" {
		archived = true
		rest = rest[1:]
	}
	switch {
	case len(rest) == 1:
		return strings.TrimSuffix(rest[0], ".md"), true, archived
	case len(rest) == 2 && rest[1] == "handoff.md":
		return rest[0], true, archived
	}
	return "", false, archived
}

func validSession(id string) bool {
	return id == legacySession || sessionIDPattern.MatchString(id)
}

func handoffFindings(path, relative string, data []byte) []Finding {
	slug, isHandoff, archived := HandoffSlug(relative)
	if !isHandoff {
		return nil
	}
	var findings []Finding
	fail := func(format string, args ...any) {
		findings = append(findings, finding("H001", SeverityError, path, 1, format, args...))
	}
	var parsed handoffFrontmatter
	if err := parseInto(data, &parsed); err != nil {
		fail("%v", err)
		return findings
	}
	if parsed.Name != slug {
		fail("name %q must be the handoff slug %q", parsed.Name, slug)
	}
	if strings.TrimSpace(parsed.Description) == "" {
		fail("frontmatter is missing description")
	}
	if !contains(handoffStatuses, parsed.Status) {
		fail("status must be one of %s", strings.Join(handoffStatuses, ", "))
	} else if contains(archivedStatuses, parsed.Status) != archived {
		findings = append(findings, finding("H002", SeverityError, path, 1, "status %q belongs %s archive/", parsed.Status, map[bool]string{true: "under", false: "outside"}[!archived]))
	}
	created, err := time.Parse("2006-01-02", parsed.Created)
	if err != nil {
		fail("created must be YYYY-MM-DD")
	} else if parsed.Feature == "" && !created.Before(featureRequiredFrom) {
		fail("frontmatter is missing feature: <project>/<taskId> or none")
	}
	if parsed.Feature != "" && parsed.Feature != "none" && !featurePattern.MatchString(parsed.Feature) {
		fail("feature %q must be none or <project>/<taskId>", parsed.Feature)
	}
	if !validSession(parsed.CreatedBy) {
		fail("created-by must be a session id")
	}
	if len(parsed.Sessions) == 0 {
		fail("sessions must list every session that worked the handoff, creator first")
	} else {
		if parsed.Sessions[0] != parsed.CreatedBy {
			fail("sessions must start with created-by")
		}
		for _, id := range parsed.Sessions {
			if !validSession(id) {
				fail("sessions entry %q is not a session id", id)
			}
		}
	}
	if lines := lineCount(data); lines > maxHandoffLines {
		findings = append(findings, finding("H003", SeverityWarning, path, 1, "handoff has %d lines; maximum is %d", lines, maxHandoffLines))
	}
	return findings
}

func parseInto(data []byte, out any) error {
	normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		return errMissingFrontmatter
	}
	end := strings.Index(normalized[4:], "\n---")
	if end < 0 {
		return errUnterminatedFrontmatter
	}
	return yaml.Unmarshal([]byte(normalized[4:4+end]), out)
}

func lintHandoffRoot(root string, config Config) ([]Finding, int, error) {
	var findings []Finding
	files := 0
	err := filepath.WalkDir(root, func(path string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if item.IsDir() || !strings.EqualFold(filepath.Ext(path), ".md") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files++
		findings = append(findings, handoffFindings(path, relative, data)...)
		findings = append(findings, secretFindings(path, data, config)...)
		return nil
	})
	return findings, files, err
}

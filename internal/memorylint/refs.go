package memorylint

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// noteNamePattern matches a bare `<note>.md` mention in prose — the shape a
// consolidation leaves behind when it merges away the note a line points at. A
// separator is required (`project-`, `feedback_`, …): a bare prefix word is not
// a note name, and real sibling documents are called `reference.md`.
// A preceding `/` (a repo path) or `[` (a wikilink, checked elsewhere)
// disqualifies the match.
var noteNamePattern = regexp.MustCompile(`(?:^|[^\w./\-\[])((?:project|reference|feedback|user)[-_][A-Za-z0-9_-]+)\.md\b`)

// RefOptions configures a reference sweep of files OUTSIDE a memory home.
type RefOptions struct {
	// Prefix is the repo-relative path the home is referenced by, e.g. ".claude/memory".
	Prefix string
	// Bare also reports unqualified `<note>.md` names, not just `<Prefix>/<note>.md`.
	Bare bool
}

// DefaultRefOptions is the sweep FixIt's CI runs.
func DefaultRefOptions() RefOptions { return RefOptions{Prefix: ".claude/memory"} }

// Refs reports references in paths to notes that do not exist in home.
//
// Lint only ever walks the home itself, so a source comment or a design doc
// pointing at a merged-away note stays green forever. This is that other half.
func Refs(home string, paths []string, opts RefOptions) (Report, error) {
	if opts.Prefix == "" {
		opts.Prefix = DefaultRefOptions().Prefix
	}
	prefix := strings.Trim(filepath.ToSlash(opts.Prefix), "/")

	absolute, err := filepath.Abs(home)
	if err != nil {
		return Report{}, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return Report{}, fmt.Errorf("inspect %s: %w", absolute, err)
	}
	if !info.IsDir() {
		return Report{}, fmt.Errorf("memory root is not a directory: %s", absolute)
	}

	known := map[string]bool{}
	dir, err := os.ReadDir(absolute)
	if err != nil {
		return Report{}, err
	}
	for _, e := range dir {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			known[e.Name()] = true
		}
	}

	pathPattern := regexp.MustCompile(regexp.QuoteMeta(prefix) + `/([A-Za-z0-9][A-Za-z0-9_.-]*\.md)`)
	report := Report{Roots: []string{absolute}}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			report.Findings = append(report.Findings, finding("M020", SeverityError, path, 0, "read: %v", err))
			continue
		}
		report.Files++
		for n, line := range strings.Split(string(raw), "\n") {
			seen := map[string]bool{}
			for _, m := range pathPattern.FindAllStringSubmatch(line, -1) {
				if !known[m[1]] {
					seen[m[1]] = true
					report.Findings = append(report.Findings, finding("M020", SeverityError, path, n+1,
						"references %s/%s, which is not a note in %s", prefix, m[1], prefix))
				}
			}
			if !opts.Bare {
				continue
			}
			for _, m := range noteNamePattern.FindAllStringSubmatch(line, -1) {
				name := m[1] + ".md"
				if !known[name] && !seen[name] {
					report.Findings = append(report.Findings, finding("M021", SeverityError, path, n+1,
						"references %s, which is not a note in %s", name, prefix))
				}
			}
		}
	}
	return report, nil
}

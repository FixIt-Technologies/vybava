package memorylint

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// noteNamePatternFor matches a bare `<note>.md` mention in prose — the shape a
// consolidation leaves behind when it merges away the note a line points at. A
// separator is required (`project-`, `feedback_`, …): a bare prefix word is not
// a note name, and real sibling documents are called `reference.md`.
// A preceding `/` (a repo path) or `[` (a wikilink, checked elsewhere)
// disqualifies the match.
//
// The prefixes come from the home's configured types, like every other command:
// hardcoding the four defaults meant a reference to a custom-typed note was
// invisible here while Lint, Fix, New and Reindex all recognised it.
func noteNamePatternFor(types []string) *regexp.Regexp {
	var quoted []string
	for _, t := range types {
		if t != "" {
			quoted = append(quoted, regexp.QuoteMeta(t))
		}
	}
	if len(quoted) == 0 {
		quoted = DefaultConfig().AllowedTypes
	}
	return regexp.MustCompile(`(?:^|[^\w./\-\[])((?:` + strings.Join(quoted, "|") + `)[-_][A-Za-z0-9_-]+)\.md\b`)
}

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

	// Recursive, because everything else that walks a home is: Lint, Fix, Reindex
	// and the write hook all see `inbox/project-sub.md`. A flat read here meant a
	// perfectly valid reference to a subdirectory note was reported as pointing at
	// a note that does not exist — a false failure in the CI lane that runs this.
	files, err := noteFiles(absolute)
	if err != nil {
		return Report{}, err
	}
	known := map[string]bool{"MEMORY.md": true}
	for _, f := range files {
		known[filepath.Base(f)] = true
	}
	config, err := loadConfig(absolute)
	if err != nil {
		return Report{}, err
	}
	barePattern := noteNamePatternFor(config.AllowedTypes)

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
			for _, m := range barePattern.FindAllStringSubmatch(line, -1) {
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

package handoffs

import (
	"regexp"
	"strconv"
	"strings"
)

// Evidence is what a handoff body says about where its work lives.
type Evidence struct {
	Branches []BranchRef
	PRs      []PRRef
	Mentions []PRRef // PRs named below the first heading — context, never evidence
	Merged   bool    // the Branch line names a non-default branch and says it merged
}

// BranchRef names a branch; Repo is a repo name or project slug, "" meaning
// the handoff's own project.
type BranchRef struct{ Repo, Branch string }

// PRRef names a pull request; Repo is owner/repo, "" when only `PR #N` is known.
type PRRef struct {
	Repo   string
	Number int
}

const bt = "`"

var (
	branchLineStart = regexp.MustCompile(`^\*\*Branch(?:es)?[^:*\n]*:\*\*(.*)$`)
	// `repo@branch`
	repoAtBranch = regexp.MustCompile(bt + `([A-Za-z0-9][A-Za-z0-9._-]*)@([A-Za-z0-9][A-Za-z0-9._/-]*)` + bt)
	// repo `branch`
	repoSpaceBranch = regexp.MustCompile(`(?:^|[\s·,(])([A-Za-z0-9][A-Za-z0-9._-]*)\s+` + bt + `([A-Za-z0-9][A-Za-z0-9._/-]*)` + bt)
	// branch @ sha · `branch` — repo @ sha · work/x (PR #N)
	firstToken = regexp.MustCompile(`^\s*` + bt + `?([A-Za-z0-9][A-Za-z0-9._/-]*)` + bt + `?\s+(?:@|—|-\s|\(PR)`)
	// repo @ sha, for the "`branch` — repo @ sha · repo2 @ sha" shape
	repoAtSHA = regexp.MustCompile(`(?:^|[\s·(])([A-Za-z0-9][A-Za-z0-9._-]*)\s+@\s+` + bt + `?[0-9a-f]{7,40}`)
	// any backticked branch-looking token (has a slash, is not a path)
	backtickedBranch = regexp.MustCompile(bt + `([A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9._/-]+)` + bt)
	shaToken         = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

	prURL     = regexp.MustCompile(`github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/pull/(\d+)`)
	prRepoRef = regexp.MustCompile(`(?:^|[^/\w.-])([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)#(\d+)`)
	prWord    = regexp.MustCompile(`\bPRs?\s*#?(\d+)`)
	mergedRE  = regexp.MustCompile(`(?i)\bmerged\b`)
	notMerged = regexp.MustCompile(`(?i)\b(?:not|un)[\s-]?merged\b`)

	// words that sit where a repo name would but are prose
	repoStopwords = set("worktree", "off", "from", "on", "to", "as", "the", "in", "via", "see", "at", "of", "and", "or", "branch", "checkout", "into", "onto", "is", "was", "reuse", "with", "for", "by", "use", "under", "reviewed", "now", "currently", "deployed", "pushed", "clean", "tree")
	// words that sit where a branch name would but are prose
	branchStopwords = set("none", "n/a", "whatever", "start", "work", "tbd", "aggregate", "primary", "origin")
	// defaultBranches are baselines, never evidence: a Branch line naming only
	// one of these describes where NEW work starts.
	defaultBranches = set("main", "master", "devlp", "dev", "develop", "release")
)

// Extract reads the Branch line(s) and PR references out of a handoff. Only
// the header (everything above the first `## ` heading) is evidence; PRs named
// further down are mentions.
func Extract(body string) Evidence {
	var ev Evidence
	header, rest := splitHeader(body)
	seen := map[BranchRef]bool{}
	named := 0 // non-default branches found on the current Branch line
	addBranch := func(repo, branch string) {
		branch = strings.TrimPrefix(branch, "origin/")
		if branchStopwords[strings.ToLower(branch)] || shaToken.MatchString(branch) {
			return
		}
		if repoStopwords[strings.ToLower(repo)] {
			repo = ""
		}
		ref := BranchRef{Repo: repo, Branch: branch}
		if !seen[ref] {
			seen[ref] = true
			ev.Branches = append(ev.Branches, ref)
			if !IsDefaultBranch(branch) {
				named++
			}
		}
	}
	for _, text := range branchLines(header) {
		named = 0
		consumed := map[string]bool{}
		for _, m := range repoAtBranch.FindAllStringSubmatch(text, -1) {
			addBranch(m[1], m[2])
			consumed[m[2]] = true
		}
		for _, m := range repoSpaceBranch.FindAllStringSubmatch(text, -1) {
			addBranch(m[1], m[2])
			consumed[m[2]] = true
		}
		if m := firstToken.FindStringSubmatch(text); m != nil && !consumed[m[1]] {
			branch := m[1]
			repos := 0
			for _, r := range repoAtSHA.FindAllStringSubmatch(text, -1) {
				if r[1] != branch && !repoStopwords[strings.ToLower(r[1])] {
					addBranch(r[1], branch)
					repos++
				}
			}
			if repos == 0 {
				addBranch("", branch)
			}
			consumed[branch] = true
		}
		for _, m := range backtickedBranch.FindAllStringSubmatch(text, -1) {
			if !consumed[m[1]] {
				addBranch("", m[1])
			}
		}
		// "merged" only counts when said about a named branch — "main @ sha (old
		// work merged)" is the baseline of new work, not a verdict.
		if named > 0 && mergedRE.MatchString(notMerged.ReplaceAllString(text, "")) {
			ev.Merged = true
		}
	}
	ev.PRs = prRefs(header)
	ev.Mentions = prRefs(rest)
	return ev
}

// splitHeader cuts a handoff at its first `## ` heading.
func splitHeader(body string) (header, rest string) {
	if i := strings.Index("\n"+body, "\n## "); i >= 0 {
		return body[:i], body[i:]
	}
	return body, ""
}

// prRefs finds every PR reference in text, deduplicated, in order of first sight.
func prRefs(text string) []PRRef {
	var out []PRRef
	seen := map[PRRef]bool{}
	add := func(repo, number string) {
		n, err := strconv.Atoi(number)
		if err != nil {
			return
		}
		ref := PRRef{Repo: repo, Number: n}
		if !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	for _, m := range prURL.FindAllStringSubmatch(text, -1) {
		add(m[1], m[2])
	}
	for _, m := range prRepoRef.FindAllStringSubmatch(text, -1) {
		add(m[1], m[2])
	}
	for _, m := range prWord.FindAllStringSubmatch(text, -1) {
		add("", m[1])
	}
	return out
}

// branchLines returns the text after each `**Branch…:**` marker, joined with
// the wrapped lines that follow it (up to a blank line or the next field).
func branchLines(body string) []string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		m := branchLineStart.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		text := m[1]
		for i+1 < len(lines) {
			next := strings.TrimSpace(lines[i+1])
			if next == "" || strings.HasPrefix(next, "**") || strings.HasPrefix(next, "#") || strings.HasPrefix(next, "- ") || strings.HasPrefix(next, "|") {
				break
			}
			text += " " + next
			i++
		}
		out = append(out, text)
	}
	return out
}

// IsDefaultBranch reports a baseline branch name — never liveness evidence.
func IsDefaultBranch(branch string) bool {
	return defaultBranches[strings.ToLower(branch)]
}

func set(words ...string) map[string]bool {
	out := make(map[string]bool, len(words))
	for _, w := range words {
		out[w] = true
	}
	return out
}

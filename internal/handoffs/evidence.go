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
	Merged   bool // the Branch line says the work is merged
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
)

// Extract reads the Branch line(s) and PR references out of a handoff body.
func Extract(body string) Evidence {
	var ev Evidence
	seen := map[BranchRef]bool{}
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
		}
	}
	for _, text := range branchLines(body) {
		if mergedRE.MatchString(notMerged.ReplaceAllString(text, "")) {
			ev.Merged = true
		}
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
	}
	prSeen := map[PRRef]bool{}
	addPR := func(repo, number string) {
		n, err := strconv.Atoi(number)
		if err != nil {
			return
		}
		ref := PRRef{Repo: repo, Number: n}
		if !prSeen[ref] {
			prSeen[ref] = true
			ev.PRs = append(ev.PRs, ref)
		}
	}
	for _, m := range prURL.FindAllStringSubmatch(body, -1) {
		addPR(m[1], m[2])
	}
	for _, m := range prRepoRef.FindAllStringSubmatch(body, -1) {
		addPR(m[1], m[2])
	}
	for _, m := range prWord.FindAllStringSubmatch(body, -1) {
		addPR("", m[1])
	}
	return ev
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

func set(words ...string) map[string]bool {
	out := make(map[string]bool, len(words))
	for _, w := range words {
		out[w] = true
	}
	return out
}

package handoffs

import (
	"reflect"
	"testing"
)

// TestExtract pins the evidence regexes against the Branch/PR shapes real
// handoffs use.
func TestExtract(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, body string
		branches   []BranchRef
		prs        []PRRef
		merged     bool
	}{
		{
			name:     "branch at sha",
			body:     "**Branch:** work/single-press @ 1a2b3c4d\n",
			branches: []BranchRef{{"", "work/single-press"}},
		},
		{
			name:     "branch with worktree note",
			body:     "**Branch:** work/revolut-earnings @ abc1234 (worktree `.worktrees/revolut-earnings`, slot 13)\n",
			branches: []BranchRef{{"", "work/revolut-earnings"}},
		},
		{
			name:     "backticked branch and sha",
			body:     "**Branch:** `work/bozp-training-platform` @ `deadbeef1`\n",
			branches: []BranchRef{{"", "work/bozp-training-platform"}},
		},
		{
			name:     "main only",
			body:     "**Branch:** main @ 1234567 (in sync with origin)\n",
			branches: []BranchRef{{"", "main"}},
		},
		{
			name:     "repos with backticked branches",
			body:     "**Branches:** forge `feat/product` @ 1234abc · pwf-service `feat/forge-pricing` (uncommitted backend)\n",
			branches: []BranchRef{{"forge", "feat/product"}, {"pwf-service", "feat/forge-pricing"}},
		},
		{
			name:     "repo@branch wrapped onto the next line",
			body:     "**Branches:** `pwf-ui@forge-redesign` (`1234abc`),\n`ai-ms@forge-redesign` (`5678def`)\n\nNext.\n",
			branches: []BranchRef{{"pwf-ui", "forge-redesign"}, {"ai-ms", "forge-redesign"}},
		},
		{
			name:     "one branch across repos",
			body:     "**Branch:** `forge-redesign` — pwf-ui @ `1234abc` · ai-ms @ `5678def`\n",
			branches: []BranchRef{{"pwf-ui", "forge-redesign"}, {"ai-ms", "forge-redesign"}},
		},
		{
			name:     "worktree repos",
			body:     "**Branches:** FixIt `feat/mcp-sdk-v2` (worktree `.worktrees/mcp-sdk-v2`) · eve-ai-layer `feat/mcp-client-v2` (worktree `.worktrees/mcp-client-v2`)\n",
			branches: []BranchRef{{"FixIt", "feat/mcp-sdk-v2"}, {"eve-ai-layer", "feat/mcp-client-v2"}},
		},
		{
			name:     "merged with PR",
			body:     "**Branch:** work/golden-spec @ abc1234 — **MERGED to main as PR #609**. Working tree clean.\n",
			branches: []BranchRef{{"", "work/golden-spec"}},
			prs:      []PRRef{{"", 609}},
			merged:   true,
		},
		{
			name:     "not merged is not merged",
			body:     "**Branch:** vitrinka `feat/eve-cutover-track3` @ abc1234 · PR #209 OPEN (not merged, not deployed)\n",
			branches: []BranchRef{{"vitrinka", "feat/eve-cutover-track3"}},
			prs:      []PRRef{{"", 209}},
		},
		{
			name:     "prose branch line yields only the default branch",
			body:     "**Branch:** none yet — start fresh from `origin/main` in a new worktree\n",
			branches: []BranchRef{{"", "main"}},
		},
		{
			name:     "branch with PR in parentheses",
			body:     "**Branch:** work/acceptance-engine (PR #1127) — reviewed @ `abc1234`\n",
			branches: []BranchRef{{"", "work/acceptance-engine"}},
			prs:      []PRRef{{"", 1127}},
		},
		{
			name: "PR forms in the body",
			body: "No branch.\n\nSee https://github.com/LEFTEQ/FixIt/pull/644 and LEFTEQ/vitrinka#12, then PR #644 again.\n",
			prs:  []PRRef{{"LEFTEQ/FixIt", 644}, {"LEFTEQ/vitrinka", 12}, {"", 644}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := Extract(c.body)
			if !reflect.DeepEqual(got.Branches, c.branches) {
				t.Errorf("Branches = %v, want %v", got.Branches, c.branches)
			}
			if !reflect.DeepEqual(got.PRs, c.prs) {
				t.Errorf("PRs = %v, want %v", got.PRs, c.prs)
			}
			if got.Merged != c.merged {
				t.Errorf("Merged = %v, want %v", got.Merged, c.merged)
			}
		})
	}
}

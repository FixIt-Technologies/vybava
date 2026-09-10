package claudeguards

import "testing"

func TestEscapeHatch(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"CLAUDE_ALLOW_DANGEROUS=1 git stash", true},
		{"cd /x && CLAUDE_ALLOW_DANGEROUS=1 git stash", true},
		{"FOO=bar CLAUDE_ALLOW_DANGEROUS=1 git stash", true},
		{"(CLAUDE_ALLOW_DANGEROUS=1 git stash)", true},
		{`echo "CLAUDE_ALLOW_DANGEROUS=1" && git stash`, false},
		{`git commit -m "CLAUDE_ALLOW_DANGEROUS=1 was needed" && git stash`, false},
		{"git stash # CLAUDE_ALLOW_DANGEROUS=1", false},
		{"CLAUDE_ALLOW_DANGEROUS=10 git stash", false},
		{"XCLAUDE_ALLOW_DANGEROUS=1 git stash", false},
	}
	for _, c := range cases {
		if got := escapeHatch(c.cmd, "CLAUDE_ALLOW_DANGEROUS"); got != c.want {
			t.Errorf("%q: got %v want %v", c.cmd, got, c.want)
		}
	}
	if destructiveMatch(`echo "CLAUDE_ALLOW_DANGEROUS=1" && git stash`, mainClone) == nil {
		t.Fatal("quoted escape hatch must not disarm git-stash")
	}
}

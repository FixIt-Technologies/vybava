package claudeguards

import "regexp"

// escapeHatch reports whether VAR=1 is set as a leading environment
// assignment of some command segment — `VAR=1 git stash`, `cd x && VAR=1 cat f`
// — and not merely mentioned anywhere in the text. A quoted mention
// (`echo "VAR=1"`, a commit message, a heredoc body) must never disarm a rule.
func escapeHatch(cmd, name string) bool {
	return regexp.MustCompile(`(^|[;&|(]|\n)[[:space:]]*(?:[A-Za-z_][A-Za-z0-9_]*=[^[:space:]]*[[:space:]]+)*` + regexp.QuoteMeta(name) + `=1([[:space:]]|$)`).MatchString(cmd)
}

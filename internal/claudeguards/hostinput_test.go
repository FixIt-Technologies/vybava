package claudeguards

import "testing"

func TestHostInputPatterns(t *testing.T) {
	block := []string{
		"cliclick c:100,200",
		"sleep 1; cliclick c:1,2",
		`osascript -e 'tell application "Simulator" to activate'`,
		`osascript -e 'tell application "System Events" to click at {1, 2}'`,
		`osascript -e 'tell application "System Events" to keystroke "a"'`,
	}
	pass := []string{
		"xcrun simctl list devices booted",
		"bunx tsx appium/adhoc/__dump-tree.ts",
		"echo cliclick-is-banned",
		`osascript -e 'display notification "done"'`,
		"grep -rn cliclick scripts/",
		"git commit -m \"guards: block host input (cliclick)\"",
	}
	for _, c := range block {
		if !hostInputMatch(c) {
			t.Errorf("should block %q", c)
		}
	}
	for _, c := range pass {
		if hostInputMatch(c) {
			t.Errorf("should pass %q", c)
		}
	}
}

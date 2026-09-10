package claudeguards

import "regexp"

// ---------------------------------------------------------------------------
// guardHostInput — host-level mouse/keyboard automation is banned (ruled
// 2026-09-09, re-affirmed 2026-09-10): cliclick, AppleScript `System Events`
// clicks/keystrokes and `tell application "Simulator" to activate` steal the
// cursor and focus of the Mac Lukáš is working on, and they do not even
// register inside the Simulator window. iOS simulator UI is driven through
// the repo's Appium/XCUITest support instead (connectViaXcuitest, the
// appium/adhoc drivers, appium/support/ios-alerts.ts).
// ---------------------------------------------------------------------------

var reHostInput = regexp.MustCompile(
	`(^|[;&|][[:space:]]*)cliclick([[:space:]]|$)` +
		`|osascript[^;&|]*(System Events|tell application "Simulator" to activate|keystroke|click at)`)

const hostInputMsg = `Host-level input automation (cliclick, AppleScript System Events clicks/keystrokes,
activating the Simulator window) is banned — it hijacks the cursor and focus of the
Mac the user is working on, and it does not register inside the Simulator anyway.

Drive the iOS simulator through Appium/XCUITest instead:
  • open/attach the Expo dev client:  connectViaXcuitest / waitForPersonaReady
                                       (scripts/worktree/connect-dev-client.ts)
  • SpringBoard "Otevřít v aplikaci":   appium/support/ios-alerts.ts
  • ad-hoc taps/walkthroughs/dumps:     appium/adhoc/ (INDEX.md, lib/driver.ts)`

func hostInputMatch(cmd string) bool { return reHostInput.MatchString(cmd) }

func guardHostInput(in *HookInput) *Denial {
	if hostInputMatch(in.ToolInput.Command) {
		return deny("simulator:host-input", hostInputMsg, destructiveEscape)
	}
	return nil
}

package pwmcp

import "strings"

// SharedProfileFlag opts a caller out of the injected isolation. It is consumed
// by pwmcp and never reaches the server.
const SharedProfileFlag = "--shared-profile"

// profileFlags are the passthrough flags that already decide how the profile
// works. Injecting --isolated alongside one of them would override a deliberate
// choice: Chromium only loads an unpacked extension into a persistent profile,
// so a config that wants one is not confused, it is doing the one thing
// isolation forbids.
var profileFlags = []string{"--isolated", "--user-data-dir", "--config", "--storage-state"}

// Isolate returns the argument list to hand the server, and whether isolation
// was injected.
//
// Isolated is the default because the alternative is a single on-disk profile
// shared by every MCP server on the machine: concurrent sessions then fight over
// cookies and storage, and a crashed run leaves the next one a dirty profile.
// Isolation is per-server-process, which is the level that actually matters —
// the browser binary stays shared, and should, since duplicating it is what
// makes downloads multiply.
func Isolate(args []string) ([]string, bool) {
	kept := make([]string, 0, len(args)+1)
	shared := false
	decided := false
	for _, arg := range args {
		if arg == SharedProfileFlag {
			shared = true
			continue
		}
		if isProfileFlag(arg) {
			decided = true
		}
		kept = append(kept, arg)
	}
	if shared || decided {
		return kept, false
	}
	return append([]string{"--isolated"}, kept...), true
}

func isProfileFlag(arg string) bool {
	for _, flag := range profileFlags {
		if arg == flag || strings.HasPrefix(arg, flag+"=") {
			return true
		}
	}
	return false
}

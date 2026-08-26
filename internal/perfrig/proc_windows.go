//go:build windows

package perfrig

import "os/exec"

// setProcGroup: no process groups on Windows — fall back to exec.CommandContext's
// default kill of the direct child. Drills run from mac/linux; this exists only
// so the multi-platform release build compiles.
func setProcGroup(_ *exec.Cmd) {}

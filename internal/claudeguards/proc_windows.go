//go:build windows

package claudeguards

import "syscall"

// The hooks are macOS/Linux tools (tmux swarms, Claude Code on a Unix shell);
// Windows builds exist only so the multicall binary still ships there.
func pidAlive(int) bool                  { return false }
func terminate(int)                      {}
func kill(int)                           {}
func detachedAttr() *syscall.SysProcAttr { return nil }

//go:build unix

package claudeguards

import "syscall"

// pidAlive reports whether a process exists. kill(pid, 0) succeeds for live
// processes we own and fails with EPERM for live ones we don't; only ESRCH
// means gone.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func terminate(pid int) { _ = syscall.Kill(pid, syscall.SIGTERM) }
func kill(pid int)      { _ = syscall.Kill(pid, syscall.SIGKILL) }

// detachedAttr puts a child in its own session so the hook can return while
// the child keeps running.
func detachedAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

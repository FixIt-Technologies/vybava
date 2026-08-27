//go:build !windows

package pwmcp

import (
	"errors"
	"os"
	"syscall"
)

// processAlive reports whether a pid is still running. Signal 0 performs the
// permission and existence checks without delivering anything; EPERM means the
// process exists but belongs to someone else, which still counts as alive.
func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EPERM)
}

//go:build windows

package pwmcp

import "os"

// processAlive reports whether a pid is still running. On Windows FindProcess
// opens a real handle, so its error already answers the question.
func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = process.Release()
	return true
}

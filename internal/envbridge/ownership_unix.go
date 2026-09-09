//go:build !windows

package envbridge

import (
	"os"
	"syscall"
)

const platformSupported = true

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

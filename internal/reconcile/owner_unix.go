//go:build !windows

package reconcile

import (
	"os"
	"os/user"
	"strconv"
	"syscall"
)

func ownerOf(p string) string {
	fi, err := os.Stat(p)
	if err != nil {
		return ""
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	uid := strconv.FormatUint(uint64(st.Uid), 10)
	if u, err := user.LookupId(uid); err == nil {
		return u.Username
	}
	return "uid " + uid
}

func currentUser() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

//go:build !windows

package reclaim

import "syscall"

// Free reports the bytes available to the user and the volume size.
func Free(volume string) (free, total int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(volume, &st); err != nil {
		return 0, 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), int64(st.Blocks) * int64(st.Bsize), nil
}

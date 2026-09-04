//go:build windows

package reclaim

import (
	"syscall"
	"unsafe"
)

// Free reports the bytes available to the user and the volume size via
// GetDiskFreeSpaceExW; the ladder itself is Unix-shaped, but the binary
// must still build for every release target.
func Free(volume string) (free, total int64, err error) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetDiskFreeSpaceExW")
	path, err := syscall.UTF16PtrFromString(volume)
	if err != nil {
		return 0, 0, err
	}
	var avail, size, totalFree uint64
	r, _, callErr := proc.Call(uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&avail)), uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&totalFree)))
	if r == 0 {
		return 0, 0, callErr
	}
	return int64(avail), int64(size), nil
}

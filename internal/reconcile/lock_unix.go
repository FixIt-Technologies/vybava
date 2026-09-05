//go:build !windows

package reconcile

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// Lock serializes run/force/rollback behind one flock-style lock — the same
// file the bash cron entry wraps with `flock -n`, so both engines exclude each
// other during a parity window.
type Lock struct{ f *os.File }

// AcquireLock waits up to timeout for an exclusive lock on path.
func AcquireLock(path string, timeout time.Duration) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock %s: %w", path, err)
	}
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &Lock{f: f}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("timed out waiting for the reconcile lock (%s)", path)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}

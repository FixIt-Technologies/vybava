//go:build windows

package reconcile

import (
	"errors"
	"time"
)

// Lock is unsupported on Windows: the engine targets Linux boxes and the
// release matrix merely has to compile.
type Lock struct{}

func AcquireLock(string, time.Duration) (*Lock, error) {
	return nil, errors.New("reconcile: file locking is not supported on windows")
}

func (l *Lock) Release() {}

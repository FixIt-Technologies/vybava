//go:build windows

package operator

import (
	"context"
	"errors"
)

func lock(string) (func(), error) {
	return nil, errors.New("the local operator pilot currently supports macOS and Linux")
}

func lockContext(context.Context, string) (func(), error) { return lock("") }

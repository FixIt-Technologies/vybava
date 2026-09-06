//go:build windows

package operator

import "errors"

func lock(string) (func(), error) {
	return nil, errors.New("the local operator pilot currently supports macOS and Linux")
}

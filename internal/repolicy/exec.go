package repolicy

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ExecRunner runs real processes.
type ExecRunner struct{}

func (ExecRunner) Run(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = fmt.Sprintf("exited %d", exitErr.ExitCode())
		}
		return "", errors.New(message)
	}
	if errors.Is(err, exec.ErrNotFound) {
		return "", fmt.Errorf("%s is not installed — repolicy drives GitHub through it", name)
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

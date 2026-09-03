package hotfix

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Runner executes a process; tests substitute a scripted fake.
type Runner interface {
	// Run executes name with args in dir and returns trimmed stdout. A
	// non-zero exit returns *ExitErr carrying the code and stderr.
	Run(dir, name string, args ...string) (string, error)
	// Stream executes name with args in dir, forwarding output to the
	// process stderr (never stdout — the envelope owns stdout).
	Stream(dir, name string, args ...string) error
	Sleep(d time.Duration)
	Now() time.Time
}

// ExitErr is a non-zero exit from a wrapped command.
type ExitErr struct {
	Cmd    string
	Code   int
	Stderr string
}

func (e *ExitErr) Error() string {
	msg := fmt.Sprintf("%s exited %d", e.Cmd, e.Code)
	if s := strings.TrimSpace(e.Stderr); s != "" {
		msg += ": " + s
	}
	return msg
}

// ExecRunner runs real processes.
type ExecRunner struct{}

func (ExecRunner) Run(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return strings.TrimSpace(stdout.String()), &ExitErr{Cmd: name + " " + strings.Join(args, " "), Code: ee.ExitCode(), Stderr: stderr.String()}
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (ExecRunner) Stream(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &ExitErr{Cmd: name + " " + strings.Join(args, " "), Code: ee.ExitCode()}
	}
	return err
}

func (ExecRunner) Sleep(d time.Duration) { time.Sleep(d) }
func (ExecRunner) Now() time.Time        { return time.Now() }

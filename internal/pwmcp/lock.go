package pwmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// LockTimeout bounds how long a caller waits for the install lock. Playwright's
// own __dirlock has no timeout and prints nothing, which is how a crashed
// install silently wedges every later session; pwmcp always ends with an answer.
const LockTimeout = 25 * time.Minute

// staleAfter is when a held lock is assumed abandoned even though its owner
// still looks alive — a process wedged on a dead socket never releases.
const staleAfter = 30 * time.Minute

// holder records who is installing, so a waiting session can name the process
// blocking it instead of guessing.
type holder struct {
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
	Command string    `json:"command"`
}

// lock serializes installs across every session on the workstation. It returns a
// release function that is safe to call once.
func (c Config) lock(ctx context.Context, progress io.Writer) (func(), error) {
	if err := os.MkdirAll(c.Root, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", c.Root, err)
	}
	path := filepath.Join(c.Root, "install.lock")
	deadline := time.Now().Add(LockTimeout)
	announced := false
	for {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			record, _ := json.Marshal(holder{PID: os.Getpid(), Started: time.Now().UTC(), Command: commandName()})
			_, _ = file.Write(record)
			_ = file.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("create lock %s: %w", path, err)
		}
		current, age := readHolder(path)
		if stale(current, age) {
			fmt.Fprintf(progress, "pwmcp: clearing stale install lock from pid %d (%s old)\n", current.PID, age.Round(time.Second))
			// A failed remove means another waiter cleared it first, which is
			// the outcome we wanted anyway.
			_ = os.Remove(path)
			continue
		}
		if !announced {
			fmt.Fprintf(progress, "pwmcp: waiting for the install held by pid %d (%s, started %s ago)\n",
				current.PID, current.Command, age.Round(time.Second))
			announced = true
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out after %s waiting for %s, held by pid %d (%s); if that process is gone, delete the file",
				LockTimeout, path, current.PID, current.Command)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func readHolder(path string) (holder, time.Duration) {
	var current holder
	raw, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(raw, &current)
	}
	if current.Started.IsZero() {
		// An unreadable or truncated lock still has a mtime to age it by.
		if info, statErr := os.Stat(path); statErr == nil {
			current.Started = info.ModTime()
		} else {
			return current, 0
		}
	}
	return current, time.Since(current.Started)
}

func stale(current holder, age time.Duration) bool {
	if current.PID <= 0 {
		return true
	}
	if !processAlive(current.PID) {
		return true
	}
	return age > staleAfter
}

func commandName() string {
	if len(os.Args) == 0 {
		return "pwmcp"
	}
	return filepath.Base(os.Args[0])
}

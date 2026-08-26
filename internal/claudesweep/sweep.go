package claudesweep

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// CommandRunner executes a command and returns its stdout. Injected so the
// decision logic stays testable without spawning processes.
type CommandRunner func(name string, args ...string) (string, error)

// ExecRunner is the production CommandRunner.
func ExecRunner(name string, args ...string) (string, error) {
	command := exec.Command(name, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return stdout.String(), fmt.Errorf("%s: %w: %s", name, err, detail)
		}
		return stdout.String(), fmt.Errorf("%s: %w", name, err)
	}
	return stdout.String(), nil
}

// DefaultTmuxDir is the per-user tmux socket directory tmux itself would use.
func DefaultTmuxDir() string {
	if parent := os.Getenv("TMUX_TMPDIR"); parent != "" {
		return filepath.Join(parent, fmt.Sprintf("tmux-%d", os.Getuid()))
	}
	return fmt.Sprintf("/tmp/tmux-%d", os.Getuid())
}

// Sweeper audits claude-swarm tmux servers and optionally reaps them.
type Sweeper struct {
	Run     CommandRunner
	TmuxDir string
	Age     time.Duration
	Now     func() time.Time
	Sleep   func(time.Duration)
}

func (s Sweeper) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s Sweeper) sleep(duration time.Duration) {
	if s.Sleep != nil {
		s.Sleep(duration)
		return
	}
	time.Sleep(duration)
}

// Sweep audits every claude-swarm socket; with kill it also reaps the
// reapable ones. Per-socket failures are recorded and never abort the sweep.
func (s Sweeper) Sweep(kill, orphans bool) (Report, error) {
	if s.Run == nil {
		s.Run = ExecRunner
	}
	if s.TmuxDir == "" {
		s.TmuxDir = DefaultTmuxDir()
	}
	report := Report{TmuxDir: s.TmuxDir, AgeSeconds: int64(s.Age.Seconds()), Kill: kill, Swarms: []Swarm{}}

	entries, err := os.ReadDir(s.TmuxDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return report, fmt.Errorf("read tmux directory %s: %w", s.TmuxDir, err)
	}
	for _, entry := range entries {
		if !IsSwarmSocket(entry.Name()) {
			continue
		}
		swarm := s.auditSocket(entry.Name())
		if kill {
			s.kill(&swarm)
		}
		report.Swarms = append(report.Swarms, swarm)
	}
	if orphans {
		report.Orphans = []Orphan{}
		output, psErr := s.Run("ps", "-axo", "pid=,ppid=,etime=,command=")
		if psErr != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("list orphans: %v", psErr))
		} else if parsed := ParseOrphans(output); parsed != nil {
			report.Orphans = parsed
		}
	}
	return report, nil
}

func (s Sweeper) auditSocket(name string) Swarm {
	path := filepath.Join(s.TmuxDir, name)
	swarm := Swarm{Socket: name, Path: path}
	now := s.now()

	if _, err := s.Run("tmux", "-S", path, "list-sessions"); err != nil {
		swarm.Verdict = VerdictDeadSocket
		if info, statErr := os.Lstat(path); statErr == nil {
			swarm.AgeSeconds = int64(now.Sub(info.ModTime()).Seconds())
		}
		return swarm
	}

	output, err := s.Run("tmux", "-S", path, "list-panes", "-a", "-F", PaneFormat)
	if err != nil {
		swarm.Verdict = VerdictActive
		swarm.Errors = append(swarm.Errors, fmt.Sprintf("list panes: %v", err))
		return swarm
	}

	home, _ := os.UserHomeDir()
	var oldest time.Time
	for _, line := range strings.Split(strings.TrimRight(output, "\n"), "\n") {
		if line == "" {
			continue
		}
		info, parseErr := ParsePaneLine(line)
		if parseErr != nil {
			// A pane we cannot judge keeps the whole swarm unreapable.
			swarm.Errors = append(swarm.Errors, parseErr.Error())
			swarm.Panes = append(swarm.Panes, Pane{Idle: false, Reason: "unparseable pane line"})
			continue
		}
		if oldest.IsZero() || info.SessionCreated.Before(oldest) {
			oldest = info.SessionCreated
		}
		if swarm.Project == "" {
			swarm.Project = ProjectName(info, home)
		}

		pane := Pane{ID: info.ID, PID: info.PID, Command: info.Command}
		evidence := PaneEvidence{SessionCreated: info.SessionCreated}
		if transcript, mtime, found := s.paneTranscript(info.PID); found {
			pane.Transcript = transcript
			evidence.TranscriptMtime = &mtime
		} else if capture, captureErr := s.Run("tmux", "-S", path, "capture-pane", "-p", "-t", info.ID); captureErr != nil {
			swarm.Errors = append(swarm.Errors, fmt.Sprintf("capture pane %s: %v", info.ID, captureErr))
		} else {
			evidence.CaptureTail = capture
		}
		pane.Idle, pane.Reason = PaneIdle(evidence, now, s.Age)
		swarm.Panes = append(swarm.Panes, pane)
	}

	swarm.Verdict = LiveVerdict(swarm.Panes)
	if !oldest.IsZero() {
		swarm.AgeSeconds = int64(now.Sub(oldest).Seconds())
	}
	return swarm
}

// paneTranscript finds the newest open Claude Code transcript across the
// pane's process and up to two generations of children (the pane pid is often
// a shell wrapping the actual claude process).
func (s Sweeper) paneTranscript(panePID int) (string, time.Time, bool) {
	var best string
	var bestMtime time.Time
	for _, pid := range s.processFamily(panePID) {
		output, err := s.Run("lsof", "-p", strconv.Itoa(pid), "-Fn")
		if err != nil {
			continue
		}
		for _, candidate := range ParseLsofTranscripts(output) {
			info, statErr := os.Stat(candidate)
			if statErr != nil {
				continue
			}
			if info.ModTime().After(bestMtime) {
				best, bestMtime = candidate, info.ModTime()
			}
		}
	}
	return best, bestMtime, best != ""
}

const maxFamilySize = 12

func (s Sweeper) processFamily(panePID int) []int {
	family := []int{panePID}
	for cursor := 0; cursor < len(family) && len(family) < maxFamilySize; cursor++ {
		output, err := s.Run("pgrep", "-P", strconv.Itoa(family[cursor]))
		if err != nil {
			continue // pgrep exits 1 when a process has no children
		}
		for _, field := range strings.Fields(output) {
			if child, convErr := strconv.Atoi(field); convErr == nil {
				family = append(family, child)
			}
		}
	}
	return family
}

// killGracePeriod is how long a killed server gets before survivors are
// signalled directly.
const killGracePeriod = 2 * time.Second

func (s Sweeper) kill(swarm *Swarm) {
	switch swarm.Verdict {
	case VerdictDeadSocket:
		if time.Duration(swarm.AgeSeconds)*time.Second < s.Age {
			swarm.Actions = append(swarm.Actions, "kept: socket younger than threshold")
			return
		}
		info, err := os.Lstat(swarm.Path)
		if err != nil {
			swarm.Errors = append(swarm.Errors, fmt.Sprintf("stat socket: %v", err))
			return
		}
		if info.Mode()&os.ModeSocket == 0 {
			swarm.Errors = append(swarm.Errors, "refusing to remove non-socket file")
			return
		}
		if err := os.Remove(swarm.Path); err != nil {
			swarm.Errors = append(swarm.Errors, fmt.Sprintf("remove socket: %v", err))
			return
		}
		swarm.Actions = append(swarm.Actions, "removed dead socket")
	case VerdictIdleReapable:
		if _, err := s.Run("tmux", "-S", swarm.Path, "kill-server"); err != nil {
			swarm.Errors = append(swarm.Errors, fmt.Sprintf("kill-server: %v", err))
			return
		}
		swarm.Actions = append(swarm.Actions, "killed tmux server")
		s.sleep(killGracePeriod)
		for _, pane := range swarm.Panes {
			if pane.PID <= 1 {
				continue
			}
			process, err := os.FindProcess(pane.PID)
			if err != nil {
				continue
			}
			if process.Signal(syscall.Signal(0)) != nil {
				continue // already gone
			}
			if err := process.Signal(syscall.SIGTERM); err != nil {
				swarm.Errors = append(swarm.Errors, fmt.Sprintf("SIGTERM %d: %v", pane.PID, err))
				continue
			}
			swarm.Actions = append(swarm.Actions, fmt.Sprintf("SIGTERM surviving pid %d", pane.PID))
		}
	}
}

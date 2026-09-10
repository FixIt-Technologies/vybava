package claudeguards

// Swarm teardown — SessionStart/SessionEnd hook.
//
// A swarm leader spawns its teammates inside `tmux -L claude-swarm-<leaderpid>`.
// The doctrine says a swarm ends when its goal ends, but in practice leaders
// park finished teammates: on 2026-09-06 sixteen detached swarm servers held
// 58 Claude sessions and ~600 MCP processes, and swap was full. A rule that
// depends on remembering keeps breaking, so this is enforcement: at session
// end the leader's own swarm dies with it, and every start/end also sweeps
// swarm sockets whose leader pid is gone (crash, kill -9, reboot leftovers).
//
// Kill order matters: tmux kill-server only HUPs the pane shells, and a claude
// teammate can outlive that and get reparented to launchd. So each pane's
// process tree is TERMed first, then the server is killed, then the socket
// file is removed so the next sweep is cheap.

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const swarmSocketPrefix = "claude-swarm-"

// swarmSocketDir is where tmux keeps per-user sockets on macOS.
func swarmSocketDir() string {
	return fmt.Sprintf("/private/tmp/tmux-%d", os.Getuid())
}

// leaderPID parses the leader pid out of a socket name; ok=false for anything
// that is not a swarm socket.
func leaderPID(socketName string) (int, bool) {
	if !strings.HasPrefix(socketName, swarmSocketPrefix) {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(socketName, swarmSocketPrefix))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// selectSwarms picks the socket names to tear down: every socket whose leader
// is dead, plus every socket whose leader is one of our own ancestors (the
// session that is ending right now). alive and ancestors are injected so the
// selection is unit-testable without processes.
func selectSwarms(socketNames []string, alive func(int) bool, ancestors map[int]bool) []string {
	var out []string
	for _, name := range socketNames {
		pid, ok := leaderPID(name)
		if !ok {
			continue
		}
		if ancestors[pid] || !alive(pid) {
			out = append(out, name)
		}
	}
	return out
}

// pidAlive reports whether a process exists. kill(pid, 0) succeeds for live
// processes we own and fails with EPERM for live ones we don't; only ESRCH
// means gone.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// ancestorPIDs walks up from our parent a bounded number of hops. The hook is
// started through a shell, so the leader is the grandparent or further up;
// eight hops is far more than any wrapper chain needs.
func ancestorPIDs() map[int]bool {
	out := map[int]bool{}
	pid := os.Getppid()
	for hops := 0; hops < 8 && pid > 1; hops++ {
		out[pid] = true
		raw, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			break
		}
		next, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			break
		}
		pid = next
	}
	return out
}

// descendants returns the process tree under pid (depth-first), via pgrep -P.
func descendants(pid int) []int {
	var out []int
	stack := []int{pid}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		raw, err := exec.Command("pgrep", "-P", strconv.Itoa(cur)).Output()
		if err != nil {
			continue // no children
		}
		for _, f := range strings.Fields(string(raw)) {
			if n, err := strconv.Atoi(f); err == nil {
				out = append(out, n)
				stack = append(stack, n)
			}
		}
	}
	return out
}

// teardownSwarm kills one swarm: pane trees first, then the server, then the
// socket file. Best effort throughout; a missing server is not an error.
func teardownSwarm(socketPath string) {
	raw, err := exec.Command("tmux", "-S", socketPath, "list-panes", "-a", "-F", "#{pane_pid}").Output()
	if err == nil {
		var victims []int
		for _, f := range strings.Fields(string(raw)) {
			if n, err := strconv.Atoi(f); err == nil {
				victims = append(victims, n)
				victims = append(victims, descendants(n)...)
			}
		}
		for _, v := range victims {
			_ = syscall.Kill(v, syscall.SIGTERM)
		}
		if len(victims) > 0 {
			time.Sleep(2 * time.Second)
			for _, v := range victims {
				if pidAlive(v) {
					_ = syscall.Kill(v, syscall.SIGKILL)
				}
			}
		}
	}
	_ = exec.Command("tmux", "-S", socketPath, "kill-server").Run()
	_ = os.Remove(socketPath)
}

// swarmTeardown is the hook entry point. It never blocks the session: any
// failure is logged to stderr and the exit code stays 0. With deadOnly the
// caller's own swarm is spared — that is the SessionStart wiring and the only
// safe way to run it by hand from inside a live session, because the leader's
// in-process subagents also live in its swarm tmux server.
func SwarmTeardown(deadOnly bool, stderr io.Writer) {
	dir := swarmSocketDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // no tmux sockets at all
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	ancestors := map[int]bool{}
	if !deadOnly {
		ancestors = ancestorPIDs()
	}
	targets := selectSwarms(names, pidAlive, ancestors)
	for _, name := range targets {
		teardownSwarm(filepath.Join(dir, name))
		fmt.Fprintf(stderr, "claude-guards: tore down swarm %s\n", name)
	}
}

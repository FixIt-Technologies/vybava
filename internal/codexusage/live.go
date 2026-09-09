package codexusage

import (
	"bufio"
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// liveTimeout bounds process inspection. lsof can block indefinitely on a
// wedged mount, and a stalled report is worse than one without PIDs.
const liveTimeout = 10 * time.Second

// Process is a running Codex CLI and the rollout it is writing.
type Process struct {
	PID   int
	TTY   string
	Start time.Time
}

// scanLive maps rollout files to the processes holding them open, which is how
// a thread on disk becomes a terminal tab the user can actually go close.
// Every failure degrades to a warning: the usage numbers stand on the rollout
// files alone, and live attribution is an enrichment on top.
func scanLive(ctx context.Context, env Env) (map[string]Process, []string) {
	files := map[string]Process{}
	if env.Exec == nil {
		return files, nil
	}
	ctx, cancel := context.WithTimeout(ctx, liveTimeout)
	defer cancel()

	processes, err := codexProcesses(ctx, env)
	if err != nil {
		return files, []string{fmt.Sprintf("live sessions unavailable: %v", err)}
	}
	if len(processes) == 0 {
		return files, nil
	}

	pids := make([]string, 0, len(processes))
	for pid := range processes {
		pids = append(pids, strconv.Itoa(pid))
	}
	// lsof exits non-zero when any listed pid is gone, which races normally
	// against a session the user just quit — the output before that is still
	// good, so the error is only reported when nothing came back.
	out, err := env.Exec(ctx, "lsof", "-p", strings.Join(pids, ","), "-F", "pfn")
	if len(out) == 0 {
		if err != nil {
			return files, []string{fmt.Sprintf("open rollouts unavailable: %v", err)}
		}
		return files, nil
	}

	current := 0
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			current, _ = strconv.Atoi(line[1:])
		case 'n':
			name := line[1:]
			base := filepath.Base(name)
			if !strings.HasPrefix(base, "rollout-") || !strings.HasSuffix(base, ".jsonl") {
				continue
			}
			if process, ok := processes[current]; ok {
				files[name] = process
			}
		}
	}
	return files, nil
}

// codexProcesses lists running Codex CLIs. The ChatGPT desktop app ships
// helper processes whose paths also contain "Codex", so the match is on the
// executable's own name rather than anywhere in the command line.
func codexProcesses(ctx context.Context, env Env) (map[int]Process, error) {
	out, err := env.Exec(ctx, "ps", "-axo", "pid=,tty=,lstart=,args=")
	if err != nil && len(out) == 0 {
		return nil, err
	}
	processes := map[int]Process{}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 8 {
			continue
		}
		if filepath.Base(fields[7]) != "codex" {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		process := Process{PID: pid, TTY: fields[1]}
		if start, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(fields[2:7], " "), env.Now.Location()); err == nil {
			process.Start = start
		}
		processes[pid] = process
	}
	return processes, nil
}

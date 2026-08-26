// Package claudesweep audits and reaps stale Claude Code swarm tmux servers.
//
// Swarms are tmux servers with sockets named claude-swarm-<pid> under the
// per-user tmux temp directory. Finished teammates never self-terminate, so
// multi-day-old swarms accumulate and re-bill their full context whenever
// something wakes them. This package contains the pure decision logic; the
// process-facing side lives in sweep.go and launchd.go.
package claudesweep

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Verdict classifies one swarm socket.
type Verdict string

const (
	// VerdictActive means at least one pane shows recent activity.
	VerdictActive Verdict = "ACTIVE"
	// VerdictIdleReapable means every pane has been idle past the threshold.
	VerdictIdleReapable Verdict = "IDLE-REAPABLE"
	// VerdictDeadSocket means no tmux server answers on the socket.
	VerdictDeadSocket Verdict = "DEAD-SOCKET"
)

const swarmSocketPrefix = "claude-swarm-"

// IsSwarmSocket reports whether a socket file name belongs to a Claude swarm.
// Anything else — including the tmux "default" socket — is never touched.
func IsSwarmSocket(name string) bool {
	if !strings.HasPrefix(name, swarmSocketPrefix) {
		return false
	}
	if len(name) == len(swarmSocketPrefix) {
		return false
	}
	return !strings.ContainsAny(name, "/\\")
}

var dayPattern = regexp.MustCompile(`(\d+(?:\.\d+)?)d`)

// ParseAge parses an idleness threshold. It accepts everything
// time.ParseDuration does plus a day unit, e.g. "24h", "3d", "1d12h".
func ParseAge(input string) (time.Duration, error) {
	expanded := dayPattern.ReplaceAllStringFunc(input, func(match string) string {
		value, err := strconv.ParseFloat(strings.TrimSuffix(match, "d"), 64)
		if err != nil {
			return match
		}
		return strconv.FormatFloat(value*24, 'f', -1, 64) + "h"
	})
	duration, err := time.ParseDuration(expanded)
	if err != nil {
		return 0, fmt.Errorf("invalid age %q: %w", input, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("age must be positive, got %q", input)
	}
	return duration, nil
}

// Pane is the audited state of one tmux pane inside a swarm.
type Pane struct {
	ID         string `json:"id"`
	PID        int    `json:"pid"`
	Command    string `json:"command"`
	Transcript string `json:"transcript,omitempty"`
	Idle       bool   `json:"idle"`
	Reason     string `json:"reason"`
}

// Swarm is the audit result for one claude-swarm socket.
type Swarm struct {
	Socket     string   `json:"socket"`
	Path       string   `json:"path"`
	Verdict    Verdict  `json:"verdict"`
	AgeSeconds int64    `json:"age_seconds"`
	Project    string   `json:"project,omitempty"`
	Panes      []Pane   `json:"panes,omitempty"`
	Actions    []string `json:"actions,omitempty"`
	Errors     []string `json:"errors,omitempty"`
}

// Orphan is a claude --agent-id process re-parented to launchd (ppid 1).
type Orphan struct {
	PID     int    `json:"pid"`
	Elapsed string `json:"elapsed"`
	Command string `json:"command"`
}

// Report is the stable output of one sweep.
type Report struct {
	TmuxDir    string   `json:"tmux_dir"`
	AgeSeconds int64    `json:"age_seconds"`
	Kill       bool     `json:"kill"`
	Swarms     []Swarm  `json:"swarms"`
	Orphans    []Orphan `json:"orphans,omitempty"`
	Errors     []string `json:"errors,omitempty"`
}

// PaneEvidence is everything the idleness decision may consider for one pane.
type PaneEvidence struct {
	// TranscriptMtime is the modification time of the newest Claude Code
	// transcript the pane's process tree holds open; nil when none was found.
	TranscriptMtime *time.Time
	// CaptureTail is the pane's visible content, used only as a fallback.
	CaptureTail string
	// SessionCreated is when the pane's tmux session was created.
	SessionCreated time.Time
}

// PaneIdle judges one pane. The transcript mtime is the most reliable signal;
// the capture-pane fallback additionally requires the session itself to be
// older than the threshold so freshly started swarms are never reapable.
func PaneIdle(evidence PaneEvidence, now time.Time, age time.Duration) (bool, string) {
	if evidence.TranscriptMtime != nil {
		quiet := now.Sub(*evidence.TranscriptMtime)
		if quiet > age {
			return true, fmt.Sprintf("transcript quiet for %s", FormatAge(quiet))
		}
		return false, fmt.Sprintf("transcript written %s ago", FormatAge(quiet))
	}
	if !HasIdleMarker(evidence.CaptureTail) {
		return false, "no transcript and no idle marker on screen"
	}
	if evidence.SessionCreated.IsZero() {
		return false, "idle marker but unknown session age"
	}
	lived := now.Sub(evidence.SessionCreated)
	if lived > age {
		return true, fmt.Sprintf("idle prompt on screen, session %s old", FormatAge(lived))
	}
	return false, fmt.Sprintf("idle prompt but session only %s old", FormatAge(lived))
}

// idleMarkerWindow is how many trailing non-empty screen lines are inspected.
const idleMarkerWindow = 8

// HasIdleMarker reports whether the trailing visible lines of a pane look like
// a Claude Code session resting at its prompt.
func HasIdleMarker(capture string) bool {
	lines := strings.Split(capture, "\n")
	inspected := 0
	for index := len(lines) - 1; index >= 0 && inspected < idleMarkerWindow; index-- {
		trimmed := strings.TrimSpace(lines[index])
		if trimmed == "" {
			continue
		}
		inspected++
		lowered := strings.ToLower(trimmed)
		if strings.HasPrefix(trimmed, "❯") {
			return true
		}
		if strings.Contains(lowered, "/clear to save") || strings.Contains(lowered, "new task?") {
			return true
		}
	}
	return false
}

// LiveVerdict aggregates pane verdicts for a responding server. A swarm is
// reapable only when every pane is idle; no panes means we cannot tell, which
// conservatively counts as active.
func LiveVerdict(panes []Pane) Verdict {
	if len(panes) == 0 {
		return VerdictActive
	}
	for _, pane := range panes {
		if !pane.Idle {
			return VerdictActive
		}
	}
	return VerdictIdleReapable
}

// PaneInfo is one parsed line of `tmux list-panes -a -F PaneFormat`.
type PaneInfo struct {
	ID             string
	PID            int
	Command        string
	SessionCreated time.Time
	Path           string
	Title          string
}

// PaneFormat is the tmux format string ParsePaneLine understands.
const PaneFormat = "#{pane_id}\t#{pane_pid}\t#{pane_current_command}\t#{session_created}\t#{pane_current_path}\t#{pane_title}"

// ParsePaneLine parses one tab-separated pane line produced by PaneFormat.
func ParsePaneLine(line string) (PaneInfo, error) {
	parts := strings.SplitN(line, "\t", 6)
	if len(parts) < 5 {
		return PaneInfo{}, fmt.Errorf("malformed pane line %q", line)
	}
	pid, err := strconv.Atoi(parts[1])
	if err != nil {
		return PaneInfo{}, fmt.Errorf("pane pid in %q: %w", line, err)
	}
	created, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return PaneInfo{}, fmt.Errorf("session created in %q: %w", line, err)
	}
	info := PaneInfo{
		ID: parts[0], PID: pid, Command: parts[2],
		SessionCreated: time.Unix(created, 0), Path: parts[4],
	}
	if len(parts) == 6 {
		info.Title = parts[5]
	}
	return info, nil
}

// ParseLsofTranscripts extracts Claude Code transcript paths from
// `lsof -p <pid> -Fn` output.
func ParseLsofTranscripts(output string) []string {
	var result []string
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "n") {
			continue
		}
		name := line[1:]
		if strings.Contains(name, "/.claude/projects/") && strings.HasSuffix(name, ".jsonl") {
			result = append(result, name)
		}
	}
	return result
}

// ParseOrphans extracts orphaned `claude --agent-id` processes from
// `ps -axo pid=,ppid=,etime=,command=` output. Report-only.
func ParseOrphans(output string) []Orphan {
	var result []Orphan
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[1] != "1" {
			continue
		}
		command := strings.Join(fields[3:], " ")
		if !strings.Contains(command, "--agent-id") {
			continue
		}
		if !strings.Contains(filepath.Base(fields[3]), "claude") {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		result = append(result, Orphan{PID: pid, Elapsed: fields[2], Command: command})
	}
	return result
}

// ProjectName derives a human label for a pane from its working directory,
// falling back to the pane title.
func ProjectName(info PaneInfo, home string) string {
	if info.Path != "" && info.Path != "/" && info.Path != home {
		return filepath.Base(info.Path)
	}
	return strings.TrimSpace(info.Title)
}

// FormatAge renders a duration compactly: 45m, 26h, 3d2h.
func FormatAge(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	if duration < time.Hour {
		return fmt.Sprintf("%dm", int(duration.Minutes()))
	}
	days := int(duration.Hours()) / 24
	hours := int(duration.Hours()) % 24
	if days == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	if hours == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd%dh", days, hours)
}

// FormatText renders the human-readable audit table.
func FormatText(report Report) string {
	var builder strings.Builder
	if len(report.Swarms) == 0 {
		builder.WriteString("no claude-swarm sockets in " + report.TmuxDir + "\n")
	} else {
		fmt.Fprintf(&builder, "%-24s %-14s %-8s %-6s %s\n", "SOCKET", "VERDICT", "AGE", "PANES", "PROJECT")
		counts := map[Verdict]int{}
		for _, swarm := range report.Swarms {
			counts[swarm.Verdict]++
			fmt.Fprintf(&builder, "%-24s %-14s %-8s %-6d %s\n",
				swarm.Socket, swarm.Verdict,
				FormatAge(time.Duration(swarm.AgeSeconds)*time.Second),
				len(swarm.Panes), swarm.Project)
			for _, action := range swarm.Actions {
				fmt.Fprintf(&builder, "    → %s\n", action)
			}
			for _, failure := range swarm.Errors {
				fmt.Fprintf(&builder, "    ! %s\n", failure)
			}
		}
		fmt.Fprintf(&builder, "\nclaude-sweep: %d swarm(s) — %d active, %d idle-reapable, %d dead socket(s)\n",
			len(report.Swarms), counts[VerdictActive], counts[VerdictIdleReapable], counts[VerdictDeadSocket])
	}
	if report.Orphans != nil {
		builder.WriteString("\nORPHANS (ppid 1, report-only)\n")
		if len(report.Orphans) == 0 {
			builder.WriteString("  none\n")
		}
		for _, orphan := range report.Orphans {
			fmt.Fprintf(&builder, "  pid %-7d up %-12s %s\n", orphan.PID, orphan.Elapsed, truncate(orphan.Command, 100))
		}
	}
	for _, failure := range report.Errors {
		fmt.Fprintf(&builder, "! %s\n", failure)
	}
	return builder.String()
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max] + "…"
}

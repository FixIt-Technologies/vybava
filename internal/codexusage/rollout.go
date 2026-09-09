package codexusage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Rollout trees Codex writes to. Archived threads still count against a limit
// that was spent before they were archived.
var rolloutRoots = []string{"sessions", "archived_sessions"}

// tokenCountMarker gates JSON decoding. A rollout is mostly transcript — tens
// of megabytes of tool output per file — and only the handful of lines
// carrying this marker are usage evidence, so the substring test runs first.
var tokenCountMarker = []byte(`"token_count"`)

type rolloutLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type sessionMeta struct {
	ID         string `json:"id"`
	Timestamp  string `json:"timestamp"`
	CWD        string `json:"cwd"`
	CLIVersion string `json:"cli_version"`
	Git        *struct {
		Branch string `json:"branch"`
	} `json:"git"`
}

type usageJSON struct {
	Input      int64 `json:"input_tokens"`
	Cached     int64 `json:"cached_input_tokens"`
	CacheWrite int64 `json:"cache_write_input_tokens"`
	Output     int64 `json:"output_tokens"`
	Reasoning  int64 `json:"reasoning_output_tokens"`
	Total      int64 `json:"total_tokens"`
}

func (u usageJSON) usage() Usage {
	return Usage{Input: u.Input, Cached: u.Cached, CacheWrite: u.CacheWrite, Output: u.Output, Reasoning: u.Reasoning}
}

type tokenCountPayload struct {
	Type string `json:"type"`
	Info *struct {
		Total         usageJSON `json:"total_token_usage"`
		Last          usageJSON `json:"last_token_usage"`
		ContextWindow int64     `json:"model_context_window"`
	} `json:"info"`
	RateLimits *struct {
		LimitID  string `json:"limit_id"`
		PlanType string `json:"plan_type"`
		Primary  *struct {
			UsedPercent   float64 `json:"used_percent"`
			WindowMinutes int     `json:"window_minutes"`
			ResetsAt      int64   `json:"resets_at"`
		} `json:"primary"`
	} `json:"rate_limits"`
}

func (p tokenCountPayload) limit() *Limit {
	if p.RateLimits == nil || p.RateLimits.Primary == nil {
		return nil
	}
	return &Limit{
		Plan:          p.RateLimits.PlanType,
		ID:            p.RateLimits.LimitID,
		UsedPercent:   p.RateLimits.Primary.UsedPercent,
		WindowMinutes: p.RateLimits.Primary.WindowMinutes,
		ResetsAt:      time.Unix(p.RateLimits.Primary.ResetsAt, 0),
	}
}

// scanSessions reads every rollout that could hold a call inside the window.
// A single unreadable rollout is a warning, never a failure — a partial answer
// beats none when a limit is already burning.
func scanSessions(env Env, since time.Time) ([]Session, []string, error) {
	paths, err := rolloutPaths(env.Home, since)
	if err != nil {
		return nil, nil, err
	}
	var (
		sessions []Session
		warnings []string
	)
	for _, path := range paths {
		session, err := readRollout(path, since)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", filepath.Base(path), err))
			continue
		}
		sessions = append(sessions, session)
	}
	return sessions, warnings, nil
}

// rolloutPaths prefilters by modification time. A rollout untouched since
// before the window cannot contain a call inside it, and skipping it avoids
// reading hundreds of megabytes of settled transcript.
func rolloutPaths(home string, since time.Time) ([]string, error) {
	var paths []string
	for _, root := range rolloutRoots {
		dir := filepath.Join(home, ".codex", root)
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), "rollout-") || !strings.HasSuffix(entry.Name(), ".jsonl") {
				return nil
			}
			info, err := entry.Info()
			if err != nil || info.ModTime().Before(since) {
				return nil
			}
			paths = append(paths, path)
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// readRollout extracts a thread's identity and its in-window calls.
func readRollout(path string, since time.Time) (Session, error) {
	file, err := os.Open(path)
	if err != nil {
		return Session{}, err
	}
	defer file.Close()

	session := Session{File: path}
	reader := bufio.NewReaderSize(file, 256*1024)

	// The first line is session_meta and carries the model's base instructions,
	// so it can be megabytes on its own — read it unbounded, once.
	if head, err := reader.ReadString('\n'); err == nil || len(head) > 0 {
		applyMeta(&session, []byte(head))
	}

	var previous usageJSON
	for {
		line, err := reader.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			// A line longer than the buffer is transcript, never a token_count
			// event; drop the remainder without materializing it.
			for err == bufio.ErrBufferFull {
				_, err = reader.ReadSlice('\n')
			}
			if err != nil {
				break
			}
			continue
		}
		if len(line) > 0 && bytes.Contains(line, tokenCountMarker) {
			appendSample(&session, line, since, &previous)
		}
		if err != nil {
			break
		}
	}
	return session, nil
}

func applyMeta(session *Session, line []byte) {
	var entry rolloutLine
	if json.Unmarshal(line, &entry) != nil || entry.Type != "session_meta" {
		return
	}
	var meta sessionMeta
	if json.Unmarshal(entry.Payload, &meta) != nil {
		return
	}
	session.ID, session.CWD, session.Version = meta.ID, meta.CWD, meta.CLIVersion
	if meta.Git != nil {
		session.Branch = meta.Git.Branch
	}
	if at, err := time.Parse(time.RFC3339, meta.Timestamp); err == nil {
		session.StartedAt = at.Local()
	}
	if session.ID == "" {
		session.ID = idFromPath(session.File)
	}
}

// appendSample records one call, skipping the repeat events Codex emits when
// only the rate limit refreshed. Those carry a stale last_token_usage that
// would be billed twice, but a fresh percentage worth keeping — so the limit
// is folded onto the existing sample instead.
func appendSample(session *Session, line []byte, since time.Time, previous *usageJSON) {
	var entry rolloutLine
	if json.Unmarshal(line, &entry) != nil || entry.Type != "event_msg" {
		return
	}
	var payload tokenCountPayload
	if json.Unmarshal(entry.Payload, &payload) != nil || payload.Type != "token_count" {
		return
	}
	at, err := time.Parse(time.RFC3339, entry.Timestamp)
	if err != nil {
		return
	}
	local := at.Local()
	limit := payload.limit()

	if local.Before(since) {
		if payload.Info != nil {
			*previous = payload.Info.Total
		}
		return
	}
	// An unchanged session total means no new call happened — this event only
	// refreshed the quota reading. Recording it unbilled keeps the percentage
	// trajectory intact without charging the call it echoes a second time.
	if payload.Info == nil || payload.Info.Total == *previous {
		if limit != nil {
			session.Samples = append(session.Samples, Sample{At: local, Limit: limit})
		}
		return
	}
	*previous = payload.Info.Total
	session.Samples = append(session.Samples, Sample{
		At:            local,
		Usage:         payload.Info.Last.usage(),
		ContextWindow: payload.Info.ContextWindow,
		Limit:         limit,
		Billed:        true,
	})
}

// idFromPath recovers a thread id from its filename when session_meta is
// unreadable: rollout-<RFC3339-ish timestamp>-<uuid>.jsonl.
func idFromPath(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if parts := strings.SplitN(strings.TrimPrefix(name, "rollout-"), "-", 4); len(parts) == 4 {
		return parts[3]
	}
	return ""
}

// loadNames reads the thread titles Codex keeps outside the rollouts. The
// index is append-only and a thread is renamed by appending, so the last entry
// for an id wins.
func loadNames(env Env) map[string]string {
	names := map[string]string{}
	file, err := os.Open(filepath.Join(env.Home, ".codex", "session_index.jsonl"))
	if err != nil {
		return names
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var entry struct {
			ID   string `json:"id"`
			Name string `json:"thread_name"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) == nil && entry.ID != "" && entry.Name != "" {
			names[entry.ID] = entry.Name
		}
	}
	return names
}

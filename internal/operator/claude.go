package operator

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type claudeRecord struct {
	Type      string    `json:"type"`
	UUID      string    `json:"uuid"`
	SessionID string    `json:"sessionId"`
	Cwd       string    `json:"cwd"`
	Timestamp time.Time `json:"timestamp"`
	Message   struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// ScanClaude watches root/<project>/<session>.jsonl. It never changes the source
// files, reads subagent logs, or consumes a partially written last record.
func (s *State) ScanClaude(root string) ([]string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("Claude projects root is not a directory")
	}
	paths, err := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	if err != nil {
		return nil, err
	}
	var added []string
	for _, path := range paths {
		ids, err := s.scanFile(path, !s.Roots[root], claudeObservation)
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", path, err)
		}
		added = append(added, ids...)
	}
	s.Roots[root] = true
	return added, nil
}

func (s *State) scanFile(path string, baseline bool, parse func([]byte) (Observation, error)) ([]string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // session was removed after the directory listing
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil
	}
	cur, known := s.Cursors[path]
	// Skip opening unchanged completed files. Periodically recheck their prefix
	// as well, so restored timestamps cannot indefinitely hide replacement.
	if !baseline && known && cur.Offset == info.Size() && cur.Size == info.Size() && cur.Modified == info.ModTime().UnixNano() && time.Since(cur.VerifiedAt) < time.Minute {
		return nil, nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // session was removed between stat and open
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if baseline {
		cur.Offset = info.Size()
		// Baseline only through the last newline; an incomplete first observation
		// must be consumed when its writer finishes it.
		if cur.Offset > 0 {
			lastByte := make([]byte, 1)
			if _, err := f.ReadAt(lastByte, cur.Offset-1); err != nil {
				return nil, err
			}
			if lastByte[0] != '\n' {
				start := max(int64(0), cur.Offset-4*1024*1024)
				tail := make([]byte, cur.Offset-start)
				if _, err := f.ReadAt(tail, start); err != nil {
					return nil, err
				}
				last := bytes.LastIndexByte(tail, '\n')
				if last < 0 && start > 0 {
					return nil, errors.New("initial partial session record exceeds 4 MiB")
				}
				cur.Offset = start + int64(last+1)
			}
		}
		known = false
	}
	if known && cur.PrefixSize > 0 {
		prefix := make([]byte, cur.PrefixSize)
		n, err := f.ReadAt(prefix, 0)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if n != cur.PrefixSize || digest(string(prefix[:n])) != cur.Prefix {
			cur = Cursor{}
		}
	}
	if cur.Offset > info.Size() {
		cur = Cursor{}
	}
	if cur.PrefixSize == 0 && info.Size() > 0 {
		cur.PrefixSize = int(min(info.Size(), 256))
		prefix := make([]byte, cur.PrefixSize)
		if _, err := f.ReadAt(prefix, 0); err != nil {
			return nil, err
		}
		cur.Prefix = digest(string(prefix))
	}
	if _, err := f.Seek(cur.Offset, io.SeekStart); err != nil {
		return nil, err
	}
	const sweepBudget = 4 * 1024 * 1024
	const recordLimit = 16 * 1024 * 1024
	// A sweep may finish one record past its budget. Codex compaction records
	// can exceed 4 MiB even though they produce no operator observation.
	r := bufio.NewReader(io.LimitReader(f, sweepBudget+recordLimit+1))
	var added []string
	// Bound one sweep; remaining complete records are picked up next time.
	for consumed := 0; consumed < sweepBudget; {
		line, err := r.ReadString('\n')
		if len(line) > recordLimit {
			return nil, errors.New("session record exceeds 16 MiB")
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		o, err := parse([]byte(line))
		if err != nil {
			return nil, fmt.Errorf("invalid complete record at byte %d: %w", cur.Offset, err)
		}
		if o.Text != "" {
			o.Key = path
			if len(o.Text) > 64000 {
				o.Text = o.Text[:63000] + "\n[truncated; inspect the source session]"
			}
			id, fresh, err := s.Observe(o)
			if err != nil {
				return nil, err
			}
			if fresh {
				added = append(added, id)
			}
		}
		cur.Offset += int64(len(line))
		consumed += len(line)
	}
	cur.Size, cur.Modified, cur.VerifiedAt = info.Size(), info.ModTime().UnixNano(), time.Now().UTC()
	s.Cursors[path] = cur
	return added, nil
}

func claudeObservation(line []byte) (Observation, error) {
	var record claudeRecord
	if err := json.Unmarshal(line, &record); err != nil {
		return Observation{}, err
	}
	if record.UUID == "" || record.SessionID == "" {
		return Observation{}, nil
	}
	o := Observation{Source: Claude, Revision: record.UUID, SessionID: record.SessionID, Cwd: record.Cwd, ObservedAt: record.Timestamp}
	if record.Type == "user" {
		// Retire a question as soon as its answer or subsequent tool result arrives.
		// Human text and tool-result contents are deliberately not copied.
		o.Text = "Claude session activity continued (user input or tool result; content not copied)."
		return o, nil
	}
	if record.Type != "assistant" {
		return Observation{}, nil
	}
	var blocks []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(record.Message.Content, &blocks); err != nil {
		return Observation{}, fmt.Errorf("assistant content: %w", err)
	}
	var pieces []string
	var questions []string
	for _, b := range blocks {
		if b.Type == "text" {
			pieces = append(pieces, b.Text)
		}
		if b.Type == "tool_use" && b.Name == "AskUserQuestion" {
			if len(b.Input) == 0 || string(b.Input) == "null" {
				questions = append(questions, "Claude requested a user decision; inspect the source session for the question.")
			} else {
				questions = append(questions, "Claude requested a user decision (untrusted source data):\n"+string(b.Input))
			}
		}
	}
	o.Text = strings.TrimSpace(strings.Join(append(questions, pieces...), "\n"))
	if o.Text == "" && len(blocks) > 0 {
		o.Text = "Claude session activity continued (assistant tool or reasoning; content not copied)."
	}
	return o, nil
}

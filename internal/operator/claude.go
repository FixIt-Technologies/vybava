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
		ids, err := s.scanFile(path, !s.Roots[root])
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", path, err)
		}
		added = append(added, ids...)
	}
	s.Roots[root] = true
	return added, nil
}

func (s *State) scanFile(path string, baseline bool) ([]string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cur, known := s.Cursors[path]
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
					return nil, errors.New("initial partial Claude record exceeds 4 MiB")
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
	r := bufio.NewReader(io.LimitReader(f, 4*1024*1024+1))
	var added []string
	// Bound one sweep; remaining complete records are picked up next time.
	for consumed := 0; consumed < 4*1024*1024; {
		line, err := r.ReadString('\n')
		if len(line) > 4*1024*1024 {
			return nil, errors.New("Claude record exceeds 4 MiB")
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		var record claudeRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, fmt.Errorf("invalid complete record at byte %d: %w", cur.Offset, err)
		}
		if record.Type == "assistant" && record.UUID != "" && record.SessionID != "" {
			var blocks []struct {
				Type string `json:"type"`
				Text string `json:"text"`
				Name string `json:"name"`
			}
			if err := json.Unmarshal(record.Message.Content, &blocks); err != nil {
				return nil, fmt.Errorf("assistant content: %w", err)
			}
			var pieces []string
			for _, b := range blocks {
				if b.Type == "text" {
					pieces = append(pieces, b.Text)
				}
				if b.Type == "tool_use" && b.Name == "AskUserQuestion" {
					pieces = append(pieces, "Claude requested a user decision; inspect the source session for the question.")
				}
			}
			text := strings.TrimSpace(strings.Join(pieces, "\n"))
			if len(text) > 64000 {
				text = text[:63000] + "\n[truncated; inspect the source session]"
			}
			if text != "" {
				id, fresh, err := s.Observe(Observation{Source: Claude, Key: path, Revision: record.UUID, Text: text, ObservedAt: record.Timestamp})
				if err != nil {
					return nil, err
				}
				if fresh {
					added = append(added, id)
				}
			}
		}
		cur.Offset += int64(len(line))
		consumed += len(line)
	}
	s.Cursors[path] = cur
	return added, nil
}

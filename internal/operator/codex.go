package operator

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type codexMetadata struct {
	ID           string          `json:"id"`
	SessionID    string          `json:"session_id"`
	Cwd          string          `json:"cwd"`
	Source       json.RawMessage `json:"source"`
	ThreadSource string          `json:"thread_source"`
}

// ScanCodex reads top-level rollouts only. Exclusions are explicit even when
// dispatch is paused: observing the operator itself would create a feedback loop.
func (s *State) ScanCodex(root string, excluded []string) ([]string, error) {
	if len(excluded) == 0 {
		return nil, errors.New("Codex observation requires an excluded operator session ID")
	}
	for _, id := range excluded {
		if strings.TrimSpace(id) == "" {
			return nil, errors.New("excluded Codex session ID must not be empty")
		}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("Codex sessions root is not a directory")
	}
	paths, err := filepath.Glob(filepath.Join(root, "*", "*", "*", "rollout-*.jsonl"))
	if err != nil {
		return nil, err
	}
	var added []string
	for _, path := range paths {
		meta, err := readCodexMetadata(path)
		if err != nil {
			return nil, fmt.Errorf("metadata %s: %w", path, err)
		}
		if meta.ID == "" {
			continue
		}
		skip := false
		for _, id := range excluded {
			if id == meta.ID || id == meta.SessionID {
				skip = true
			}
		}
		if skip {
			continue
		}
		ids, err := s.scanFile(path, !s.Roots[root], func(line []byte) (Observation, error) {
			return codexObservation(line, meta)
		})
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", path, err)
		}
		added = append(added, ids...)
	}
	s.Roots[root] = true
	return added, nil
}

func readCodexMetadata(path string) (codexMetadata, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return codexMetadata{}, err
	}
	if !info.Mode().IsRegular() {
		return codexMetadata{}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return codexMetadata{}, err
	}
	defer f.Close()
	line, err := bufio.NewReader(io.LimitReader(f, 4*1024*1024+1)).ReadBytes('\n')
	if len(line) > 4*1024*1024 {
		return codexMetadata{}, errors.New("Codex metadata exceeds 4 MiB")
	}
	if errors.Is(err, io.EOF) {
		return codexMetadata{}, nil // metadata is still being written
	}
	if err != nil {
		return codexMetadata{}, err
	}
	var record struct {
		Type     string        `json:"type"`
		LegacyID string        `json:"id"`
		Payload  codexMetadata `json:"payload"`
	}
	if err := json.Unmarshal(line, &record); err != nil {
		return codexMetadata{}, err
	}
	meta := record.Payload
	if record.Type == "" && record.LegacyID != "" {
		return codexMetadata{}, nil // legacy flat rollouts have no supported origin metadata
	}
	if record.Type != "session_meta" {
		return codexMetadata{}, errors.New("first Codex record is not session_meta")
	}
	var source string
	if err := json.Unmarshal(meta.Source, &source); err != nil || source == "" || (meta.ThreadSource != "" && meta.ThreadSource != "user") {
		return codexMetadata{}, nil // structured subagent origins are not user sessions
	}
	if meta.ID == "" {
		meta.ID = meta.SessionID
	}
	if meta.ID == "" {
		return codexMetadata{}, errors.New("Codex metadata has no session ID")
	}
	return meta, nil
}

func codexObservation(line []byte, meta codexMetadata) (Observation, error) {
	var record struct {
		Type      string          `json:"type"`
		Timestamp time.Time       `json:"timestamp"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(line, &record); err != nil {
		return Observation{}, err
	}
	if record.Type != "response_item" {
		return Observation{}, nil
	}
	var message struct {
		Type    string          `json:"type"`
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(record.Payload, &message); err != nil {
		return Observation{}, err
	}
	if message.Type != "message" || message.Role != "assistant" {
		return Observation{}, nil
	}
	var content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(message.Content, &content); err != nil {
		return Observation{}, err
	}
	var pieces []string
	for _, block := range content {
		if block.Type == "output_text" {
			pieces = append(pieces, block.Text)
		}
	}
	return Observation{Source: Codex, Revision: digest(string(line)), SessionID: meta.ID, Cwd: meta.Cwd, ObservedAt: record.Timestamp, Text: strings.TrimSpace(strings.Join(pieces, "\n"))}, nil
}

package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type SourceCoverage struct {
	Source        Source     `json:"source"`
	Scope         string     `json:"scope"`
	LastCheckedAt time.Time  `json:"last_checked_at"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	Error         string     `json:"error,omitempty"`
}

type MessagesState struct {
	Database    string               `json:"database"`
	Baseline    int64                `json:"baseline"`
	AnchorGUID  string               `json:"anchor_guid,omitempty"`
	Initialized bool                 `json:"initialized"`
	Records     map[int64]MessageRow `json:"records"`
	Coverage    SourceCoverage       `json:"coverage"`
}

func (s *State) requireMessagesCoverage(source Source) error {
	if source != Messages {
		return nil
	}
	if s.Messages == nil || !s.Messages.Initialized || s.Messages.Coverage.Error != "" || s.Messages.Coverage.LastSuccessAt == nil || time.Since(*s.Messages.Coverage.LastSuccessAt) > 2*time.Minute {
		return errors.New("Messages coverage is stale or incomplete; check the source before preparing or approving a response")
	}
	return nil
}

// ScanMessages captures the range after an explicit first-read baseline. Changed
// metadata is revisited on every pass; failed captures stay eligible for retry.
func (s Store) ScanMessages(ctx context.Context, reader MessagesReader) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := reader.validate(); err != nil {
		return nil, err
	}
	return s.scanWithTime(func(state *State) ([]string, error) {
		now := time.Now().UTC()
		if state.Messages == nil {
			state.Messages = &MessagesState{Database: reader.Database,
				Records: map[int64]MessageRow{}, Coverage: SourceCoverage{Source: Messages,
					Scope:         "Messages created after the local baseline, including later edits and local removal; earlier archive remains in Messages",
					LastCheckedAt: now}}
		}
		m := state.Messages
		if m.Database != reader.Database {
			return nil, errors.New("Messages database differs from the recorded baseline; use a separate state directory")
		}
		m.Coverage.LastCheckedAt = now
		if !m.Initialized {
			baseline, err := reader.Baseline(ctx)
			if err != nil {
				m.Coverage.Error = err.Error()
				return []string{}, nil
			}
			m.Baseline, m.AnchorGUID, m.Initialized = baseline.ID, baseline.GUID, true
			m.Coverage.Error, m.Coverage.LastSuccessAt = "", &now
			return []string{}, nil
		}
		rows, err := reader.Rows(ctx, m.Baseline, m.AnchorGUID)
		if err != nil {
			m.Coverage.Error = err.Error()
			return []string{}, nil
		}
		present := map[int64]bool{}
		for _, row := range rows {
			present[row.ID] = true
		}
		added := []string{}
		failures := 0
		reads := 0
		capture := func(row MessageRow, kind string, payload json.RawMessage) bool {
			text, err := json.Marshal(struct {
				Kind    string          `json:"kind"`
				Row     MessageRow      `json:"identity"`
				Message json.RawMessage `json:"message,omitempty"`
			}{kind, row, payload})
			if err != nil {
				failures++
				return false
			}
			id, fresh, err := state.Observe(Observation{Source: Messages,
				Key:      fmt.Sprintf("%s:chat:%d", digest(reader.Database)[:16], row.ChatID),
				Revision: digest(string(text) + now.Format(time.RFC3339Nano)), Text: string(text), ObservedAt: now})
			if err != nil {
				failures++
				return false
			}
			if fresh {
				added = append(added, id)
			}
			return true
		}
		for _, row := range rows {
			previous, known := m.Records[row.ID]
			if known && previous == row {
				continue
			}
			if reads >= 25 || ctx.Err() != nil {
				failures++
				continue
			}
			reads++
			kind := "message"
			if known {
				kind = "message-changed"
			}
			var payload json.RawMessage
			if row.Retracted > 0 {
				kind = "message-retracted"
			} else {
				payload, err = reader.Message(ctx, row)
				if err != nil {
					failures++
					continue
				}
			}
			if capture(row, kind, payload) {
				m.Records[row.ID] = row
			}
		}
		for id, previous := range m.Records {
			if !present[id] && capture(previous, "message-removed-from-local-history", nil) {
				delete(m.Records, id)
			}
		}
		if failures > 0 {
			m.Coverage.Error = fmt.Sprintf("%d Messages records could not be captured; they remain eligible for retry", failures)
		} else {
			m.Coverage.Error = ""
			completed := time.Now().UTC()
			m.Coverage.LastSuccessAt = &completed
		}
		return added, nil
	}, false)
}

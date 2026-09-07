package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
	Sweep       *MessagesSweep       `json:"sweep,omitempty"`
}

type MessagesSweep struct {
	StartedAt    time.Time      `json:"started_at"`
	High         int64          `json:"high"`
	After        int64          `json:"after"`
	Seen         map[int64]bool `json:"seen"`
	MetadataDone bool           `json:"metadata_done"`
	RemovalAfter int64          `json:"removal_after"`
	Failures     int            `json:"failures"`
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
	return s.scanWithTime(ctx, func(state *State) ([]string, error) {
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
		// Revalidate the pinned reader even after a persisted baseline/restart.
		baseline, err := reader.Baseline(ctx)
		if err != nil {
			m.Coverage.Error = err.Error()
			return []string{}, nil
		}
		if !m.Initialized {
			m.Baseline, m.AnchorGUID, m.Initialized = baseline.ID, baseline.GUID, true
			m.Coverage.Error, m.Coverage.LastSuccessAt = "", &now
			return []string{}, nil
		}
		if m.Sweep == nil {
			m.Sweep = &MessagesSweep{StartedAt: now, High: baseline.ID, After: m.Baseline, Seen: map[int64]bool{}}
		}
		if _, err := reader.Page(ctx, m.Baseline, m.AnchorGUID, m.Baseline, m.Baseline); err != nil {
			m.Coverage.Error = err.Error()
			return []string{}, nil
		}
		sweep := m.Sweep
		added := []string{}
		reads := 0
		capture := func(row MessageRow, kind string, payload json.RawMessage) bool {
			text, err := json.Marshal(struct {
				Kind    string          `json:"kind"`
				Row     MessageRow      `json:"identity"`
				Message json.RawMessage `json:"message,omitempty"`
			}{kind, row, payload})
			if err != nil {
				sweep.Failures++
				return false
			}
			id, fresh, err := state.Observe(Observation{Source: Messages,
				Key:      fmt.Sprintf("%s:chat:%d", digest(reader.Database)[:16], row.ChatID),
				Revision: digest(string(text) + now.Format(time.RFC3339Nano)), Text: string(text), ObservedAt: now})
			if err != nil {
				sweep.Failures++
				return false
			}
			if fresh {
				added = append(added, id)
			}
			return true
		}
		for !sweep.MetadataDone && reads < 25 && ctx.Err() == nil {
			rows, err := reader.Page(ctx, m.Baseline, m.AnchorGUID, sweep.After, sweep.High)
			if err != nil {
				m.Coverage.Error = err.Error()
				return added, nil
			}
			if len(rows) == 0 {
				sweep.MetadataDone = true
				break
			}
			for _, row := range rows {
				if reads >= 25 || ctx.Err() != nil {
					break
				}
				// Advance attempts even on failure; subsequent sweeps retry fairly.
				sweep.After = row.ID
				sweep.Seen[row.ID] = true
				previous, known := m.Records[row.ID]
				if known && previous == row {
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
						sweep.Failures++
						continue
					}
				}
				if capture(row, kind, payload) {
					m.Records[row.ID] = row
				}
			}
		}
		removalsDone := false
		if sweep.MetadataDone && ctx.Err() == nil {
			ids := make([]int64, 0, len(m.Records))
			for id := range m.Records {
				if id > sweep.RemovalAfter && !sweep.Seen[id] {
					ids = append(ids, id)
				}
			}
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			removalsDone = true
			for _, id := range ids {
				if reads >= 25 || ctx.Err() != nil {
					removalsDone = false
					break
				}
				reads++
				// A row may have been restored since its metadata page was read.
				rows, err := reader.Page(ctx, m.Baseline, m.AnchorGUID, id-1, id)
				sweep.RemovalAfter = id
				if err != nil {
					sweep.Failures++
					continue
				}
				if len(rows) > 0 {
					sweep.Failures++
					continue
				}
				if capture(m.Records[id], "message-removed-from-local-history", nil) {
					delete(m.Records, id)
				}
			}
		}
		if !sweep.MetadataDone || !removalsDone {
			m.Coverage.Error = "Messages sweep is incomplete; remaining records will be checked on the next pass"
		} else if sweep.Failures > 0 {
			m.Coverage.Error = fmt.Sprintf("%d Messages records could not be captured; they remain eligible for retry", sweep.Failures)
			m.Sweep = nil
		} else if baseline.ID > sweep.High {
			m.Coverage.Error = "Messages sweep completed, but newer records remain for the next pass"
			m.Sweep = nil
		} else if sweep.StartedAt.IsZero() || time.Since(sweep.StartedAt) > 2*time.Minute {
			m.Coverage.Error = "Messages sweep completed with stale checks; a new sweep must reverify the source"
			m.Sweep = nil
		} else {
			m.Coverage.Error = ""
			// Freshness belongs to the oldest check, never the final publication.
			checked := sweep.StartedAt
			m.Coverage.LastSuccessAt = &checked
			m.Sweep = nil
		}
		return added, nil
	}, false)
}

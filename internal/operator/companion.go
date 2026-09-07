package operator

import (
	"errors"
	"strings"
	"time"
)

// Feedback preserves the human's words without inventing a numeric rating.
type Feedback struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

type CompanionSnapshot struct {
	Version      int        `json:"version"`
	CheckedAt    time.Time  `json:"checked_at"`
	Summary      Summary    `json:"summary"`
	LastScanAt   *time.Time `json:"last_scan_at,omitempty"`
	Events       []Event    `json:"events"`
	Capabilities []string   `json:"capabilities"`
}

// Snapshot exposes prepared work, including history, in one consistent read.
// Source text is data only: this surface never executes an observed instruction.
func (s *State) Snapshot() CompanionSnapshot {
	r := CompanionSnapshot{Version: 1, CheckedAt: time.Now().UTC(), Summary: s.Summary(), LastScanAt: s.LastScanAt, Events: []Event{}}
	r.Capabilities = []string{"history", "review-decisions"}
	for _, e := range s.Events {
		if len(e.Proposals) > 0 {
			r.Events = append(r.Events, e)
		}
	}
	return r
}

func (s *State) AddFeedback(id string, proposal int, text string) error {
	e, err := s.Find(id)
	if err != nil {
		return err
	}
	if proposal < 1 || proposal > len(e.Proposals) {
		return errors.New("proposal number does not exist (numbers start at 1)")
	}
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 64000 {
		return errors.New("feedback must contain 1–64000 bytes")
	}
	p := &e.Proposals[proposal-1]
	p.Feedback = append(p.Feedback, Feedback{Text: text, At: time.Now().UTC()})
	return nil
}

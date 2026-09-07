package operator

import (
	"errors"
	"strings"
	"time"
)

// History is newest-ingested first. Event IDs are stable cursors, so appends do
// not shift subsequent pages. It contains observations, not a full app archive.
type HistoryPage struct {
	Events []Event `json:"events"`
	Next   string  `json:"next,omitempty"`
}

func (s *State) History(before string, limit int) (HistoryPage, error) {
	r := HistoryPage{Events: []Event{}}
	if limit < 1 || limit > 200 {
		return r, errors.New("history limit must be 1–200")
	}
	end := len(s.Events)
	if before != "" {
		end = -1
		for i := range s.Events {
			if s.Events[i].ID == before {
				end = i
				break
			}
		}
		if end < 0 {
			return r, errors.New("history cursor does not exist")
		}
	}
	for i := end - 1; i >= 0 && len(r.Events) < limit; i-- {
		r.Events = append(r.Events, s.Events[i])
		if i > 0 {
			r.Next = s.Events[i].ID
		} else {
			r.Next = ""
		}
	}
	return r, nil
}

// ReviewDecision records a human's disposition of this exact proposal. It is
// never consumed as source-app permission and has no execution side effect.
type ReviewDecision struct {
	Action         string     `json:"action"`
	Note           string     `json:"note,omitempty"`
	At             time.Time  `json:"at"`
	Delivery       string     `json:"delivery,omitempty"`
	Thread         string     `json:"thread,omitempty"`
	Receipt        string     `json:"receipt,omitempty"`
	Error          string     `json:"error,omitempty"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
}

func (s *State) Review(id string, proposal int) (*ReviewDecision, error) {
	e, err := s.Find(id)
	if err != nil {
		return nil, err
	}
	if proposal < 1 || proposal > len(e.Proposals) || e.Proposals[proposal-1].Decision == nil {
		return nil, errors.New("review decision does not exist")
	}
	return e.Proposals[proposal-1].Decision, nil
}

func (s *State) AcknowledgeReview(id string, proposal int) error {
	r, err := s.Review(id, proposal)
	if err != nil {
		return err
	}
	if r.AcknowledgedAt == nil {
		now := time.Now().UTC()
		r.AcknowledgedAt = &now
	}
	return nil
}

func (s *State) NextReview() (string, int) {
	// One unacknowledged review at a time. Ambiguous delivery never auto-retries.
	for _, e := range s.Events {
		for _, p := range e.Proposals {
			if p.Decision != nil && p.Decision.Delivery != "" && p.Decision.AcknowledgedAt == nil {
				return "", 0
			}
		}
	}
	for _, e := range s.Events {
		for i, p := range e.Proposals {
			if p.Decision != nil && p.Decision.Delivery == "" && p.Decision.AcknowledgedAt == nil {
				return e.ID, i + 1
			}
		}
	}
	return "", 0
}

func (s *State) Decide(id string, proposal int, action, note string) error {
	e, err := s.Find(id)
	if err != nil {
		return err
	}
	if action != "reject" {
		if err := s.requireMessagesCoverage(e.Source); err != nil {
			return err
		}
	}
	if proposal < 1 || proposal > len(e.Proposals) {
		return errors.New("proposal number does not exist (numbers start at 1)")
	}
	if action != "approve" && action != "reject" && action != "revise" {
		return errors.New("decision must be approve, reject or revise")
	}
	p := &e.Proposals[proposal-1]
	if e.Superseded || p.Superseded {
		return errors.New("context changed; review the latest proposal")
	}
	if p.Decision != nil {
		return errors.New("proposal already decided; prepare a new revision")
	}
	note = strings.TrimSpace(note)
	if len(note) > 64000 || (action == "revise" && note == "") {
		return errors.New("revision needs a note; notes must not exceed 64000 bytes")
	}
	p.Decision = &ReviewDecision{Action: action, Note: note, At: time.Now().UTC()}
	return nil
}

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
	Action string    `json:"action"`
	Note   string    `json:"note,omitempty"`
	At     time.Time `json:"at"`
}

func (s *State) Decide(id string, proposal int, action, note string) error {
	e, err := s.Find(id)
	if err != nil {
		return err
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

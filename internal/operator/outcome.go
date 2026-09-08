package operator

import (
	"errors"
	"strings"
	"time"
)

// Outcome records reported execution evidence, never performs an action. A
// human review is deliberately a separate record and creates no outcome.
type Outcome struct {
	Proposal int       `json:"proposal"`
	Status   string    `json:"status"`
	Evidence string    `json:"evidence"`
	At       time.Time `json:"at"`
}

func (s *State) RecordOutcome(id string, proposal int, status, evidence string) error {
	event, err := s.Find(id)
	if err != nil {
		return err
	}
	if proposal < 1 || proposal > len(event.Proposals) {
		return errors.New("proposal number does not exist (numbers start at 1)")
	}
	if status != "executed" && status != "verified" && status != "failed" {
		return errors.New("outcome status must be executed, verified or failed")
	}
	evidence = strings.TrimSpace(evidence)
	if evidence == "" || len(evidence) > 64000 {
		return errors.New("outcome requires 1–64000 bytes of actual evidence")
	}
	if status == "verified" {
		executed := false
		for _, previous := range event.Outcomes {
			if previous.Proposal == proposal {
				executed = previous.Status == "executed" || previous.Status == "verified"
			}
		}
		if !executed {
			return errors.New("record actual execution before its verification")
		}
	}
	event.Outcomes = append(event.Outcomes, Outcome{Proposal: proposal, Status: status, Evidence: evidence, At: time.Now().UTC()})
	return nil
}

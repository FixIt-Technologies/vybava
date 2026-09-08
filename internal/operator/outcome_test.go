package operator

import "testing"

func TestOutcomeRequiresEvidenceAndDoesNotFollowApproval(t *testing.T) {
	s := State{Version: 4, Events: []Event{{ID: "event", Proposals: []Proposal{{Decision: &ReviewDecision{Action: "approve"}}}}}}
	if err := s.RecordOutcome("event", 1, "executed", "legacy writer would lose this"); err == nil {
		t.Fatal("outcome accepted before indexed migration")
	}
	s.Version = 5
	if len(s.Events[0].Outcomes) != 0 {
		t.Fatal("approval must not imply execution")
	}
	if err := s.RecordOutcome("event", 1, "verified", "checked target"); err == nil {
		t.Fatal("verification without execution accepted")
	}
	if err := s.RecordOutcome("event", 1, "executed", " "); err == nil {
		t.Fatal("execution without evidence accepted")
	}
	if err := s.RecordOutcome("event", 1, "executed", "run 123 completed on dev"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordOutcome("event", 1, "verified", "target returned expected version"); err != nil {
		t.Fatal(err)
	}
	if len(s.Events[0].Outcomes) != 2 || s.Events[0].Outcomes[0].Evidence != "run 123 completed on dev" {
		t.Fatal("execution evidence was not preserved")
	}
}

package operator

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestReviewDeliveryPersistsBeforeQueueAndNeverRetriesAmbiguousAcceptance(t *testing.T) {
	s := testStore(t)
	id := addObservation(t, s, "review-notice")
	if err := s.With(func(state *State) error {
		if err := state.Propose(id, "Private draft"); err != nil {
			return err
		}
		return state.Decide(id, 1, "revise", "Private human note")
	}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	queue := func(_ context.Context, thread, message string) (string, error) {
		calls++
		if thread != "operator" || !strings.Contains(message, "review-ack") || strings.Contains(message, "Private") {
			t.Fatal("wrong target or leaked content")
		}
		if err := s.View(func(state *State) error {
			r, err := state.Review(id, 1)
			if err != nil {
				return err
			}
			if r.Delivery != "submitting" {
				t.Fatal("queued before durable reservation")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return "uncertain receipt", errors.New("transport disconnected")
	}
	if err := s.DeliverReview(context.Background(), id, 1, "operator", queue); err == nil {
		t.Fatal("lost delivery error")
	}
	if err := s.DeliverReview(context.Background(), id, 1, "operator", queue); err == nil || calls != 1 {
		t.Fatal("replayed ambiguous delivery")
	}
	if err := s.With(func(state *State) error { return state.AcknowledgeReview(id, 1) }); err != nil {
		t.Fatal(err)
	}
	if err := s.View(func(state *State) error {
		r, err := state.Review(id, 1)
		if err != nil {
			return err
		}
		if r.AcknowledgedAt == nil || r.Receipt != "uncertain receipt" {
			t.Fatal("actual receipt lost")
		}
		if next, _ := state.NextReview(); next != "" {
			t.Fatal("acknowledged review replayed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryIncludesUnpreparedEventsAndKeepsCursorAcrossAppends(t *testing.T) {
	s := testStore(t)
	a := addObservation(t, s, "a")
	b := addObservation(t, s, "b")
	c := addObservation(t, s, "c")
	var cursor string
	if err := s.View(func(state *State) error {
		p, err := state.History("", 2)
		if err != nil {
			return err
		}
		if len(p.Events) != 2 || p.Events[0].ID != c || p.Events[1].ID != b || p.Next != b {
			t.Fatalf("wrong first page: %+v", p)
		}
		cursor = p.Next
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	addObservation(t, s, "d")
	if err := s.View(func(state *State) error {
		p, err := state.History(cursor, 2)
		if err != nil {
			return err
		}
		if len(p.Events) != 1 || p.Events[0].ID != a || p.Next != "" {
			t.Fatalf("append shifted cursor: %+v", p)
		}
		if _, err := state.History("missing", 2); err == nil {
			t.Fatal("accepted bad cursor")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReviewDecisionPersistsWithoutSendingAndRejectsStaleApproval(t *testing.T) {
	s := testStore(t)
	id := addObservation(t, s, "review")
	if err := s.With(func(state *State) error {
		if err := state.Propose(id, "Draft"); err != nil {
			return err
		}
		if err := state.Decide(id, 1, "revise", " "); err == nil {
			t.Fatal("empty revision accepted")
		}
		return state.Decide(id, 1, "approve", "Looks right")
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.With(func(state *State) error {
		e, err := state.Find(id)
		if err != nil {
			return err
		}
		if e.Proposals[0].Decision.Action != "approve" || e.Proposals[0].Rating != nil || e.Delivery != "pending" {
			t.Fatal("decision lost, fabricated rating or side effect")
		}
		if err := state.Decide(id, 1, "reject", ""); err == nil {
			t.Fatal("decision overwritten")
		}
		return state.Propose(id, "Revised draft")
	}); err != nil {
		t.Fatal(err)
	}
	addObservation(t, s, "changed")
	if err := s.With(func(state *State) error {
		if err := state.Decide(id, 2, "approve", ""); err == nil {
			t.Fatal("stale approval accepted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

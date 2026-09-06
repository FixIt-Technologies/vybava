package operator

import (
	"testing"
	"time"
)

func TestSlowScanDoesNotBlockOrOverwriteHumanFeedback(t *testing.T) {
	s := testStore(t)
	id := addObservation(t, s, "human")
	if err := s.With(func(state *State) error { return state.Propose(id, "draft") }); err != nil {
		t.Fatal(err)
	}
	reading, release := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		_, err := s.scan(func(state *State) ([]string, error) {
			close(reading)
			<-release
			newID, _, err := state.Observe(Observation{Source: Claude, Key: "source", Revision: "new", Text: "Handoff written: task"})
			state.Cursors["source"] = Cursor{Offset: 42}
			return []string{newID}, err
		})
		finished <- err
	}()
	<-reading
	wrote := make(chan error, 1)
	go func() {
		wrote <- s.With(func(state *State) error {
			if err := state.Acknowledge(id); err != nil {
				return err
			}
			return state.AddFeedback(id, 1, "Actual human correction")
		})
	}()
	select {
	case err := <-wrote:
		if err != nil {
			close(release)
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		<-finished
		t.Fatal("scan held the feedback lock")
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := s.View(func(state *State) error {
		e, _ := state.Find(id)
		if e.AcknowledgedAt == nil || len(e.Proposals[0].Feedback) != 1 || state.Cursors["source"].Offset != 42 || state.LastScanAt == nil {
			t.Fatal("scan overwrote feedback, receipt, or cursors")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

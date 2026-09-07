package operator

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestMessagesWaitingForScanLockHonorsCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[deadline], func(t *testing.T) {
			s := testStore(t)
			if err := s.prepare(); err != nil {
				t.Fatal(err)
			}
			unlock, err := lock(filepath.Join(s.Dir, "scan.lock"))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			want := context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
				want = context.DeadlineExceeded
			}
			defer cancel()
			var read atomic.Bool
			r := MessagesReader{Binary: "/verified/imsg", Database: "/messages/chat.db", run: func(context.Context, string, []string, []byte) ([]byte, error) {
				read.Store(true)
				return nil, errors.New("unexpected source read")
			}}
			finished := make(chan error, 1)
			stopped := make(chan struct{})
			go func() { defer close(stopped); _, err := s.ScanMessages(ctx, r); finished <- err }()
			defer func() { unlock(); <-stopped }()
			if !deadline {
				timer := time.AfterFunc(100*time.Millisecond, cancel)
				defer timer.Stop()
			}
			select {
			case err := <-finished:
				if !errors.Is(err, want) {
					t.Fatalf("got %v, want %v", err, want)
				}
			case <-time.After(time.Second):
				t.Fatal("canceled Messages scan remained blocked on the held scan lock")
			}
			if read.Load() {
				t.Fatal("canceled lock waiter read source")
			}
		})
	}
}

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

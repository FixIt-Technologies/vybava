package operator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func indexedFixture(t *testing.T) (Store, State) {
	t.Helper()
	s := Store{Dir: t.TempDir()}
	if err := os.Chmod(s.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	var original State
	err := s.With(func(st *State) error {
		for i := 0; i < 300; i++ {
			id, _, err := st.Observe(Observation{Source: Claude, Key: "session", Revision: fmt.Sprint(i), Text: fmt.Sprintf("observation %d", i), ObservedAt: time.Unix(int64(i+1), 0).UTC()})
			if err != nil {
				return err
			}
			if i == 299 {
				if err = st.Propose(id, "review me"); err != nil {
					return err
				}
				if err = st.AddFeedback(id, 1, "because this matters"); err != nil {
					return err
				}
				if err = st.Rate(id, 1, 4, "keep the evidence"); err != nil {
					return err
				}
			}
		}
		st.Cursors["session"] = Cursor{Offset: 42, Prefix: "kept", PrefixSize: 4}
		st.Roots["root"] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.View(func(st *State) error { original = *st; return nil }); err != nil {
		t.Fatal(err)
	}
	return s, original
}

func TestIndexedMigrationPreservesArchiveAndPages(t *testing.T) {
	s, original := indexedFixture(t)
	raw, err := os.ReadFile(filepath.Join(s.Dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(filepath.Join(s.Dir, "state.pre-sqlite.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(backup) {
		t.Fatal("backup differs")
	}
	marker, err := os.ReadFile(filepath.Join(s.Dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var guard State
	if err = json.Unmarshal(marker, &guard); err != nil {
		t.Fatal(err)
	}
	if guard.Version != 5 {
		t.Fatal("old writers are not rejected")
	}
	original.Version = 5
	if err = s.View(func(st *State) error {
		if !reflect.DeepEqual(*st, original) {
			t.Fatal("migration changed history or metadata")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	page, err := s.History("", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 200 || page.Next != original.Events[100].ID {
		t.Fatalf("bad first page: %d %s", len(page.Events), page.Next)
	}
	// A concurrent append must not move the stable cursor's next page.
	if _, _, err = s.Observe(Observation{Source: Claude, Key: "other", Revision: "1", Text: "new"}); err != nil {
		t.Fatal(err)
	}
	next, err := s.History(page.Next, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Events) != 100 || next.Events[0].ID != original.Events[99].ID || next.Next != "" {
		t.Fatal("history page shifted")
	}
	snap, err := s.Snapshot("", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Events) != 1 || snap.Summary.Events != 301 || snap.Summary.DecisionsPending != 1 || snap.Summary.Average != 4 {
		t.Fatalf("snapshot %+v", snap.Summary)
	}
	if err = s.Migrate(); err == nil {
		t.Fatal("migration overwritten existing database")
	}
}

func TestIndexedHotPathsDoNotDecodeArchiveAndHeartbeatDoesNotInvalidate(t *testing.T) {
	s, original := indexedFixture(t)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	db, err := s.openDB()
	if err != nil {
		t.Fatal(err)
	}
	// An unreadable historical body is a deliberate probe: indexed hot paths
	// must never select it. Full archive export correctly detects it.
	if _, err = db.Exec("UPDATE events SET data='not JSON' WHERE id=?", original.Events[0].ID); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := s.Revision()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.scan(func(st *State) ([]string, error) {
		if len(st.Events) != 0 {
			t.Fatal("scanner loaded archive")
		}
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	after, err := s.Revision()
	if err != nil {
		t.Fatal(err)
	}
	if before.Revision != after.Revision || after.LastScanAt == nil {
		t.Fatal("heartbeat invalidated content or disappeared")
	}
	if _, err = s.History("", 20); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Snapshot("", 20); err != nil {
		t.Fatal(err)
	}
	if err = s.ViewQueue(func(st *State) error {
		if len(st.Events) != 1 {
			t.Fatalf("queue decoded %d events", len(st.Events))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = s.WithEvent(original.Events[299].ID, func(st *State) error { return st.Acknowledge(original.Events[299].ID) }); err != nil {
		t.Fatal(err)
	}
	changed, err := s.Revision()
	if err != nil {
		t.Fatal(err)
	}
	if changed.Revision == after.Revision {
		t.Fatal("actual change did not invalidate")
	}
	if err = s.View(func(*State) error { return nil }); err == nil {
		t.Fatal("full export ignored corruption")
	}
}

func TestIndexedConcurrentFeedbackAndScanPreserveBoth(t *testing.T) {
	s, original := indexedFixture(t)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	id := original.Events[len(original.Events)-1].ID
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs <- s.WithEvent(id, func(st *State) error { return st.AddFeedback(id, 1, "concurrent human feedback") })
	}()
	go func() {
		defer wg.Done()
		_, err := s.scan(func(st *State) ([]string, error) {
			newID, _, err := st.Observe(Observation{Source: Claude, Key: "session", Revision: "new", Text: "continued"})
			return []string{newID}, err
		})
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ViewEvent(id, func(st *State) error {
		e, err := st.Find(id)
		if err != nil {
			return err
		}
		if !e.Superseded || len(e.Proposals[0].Feedback) != 2 {
			t.Fatal("scan lost feedback or supersession")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIndexedMigrationRecoversGuardWithoutDatabase(t *testing.T) {
	s, original := indexedFixture(t)
	raw, err := os.ReadFile(filepath.Join(s.Dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(s.Dir, "state.pre-sqlite.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(s.Dir, "state.json"), []byte(`{"version":5,"storage":"state.sqlite"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(); err != nil {
		t.Fatal(err)
	}
	page, err := s.History("", 1)
	if err != nil {
		t.Fatal(err)
	}
	if page.Events[0].ID != original.Events[len(original.Events)-1].ID {
		t.Fatal("recovery lost history")
	}
}

func TestIndexedAttentionQueueExcludesArchiveButKeepsBackpressure(t *testing.T) {
	s, _ := indexedFixture(t)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	add := func(key string, at time.Time) string {
		t.Helper()
		id, _, err := s.Observe(Observation{Source: Claude, Key: key, Revision: "1", Text: "Claude requested a user decision: inspect", ObservedAt: at})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	recent := add("recent", now.Add(-time.Minute))
	add("old", now.Add(-time.Hour))
	add("unfinished", now.Add(-time.Second))
	if err := s.ViewAttentionQueue(now.Add(-time.Hour), now, func(st *State) error {
		if len(st.Events) != 1 || st.Events[0].ID != recent {
			t.Fatalf("unexpected attention queue %+v", st.Events)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	old := add("failed", now.Add(-time.Hour))
	if err := s.WithEvent(old, func(st *State) error {
		e, err := st.Find(old)
		if err == nil {
			e.Delivery = "failed"
			e.Superseded = true
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ViewAttentionQueue(now.Add(-time.Hour), now, func(st *State) error {
		if len(st.Events) != 2 || st.NextAttention(now.Add(-time.Hour), now) != "" {
			t.Fatal("ambiguous receipt lost backpressure")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIndexedQueueExcludesAcknowledgedCurrentEvents(t *testing.T) {
	s, original := indexedFixture(t)
	if err := s.With(func(st *State) error {
		for i := 0; i < 200; i++ {
			id, _, err := st.Observe(Observation{Source: Claude, Key: fmt.Sprintf("finished-%d", i), Revision: "1", Text: "finished"})
			if err != nil {
				return err
			}
			if err = st.Acknowledge(id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	id := original.Events[len(original.Events)-1].ID
	if err := s.WithEvent(id, func(st *State) error { return st.Acknowledge(id) }); err != nil {
		t.Fatal(err)
	}
	if err := s.ViewQueue(func(st *State) error {
		if len(st.Events) != 0 {
			t.Fatalf("completed current event remained in queue: %d", len(st.Events))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A human decision awaiting receipt remains actionable after event receipt.
	if err := s.WithEvent(id, func(st *State) error { return st.Decide(id, 1, "approve", "") }); err != nil {
		t.Fatal(err)
	}
	if err := s.ViewQueue(func(st *State) error {
		if len(st.Events) != 1 {
			t.Fatal("pending review disappeared")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIndexedSummaryUsesTransactionalCounters(t *testing.T) {
	s, original := indexedFixture(t)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	assertParity := func() {
		t.Helper()
		got, err := s.Summary()
		if err != nil {
			t.Fatal(err)
		}
		var want Summary
		if err = s.View(func(st *State) error { want = st.Summary(); return nil }); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("summary mismatch got %+v want %+v", got, want)
		}
	}
	assertParity()
	id := original.Events[len(original.Events)-1].ID
	if err := s.WithEvent(id, func(st *State) error {
		if err := st.Acknowledge(id); err != nil {
			return err
		}
		return st.Decide(id, 1, "approve", "")
	}); err != nil {
		t.Fatal(err)
	}
	assertParity()
	if _, _, err := s.Observe(Observation{Source: Claude, Key: "session", Revision: "next", Text: "new context"}); err != nil {
		t.Fatal(err)
	}
	assertParity()
	if err := s.WithEvent(id, func(st *State) error {
		e, err := st.Find(id)
		if err != nil {
			return err
		}
		e.Delivery = "failed"
		e.AcknowledgedAt = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertParity()
	// A summary read must be independent of archive rows and proposal decoding.
	before, err := s.Summary()
	if err != nil {
		t.Fatal(err)
	}
	db, err := s.openDB()
	if err != nil {
		t.Fatal(err)
	}
	// Counter deltas roll back with an interrupted event transaction.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	event := original.Events[len(original.Events)-1]
	event.Proposals = nil
	if _, err = saveEvent(tx, event); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	rolledBack, err := readSummary(db, summaryQuery)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, rolledBack.Summary) {
		t.Fatal("rolled back event changed counters")
	}
	if _, err = db.Exec("DROP TABLE events"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	after, err := s.Summary()
	if err != nil {
		t.Fatalf("summary read still depends on event archive: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("materialized summary changed without a journal update")
	}
}

package operator

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"time"

	_ "modernc.org/sqlite"
)

// SQLite stores one event per row: archive size does not determine the cost of
// reading a revision, looking up an event, or fetching a history page.
const sqliteSchema = `
PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS cursors (path TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS summary (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1), events INTEGER NOT NULL DEFAULT 0,
 pending INTEGER NOT NULL DEFAULT 0, queued INTEGER NOT NULL DEFAULT 0,
 acknowledged INTEGER NOT NULL DEFAULT 0, delivery_problems INTEGER NOT NULL DEFAULT 0,
 proposals INTEGER NOT NULL DEFAULT 0, scored INTEGER NOT NULL DEFAULT 0,
 score_total INTEGER NOT NULL DEFAULT 0, decisions_pending INTEGER NOT NULL DEFAULT 0);
INSERT OR IGNORE INTO summary(singleton) VALUES(1);
CREATE TABLE IF NOT EXISTS events (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE,
 source TEXT NOT NULL, source_key TEXT NOT NULL, superseded INTEGER NOT NULL,
 delivery TEXT NOT NULL, acknowledged INTEGER NOT NULL, proposals INTEGER NOT NULL,
 scored INTEGER NOT NULL, score_total INTEGER NOT NULL, review_pending INTEGER NOT NULL, decisions_pending INTEGER NOT NULL,
 attention INTEGER NOT NULL, observed_at INTEGER NOT NULL, data TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS events_source ON events(source,source_key,superseded);
CREATE INDEX IF NOT EXISTS events_proposals ON events(proposals) WHERE proposals>0;
CREATE INDEX IF NOT EXISTS events_queue ON events(superseded,acknowledged,delivery);
CREATE INDEX IF NOT EXISTS events_review ON events(review_pending) WHERE review_pending=1;
CREATE INDEX IF NOT EXISTS events_attention ON events(attention,superseded,acknowledged,delivery,observed_at);
INSERT OR IGNORE INTO meta VALUES('revision','0');
PRAGMA user_version=2;
`

func (s Store) Indexed() bool {
	_, err := os.Stat(filepath.Join(s.Dir, "state.sqlite"))
	return err == nil
}

func (s Store) openDB() (*sql.DB, error) {
	if err := s.prepare(); err != nil {
		return nil, err
	}
	path := filepath.Join(s.Dir, "state.sqlite")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("operator database must be a private regular file")
	}
	dsn := url.URL{Scheme: "file", Path: path, RawQuery: "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, err
	}
	if version != 2 {
		db.Close()
		return nil, errors.New("unsupported operator database schema")
	}
	return db, nil
}

type sqlQuery interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
	Exec(string, ...any) (sql.Result, error)
}

func loadRows(q sqlQuery, where string, args ...any) ([]Event, error) {
	rows, err := q.Query("SELECT data FROM events "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Event{}
	for rows.Next() {
		var data string
		var e Event
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &e); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}
func getMeta(q sqlQuery, key string, out any) error {
	var raw string
	err := q.QueryRow("SELECT value FROM meta WHERE key=?", key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), out)
}
func putMeta(q sqlQuery, key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = q.Exec("INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value WHERE value<>excluded.value", key, string(data))
	return err
}
func loadMetadata(q sqlQuery, cursors bool) (State, error) {
	state := State{Version: 5, Roots: map[string]bool{}, Cursors: map[string]Cursor{}}
	messageKey := "messages_status"
	if cursors {
		messageKey = "messages"
	}
	for key, out := range map[string]any{"roots": &state.Roots, "last_scan_at": &state.LastScanAt, messageKey: &state.Messages} {
		if err := getMeta(q, key, out); err != nil {
			return state, err
		}
	}
	if cursors {
		rows, err := q.Query("SELECT path,value FROM cursors")
		if err != nil {
			return state, err
		}
		defer rows.Close()
		for rows.Next() {
			var path, raw string
			var c Cursor
			if err = rows.Scan(&path, &raw); err != nil {
				return state, err
			}
			if err = json.Unmarshal([]byte(raw), &c); err != nil {
				return state, err
			}
			state.Cursors[path] = c
		}
		if err = rows.Err(); err != nil {
			return state, err
		}
	}
	return state, nil
}
func saveMetadata(q sqlQuery, before, after State) error {
	for key, pair := range map[string][2]any{"roots": {before.Roots, after.Roots}, "last_scan_at": {before.LastScanAt, after.LastScanAt}, "messages": {before.Messages, after.Messages}} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			if err := putMeta(q, key, pair[1]); err != nil {
				return err
			}
		}
	}
	if !reflect.DeepEqual(before.Messages, after.Messages) {
		var coverage []SourceCoverage
		if after.Messages != nil {
			coverage = append(coverage, after.Messages.Coverage)
		}
		if err := putMeta(q, "sources", coverage); err != nil {
			return err
		}
		var status *MessagesState
		if after.Messages != nil {
			status = &MessagesState{Database: after.Messages.Database, Initialized: after.Messages.Initialized, Coverage: after.Messages.Coverage}
		}
		if err := putMeta(q, "messages_status", status); err != nil {
			return err
		}
	}
	for path, c := range after.Cursors {
		if old, ok := before.Cursors[path]; ok && old == c {
			continue
		}
		raw, err := json.Marshal(c)
		if err != nil {
			return err
		}
		if _, err = q.Exec("INSERT INTO cursors VALUES(?,?) ON CONFLICT(path) DO UPDATE SET value=excluded.value", path, string(raw)); err != nil {
			return err
		}
	}
	for path := range before.Cursors {
		if _, ok := after.Cursors[path]; !ok {
			if _, err := q.Exec("DELETE FROM cursors WHERE path=?", path); err != nil {
				return err
			}
		}
	}
	return nil
}
func saveEvent(q sqlQuery, e Event) (bool, error) {
	// Read only the indexed row's scalar contribution. Both the event write and
	// its counter delta belong to the caller's transaction, including import.
	old, err := readSummary(q, `SELECT 1,(superseded=0 AND acknowledged=0 AND delivery='pending'),(acknowledged=0 AND delivery='queued'),acknowledged,(acknowledged=0 AND delivery IN ('submitting','failed')),proposals,scored,score_total,decisions_pending FROM events WHERE id=?`, e.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		old = summaryCounters{}
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return false, err
	}
	scored, total, review, decisions := 0, 0, false, 0
	for _, p := range e.Proposals {
		if !e.Superseded && !p.Superseded && p.Decision == nil {
			decisions++
		}
		if p.Rating != nil {
			scored++
			total += p.Rating.Score
		}
		if p.Decision != nil && p.Decision.AcknowledgedAt == nil {
			review = true
		}
	}
	result, err := q.Exec(`INSERT INTO events(id,source,source_key,superseded,delivery,acknowledged,proposals,scored,score_total,review_pending,decisions_pending,attention,observed_at,data) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET superseded=excluded.superseded,delivery=excluded.delivery,acknowledged=excluded.acknowledged,proposals=excluded.proposals,scored=excluded.scored,score_total=excluded.score_total,review_pending=excluded.review_pending,decisions_pending=excluded.decisions_pending,data=excluded.data WHERE data<>excluded.data`, e.ID, e.Source, e.Key, e.Superseded, e.Delivery, e.AcknowledgedAt != nil, len(e.Proposals), scored, total, review, decisions, attentionReason(e.Observation) != "", e.ObservedAt.UnixNano(), string(raw))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	current := summaryCounters{Summary: (&State{Events: []Event{e}}).Summary(), ScoreTotal: total}
	result, err = q.Exec(`UPDATE summary SET events=events+?,pending=pending+?,queued=queued+?,acknowledged=acknowledged+?,delivery_problems=delivery_problems+?,proposals=proposals+?,scored=scored+?,score_total=score_total+?,decisions_pending=decisions_pending+? WHERE singleton=1`, current.Events-old.Events, current.Pending-old.Pending, current.Queued-old.Queued, current.Acknowledged-old.Acknowledged, current.DeliveryProblems-old.DeliveryProblems, current.Proposals-old.Proposals, current.Scored-old.Scored, current.ScoreTotal-old.ScoreTotal, current.DecisionsPending-old.DecisionsPending)
	if err != nil {
		return false, err
	}
	n, err = result.RowsAffected()
	if err != nil {
		return false, err
	}
	if n != 1 {
		return false, errors.New("operator summary row is missing")
	}
	return true, nil
}
func bumpRevision(q sqlQuery) error {
	_, err := q.Exec("UPDATE meta SET value=CAST(value AS INTEGER)+1 WHERE key='revision'")
	return err
}

// Migrate explicitly imports the legacy archive. A byte-for-byte backup is kept
// before the version marker prevents old binaries from writing a split history.
// Call with all legacy watchers stopped. Never migrates implicitly during polls.
func (s Store) Migrate() error {
	if err := s.prepare(); err != nil {
		return err
	}
	unlock, err := lock(filepath.Join(s.Dir, "scan.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	unlockState, err := lock(filepath.Join(s.Dir, "state.lock"))
	if err != nil {
		return err
	}
	defer unlockState()
	if s.Indexed() {
		return errors.New("operator database already exists; migration not repeated")
	}
	state, original, err := s.load()
	backup := filepath.Join(s.Dir, "state.pre-sqlite.json")
	if err != nil {
		// A crash after publishing the guard but before the DB rename is
		// recoverable only from the retained immutable archive.
		marker, markerErr := readPrivateFile(filepath.Join(s.Dir, "state.json"))
		var guard struct {
			Version int    `json:"version"`
			Storage string `json:"storage"`
		}
		if markerErr != nil || json.Unmarshal(marker, &guard) != nil || guard.Version != 5 || guard.Storage != "state.sqlite" {
			return err
		}
		original, err = readPrivateFile(backup)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(original, &state); err != nil {
			return err
		}
		if state.Version < 1 || state.Version > 4 || state.Roots == nil || state.Cursors == nil {
			return errors.New("invalid migration recovery archive")
		}
		if state.Messages != nil && (state.Messages.Database == "" || state.Messages.Baseline < 0 || state.Messages.Records == nil || (state.Messages.Initialized && state.Messages.Baseline > 0 && state.Messages.AnchorGUID == "")) {
			return errors.New("invalid Messages migration recovery archive")
		}
	}
	if original != nil {
		f, err := os.OpenFile(backup, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if errors.Is(err, os.ErrExist) {
			retained, readErr := readPrivateFile(backup)
			if readErr != nil {
				return readErr
			}
			if !bytes.Equal(retained, original) {
				return errors.New("existing migration backup differs; refusing to overwrite it")
			}
		} else if err != nil {
			return err
		} else {
			_, writeErr := f.Write(original)
			syncErr := f.Sync()
			closeErr := f.Close()
			if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
				return err
			}
		}
	}
	temp, err := os.CreateTemp(s.Dir, ".sqlite-import-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	temp.Close()
	defer os.Remove(name)
	tempDSN := url.URL{Scheme: "file", Path: name}
	db, err := sql.Open("sqlite", tempDSN.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(sqliteSchema); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = saveMetadata(tx, State{}, state); err != nil {
		return err
	}
	for _, e := range state.Events {
		if _, err = saveEvent(tx, e); err != nil {
			return err
		}
	}
	if err = bumpRevision(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM events").Scan(&count); err != nil {
		return err
	}
	if count != len(state.Events) {
		return errors.New("migration event count mismatch")
	}
	var integrity string
	if err = db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return fmt.Errorf("migration integrity check: %s", integrity)
	}
	if err = db.Close(); err != nil {
		return err
	}
	// Publish the marker first: a crash here fails closed instead of permitting a
	// legacy writer to append after the indexed snapshot was taken.
	marker, err := os.CreateTemp(s.Dir, ".state-marker-*")
	if err != nil {
		return err
	}
	defer os.Remove(marker.Name())
	_, err = marker.WriteString("{\"version\":5,\"storage\":\"state.sqlite\"}\n")
	if err != nil {
		marker.Close()
		return err
	}
	if err = marker.Sync(); err != nil {
		marker.Close()
		return err
	}
	if err = marker.Close(); err != nil {
		return err
	}
	if err = os.Rename(marker.Name(), filepath.Join(s.Dir, "state.json")); err != nil {
		return err
	}
	if err = syncDirectory(s.Dir); err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(s.Dir, "state.sqlite")); err != nil {
		return err
	}
	dir, err := os.Open(s.Dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func readPrivateFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("migration archive must be a private regular file")
	}
	return os.ReadFile(path)
}
func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// indexedState is a compatibility adapter. Production callers use bounded
// selectors; View/With deliberately select the entire archive for export/tests.
func (s Store) indexedState(write bool, where string, args []any, fn func(*State) error) error {
	if write {
		unlock, err := lock(filepath.Join(s.Dir, "state.lock"))
		if err != nil {
			return err
		}
		defer unlock()
	}
	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	before, err := loadMetadata(tx, where == "ORDER BY seq")
	if err != nil {
		return err
	}
	before.Events, err = loadRows(tx, where, args...)
	if err != nil {
		return err
	}
	// Attention verification needs only the cursors belonging to selected events.
	if where != "ORDER BY seq" {
		for _, e := range before.Events {
			var raw string
			err = tx.QueryRow("SELECT value FROM cursors WHERE path=?", e.Key).Scan(&raw)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			var c Cursor
			if err = json.Unmarshal([]byte(raw), &c); err != nil {
				return err
			}
			before.Cursors[e.Key] = c
		}
	}
	if !write {
		if err = fn(&before); err != nil {
			return err
		}
		return tx.Commit()
	}
	raw, err := json.Marshal(before)
	if err != nil {
		return err
	}
	var current State
	if err = json.Unmarshal(raw, &current); err != nil {
		return err
	}
	if err = fn(&current); err != nil {
		return err
	}
	if err = saveMetadata(tx, before, current); err != nil {
		return err
	}
	changed := false
	for _, e := range current.Events {
		var update bool
		update, err = saveEvent(tx, e)
		if err != nil {
			return err
		}
		changed = changed || update
	}
	if changed {
		if err = bumpRevision(tx); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s Store) ViewEvent(id string, fn func(*State) error) error {
	if !s.Indexed() {
		return s.View(fn)
	}
	return s.indexedState(false, "WHERE id=?", []any{id}, fn)
}
func (s Store) WithEvent(id string, fn func(*State) error) error {
	if !s.Indexed() {
		return s.With(fn)
	}
	return s.indexedState(true, "WHERE id=?", []any{id}, fn)
}

const queueRows = "WHERE (superseded=0 AND acknowledged=0 AND delivery='pending') OR (acknowledged=0 AND delivery IN ('queued','submitting','failed')) OR review_pending=1 ORDER BY seq"

func (s Store) ViewQueue(fn func(*State) error) error {
	if !s.Indexed() {
		return s.View(fn)
	}
	return s.indexedState(false, queueRows, nil, fn)
}
func (s Store) WithQueue(fn func(*State) error) error {
	if !s.Indexed() {
		return s.With(fn)
	}
	return s.indexedState(true, queueRows, nil, fn)
}

type ContentRevision struct {
	Revision   string           `json:"revision"`
	LastScanAt *time.Time       `json:"last_scan_at,omitempty"`
	Sources    []SourceCoverage `json:"sources,omitempty"`
}

func (s Store) Revision() (ContentRevision, error) {
	var r ContentRevision
	if !s.Indexed() {
		return r, errors.New("operator revision requires explicit indexed migration")
	}
	db, err := s.openDB()
	if err != nil {
		return r, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	if err = tx.QueryRow("SELECT value FROM meta WHERE key='revision'").Scan(&r.Revision); err != nil {
		return r, err
	}
	if err = getMeta(tx, "last_scan_at", &r.LastScanAt); err != nil {
		return r, err
	}
	if err = getMeta(tx, "sources", &r.Sources); err != nil {
		return r, err
	}
	return r, tx.Commit()
}
func (s Store) History(before string, limit int) (HistoryPage, error) {
	r := HistoryPage{Events: []Event{}}
	if limit < 1 || limit > 200 {
		return r, errors.New("history limit must be 1–200")
	}
	if !s.Indexed() {
		err := s.View(func(st *State) error { var err error; r, err = st.History(before, limit); return err })
		return r, err
	}
	db, err := s.openDB()
	if err != nil {
		return r, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	clause := "ORDER BY seq DESC LIMIT ?"
	args := []any{limit + 1}
	if before != "" {
		var seq int64
		if err = tx.QueryRow("SELECT seq FROM events WHERE id=?", before).Scan(&seq); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return r, errors.New("history cursor does not exist")
			}
			return r, err
		}
		clause = "WHERE seq<? " + clause
		args = []any{seq, limit + 1}
	}
	r.Events, err = loadRows(tx, clause, args...)
	if err != nil {
		return r, err
	}
	if len(r.Events) > limit {
		r.Events = r.Events[:limit]
		r.Next = r.Events[limit-1].ID
	}
	return r, tx.Commit()
}
func (s Store) Snapshot(before string, limit int) (CompanionSnapshot, error) {
	if limit < 1 || limit > 200 {
		return CompanionSnapshot{}, errors.New("snapshot limit must be 1–200")
	}
	var r CompanionSnapshot
	if !s.Indexed() {
		err := s.View(func(st *State) error {
			r = st.Snapshot()
			r.Events = []Event{}
			end := len(st.Events)
			if before != "" {
				end = -1
				for i, e := range st.Events {
					if e.ID == before {
						end = i
						break
					}
				}
				if end < 0 {
					return errors.New("snapshot cursor does not exist")
				}
			}
			for i := end - 1; i >= 0; i-- {
				if len(st.Events[i].Proposals) == 0 {
					continue
				}
				if len(r.Events) == limit {
					r.Next = r.Events[len(r.Events)-1].ID
					break
				}
				r.Events = append(r.Events, st.Events[i])
			}
			return nil
		})
		return r, err
	}
	db, err := s.openDB()
	if err != nil {
		return r, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	r = CompanionSnapshot{Version: 1, CheckedAt: time.Now().UTC(), Capabilities: []string{"history", "review-decisions", "indexed-revision"}, Events: []Event{}}
	if err = getMeta(tx, "last_scan_at", &r.LastScanAt); err != nil {
		return r, err
	}
	if err = getMeta(tx, "sources", &r.Sources); err != nil {
		return r, err
	}
	if len(r.Sources) > 0 {
		r.Capabilities = append(r.Capabilities, "source-coverage")
	}
	clause := "WHERE proposals>0 ORDER BY seq DESC LIMIT ?"
	args := []any{limit + 1}
	if before != "" {
		var seq int64
		if err = tx.QueryRow("SELECT seq FROM events WHERE id=?", before).Scan(&seq); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return r, errors.New("snapshot cursor does not exist")
			}
			return r, err
		}
		clause = "WHERE proposals>0 AND seq<? ORDER BY seq DESC LIMIT ?"
		args = []any{seq, limit + 1}
	}
	r.Events, err = loadRows(tx, clause, args...)
	if err != nil {
		return r, err
	}
	if len(r.Events) > limit {
		r.Events = r.Events[:limit]
		r.Next = r.Events[limit-1].ID
	}
	counters, err := readSummary(tx, summaryQuery)
	if err != nil {
		return r, err
	}
	r.Summary = counters.Summary
	return r, tx.Commit()
}

// Observe inserts only this revision and supersedes indexed predecessors.
func (s Store) Observe(o Observation) (string, bool, error) {
	var id string
	var added bool
	if !s.Indexed() {
		err := s.With(func(st *State) error { var err error; id, added, err = st.Observe(o); return err })
		return id, added, err
	}
	err := s.indexedState(true, "WHERE id=? OR (source=? AND source_key=? AND superseded=0)", []any{observationID(o), o.Source, o.Key}, func(st *State) error { var err error; id, added, err = st.Observe(o); return err })
	return id, added, err
}

func observationID(o Observation) string {
	identity, _ := json.Marshal([]string{string(o.Source), o.Key, o.Revision})
	return digest(string(identity))[:24]
}
func (s Store) ViewMetadata(fn func(*State) error) error {
	if !s.Indexed() {
		return s.View(fn)
	}
	return s.indexedState(false, "WHERE 0", nil, fn)
}
func (s Store) Summary() (Summary, error) {
	if !s.Indexed() {
		var result Summary
		err := s.View(func(st *State) error { result = st.Summary(); return nil })
		return result, err
	}
	db, err := s.openDB()
	if err != nil {
		return Summary{}, err
	}
	defer db.Close()
	counters, err := readSummary(db, summaryQuery)
	return counters.Summary, err
}

func (s Store) scanIndexed(read func(*State) ([]string, error), updateScanTime bool) ([]string, error) {
	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	scanned, err := loadMetadata(db, true)
	if err != nil {
		return nil, err
	}
	// Metadata copy excludes all event bodies and human feedback.
	raw, err := json.Marshal(scanned)
	if err != nil {
		return nil, err
	}
	var before State
	if err = json.Unmarshal(raw, &before); err != nil {
		return nil, err
	}
	_, err = read(&scanned)
	if err != nil {
		return nil, err
	}
	unlock, err := lock(filepath.Join(s.Dir, "state.lock"))
	if err != nil {
		return nil, err
	}
	defer unlock()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	current, err := loadMetadata(tx, true)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(before.Roots, current.Roots) || !reflect.DeepEqual(before.Cursors, current.Cursors) {
		return nil, errors.New("scan cursors changed during read; stop older watchers before restarting")
	}
	added := []string{}
	changed := false
	for _, candidate := range scanned.Events {
		events, err := loadRows(tx, "WHERE id=? OR (source=? AND source_key=? AND superseded=0) ORDER BY seq", candidate.ID, candidate.Source, candidate.Key)
		if err != nil {
			return nil, err
		}
		st := State{Events: events}
		id, fresh, err := st.Observe(candidate.Observation)
		if err != nil {
			return nil, err
		}
		if !fresh {
			continue
		}
		added = append(added, id)
		for _, event := range st.Events {
			updated, err := saveEvent(tx, event)
			if err != nil {
				return nil, err
			}
			changed = changed || updated
		}
	}
	if updateScanTime {
		now := time.Now().UTC()
		scanned.LastScanAt = &now
	}
	if err = saveMetadata(tx, current, scanned); err != nil {
		return nil, err
	}
	if changed {
		if err = bumpRevision(tx); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return added, nil
}

// ViewAttentionQueue bounds the periodic delivery inspection to recent signals,
// while retaining all unacknowledged receipts for global backpressure.
func (s Store) ViewAttentionQueue(since, now time.Time, fn func(*State) error) error {
	return s.attentionQueue(false, since, now, fn)
}
func (s Store) WithAttentionQueue(since, now time.Time, fn func(*State) error) error {
	return s.attentionQueue(true, since, now, fn)
}
func (s Store) attentionQueue(write bool, since, now time.Time, fn func(*State) error) error {
	if !s.Indexed() {
		if write {
			return s.With(fn)
		}
		return s.View(fn)
	}
	earliest := now.Add(-10 * time.Minute)
	if since.After(earliest) {
		earliest = since
	}
	return s.indexedState(write, `WHERE id IN (
 SELECT id FROM events WHERE attention=1 AND superseded=0 AND acknowledged=0 AND delivery='pending' AND observed_at>? AND observed_at<=?
 UNION SELECT id FROM events WHERE acknowledged=0 AND delivery IN ('queued','submitting','failed')
 UNION SELECT id FROM events WHERE review_pending=1) ORDER BY seq`, []any{earliest.UnixNano(), now.Add(-20 * time.Second).UnixNano()}, fn)
}

func (s Store) ViewReviewQueue(fn func(*State) error) error {
	if !s.Indexed() {
		return s.View(fn)
	}
	return s.indexedState(false, "WHERE review_pending=1 ORDER BY seq", nil, fn)
}
func (s Store) WithReviewQueue(fn func(*State) error) error {
	if !s.Indexed() {
		return s.With(fn)
	}
	return s.indexedState(true, "WHERE review_pending=1 ORDER BY seq", nil, fn)
}

// summary is a singleton updated by saveEvent in the same write transaction.
// Polling reads nine scalars regardless of how much history has accumulated.
const summaryQuery = `SELECT events,pending,queued,acknowledged,delivery_problems,proposals,scored,score_total,decisions_pending FROM summary WHERE singleton=1`

type summaryCounters struct {
	Summary
	ScoreTotal int
}

func readSummary(q sqlQuery, query string, args ...any) (summaryCounters, error) {
	var r summaryCounters
	err := q.QueryRow(query, args...).Scan(&r.Events, &r.Pending, &r.Queued, &r.Acknowledged, &r.DeliveryProblems, &r.Proposals, &r.Scored, &r.ScoreTotal, &r.DecisionsPending)
	if err != nil {
		return r, err
	}
	r.Sending = "human-only"
	if r.Scored > 0 {
		r.Average = float64(r.ScoreTotal) / float64(r.Scored)
	}
	return r, nil
}

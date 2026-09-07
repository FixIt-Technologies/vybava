package operator

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"reflect"
	"time"
)

// Scan serializes scanners separately from human feedback and delivery writes.
// Only newly observed source data and scan cursors are merged at publication.
func (s Store) Scan(claudeRoot, codexRoot string, excluded []string) ([]string, error) {
	return s.scan(func(state *State) ([]string, error) {
		added, err := state.ScanClaude(claudeRoot)
		if err != nil {
			return nil, err
		}
		if codexRoot != "" {
			ids, err := state.ScanCodex(codexRoot, excluded)
			if err != nil {
				return nil, err
			}
			added = append(added, ids...)
		}
		return added, nil
	})
}

func (s Store) scan(read func(*State) ([]string, error)) ([]string, error) {
	return s.scanWithTime(context.Background(), read, true)
}

func (s Store) scanWithTime(ctx context.Context, read func(*State) ([]string, error), updateScanTime bool) ([]string, error) {
	if err := s.prepare(); err != nil {
		return nil, err
	}
	unlock, err := lockContext(ctx, filepath.Join(s.Dir, "scan.lock"))
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	scanned, _, err := s.load()
	if err != nil {
		return nil, err
	}
	roots, cursors := maps.Clone(scanned.Roots), maps.Clone(scanned.Cursors)
	count := len(scanned.Events)
	added, err := read(&scanned)
	if err != nil {
		return nil, err
	}
	err = s.With(func(current *State) error {
		if !reflect.DeepEqual(roots, current.Roots) || !reflect.DeepEqual(cursors, current.Cursors) {
			return errors.New("scan cursors changed during read; stop older watchers before restarting")
		}
		for _, event := range scanned.Events[count:] {
			if _, _, err := current.Observe(event.Observation); err != nil {
				return err
			}
		}
		current.Roots, current.Cursors = scanned.Roots, scanned.Cursors
		current.Messages = scanned.Messages
		if updateScanTime {
			now := time.Now().UTC()
			current.LastScanAt = &now
		}
		return nil
	})
	return added, err
}

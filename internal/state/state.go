package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const SchemaVersion = 1

type State struct {
	SchemaVersion int         `json:"schema_version"`
	Installed     []Installed `json:"installed"`
}

type Installed struct {
	ItemID      string    `json:"item_id"`
	Kind        string    `json:"kind"`
	Agent       string    `json:"agent,omitempty"`
	Scope       string    `json:"scope"`
	Destination string    `json:"destination"`
	InstalledAt time.Time `json:"installed_at"`
}

type Store struct {
	Path string
}

func DefaultStore() (Store, error) {
	root := os.Getenv("XDG_CONFIG_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Store{}, fmt.Errorf("resolve home: %w", err)
		}
		root = filepath.Join(home, ".config")
	}
	return Store{Path: filepath.Join(root, "vybava", "state.json")}, nil
}

func (s Store) Load() (State, error) {
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return State{SchemaVersion: SchemaVersion}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read state: %w", err)
	}
	var result State
	if err := json.Unmarshal(data, &result); err != nil {
		return State{}, fmt.Errorf("parse state: %w", err)
	}
	if result.SchemaVersion != SchemaVersion {
		return State{}, fmt.Errorf("unsupported state schema_version %d", result.SchemaVersion)
	}
	return result, nil
}

func (s Store) Save(value State) error {
	value.SchemaVersion = SchemaVersion
	sort.Slice(value.Installed, func(i, j int) bool {
		if value.Installed[i].ItemID == value.Installed[j].ItemID {
			return value.Installed[i].Destination < value.Installed[j].Destination
		}
		return value.Installed[i].ItemID < value.Installed[j].ItemID
	})
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.Path), "state-*.json")
	if err != nil {
		return fmt.Errorf("create temporary state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary state: %w", err)
	}
	if err := os.Rename(temporaryPath, s.Path); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	return nil
}

func Upsert(value *State, record Installed) {
	for i := range value.Installed {
		if value.Installed[i].ItemID == record.ItemID && value.Installed[i].Destination == record.Destination {
			value.Installed[i] = record
			return
		}
	}
	value.Installed = append(value.Installed, record)
}

func Remove(value *State, itemID, destination string) {
	kept := value.Installed[:0]
	for _, installed := range value.Installed {
		if installed.ItemID != itemID || installed.Destination != destination {
			kept = append(kept, installed)
		}
	}
	value.Installed = kept
}

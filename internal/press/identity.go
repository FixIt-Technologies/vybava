package press

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Identity is the issuing party behind every press deliverable: who signs the
// offer, whose registry identity goes in the header, what the house rate and
// brand tokens are.
//
// None of it lives in this repository. Výbava is public and an issuer's
// registry identity, commercial rate and brand are private business data, so
// the values live in a machine-local file outside any checkout —
// ~/.config/press/identity.json by default, PRESS_IDENTITY to override. The
// repository ships the shape and a placeholder scaffold, never the content.
//
// Skills read it through `press identity show --json` rather than hardcoding
// anything, which is also what makes the press family reusable by anyone who
// is not the author.
type Identity struct {
	Issuer     Issuer     `json:"issuer"`
	Commercial Commercial `json:"commercial"`
	Brand      Brand      `json:"brand"`
}

// Issuer is the supplier side of a commercial or legal document.
type Issuer struct {
	Name    string `json:"name"`
	ICO     string `json:"ico"`
	DIC     string `json:"dic"`
	Address string `json:"address"`
	DataBox string `json:"dataBox"`
	Email   string `json:"email"`
	Web     string `json:"web"`
}

// Commercial holds the default pricing language of an offer.
type Commercial struct {
	DayRate       float64 `json:"dayRate"`
	Currency      string  `json:"currency"`
	RateUnit      string  `json:"rateUnit"`
	VATNote       string  `json:"vatNote"`
	ValidityWeeks int     `json:"validityWeeks"`
}

// Brand is the house style shared by every generated document.
type Brand struct {
	Accent    string `json:"accent"`
	TableHead string `json:"tableHead"`
	Zebra     string `json:"zebra"`
	Hairline  string `json:"hairline"`
	Text      string `json:"text"`
	Muted     string `json:"muted"`
	Font      string `json:"font"`
}

// ErrNoIdentity is returned when no local identity file has been created yet.
var ErrNoIdentity = errors.New("no press identity configured — run `press identity init`, then fill in ~/.config/press/identity.json")

// IdentityPath is the machine-local identity file. It deliberately sits
// outside any git checkout so it cannot be committed by accident.
func IdentityPath() string {
	if v := os.Getenv("PRESS_IDENTITY"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "press", "identity.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "press", "identity.json")
}

// scaffold is what `press identity init` writes: the full shape with empty
// values, so a human knows exactly which fields to fill and no real value is
// ever sourced from this repository.
func scaffold() Identity {
	return Identity{
		Commercial: Commercial{Currency: "CZK", RateUnit: "člověkoden", ValidityWeeks: 6},
		Brand:      Brand{Font: "Arial"},
	}
}

// LoadIdentity reads the machine-local identity file.
func LoadIdentity() (Identity, error) {
	var id Identity
	b, err := os.ReadFile(IdentityPath())
	if errors.Is(err, os.ErrNotExist) {
		return id, ErrNoIdentity
	}
	if err != nil {
		return id, err
	}
	if err := json.Unmarshal(b, &id); err != nil {
		return id, fmt.Errorf("%s is not valid JSON: %w", IdentityPath(), err)
	}
	return id, nil
}

// InitIdentity writes the placeholder scaffold when no identity file exists.
// It never overwrites real values.
func InitIdentity() (path string, created bool, err error) {
	path = IdentityPath()
	if _, err := os.Stat(path); err == nil {
		return path, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return path, false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return path, false, fmt.Errorf("create identity home: %w", err)
	}
	b, err := json.MarshalIndent(scaffold(), "", "  ")
	if err != nil {
		return path, false, err
	}
	// 0600: this is business data, not world-readable configuration.
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return path, false, err
	}
	return path, true, nil
}

// MissingIdentityFields lists the fields a document generator needs but that
// the local file leaves empty, so a skill can fail loudly and specifically
// instead of rendering a document with blanks in the header.
func (i Identity) MissingIdentityFields() []string {
	var missing []string
	for _, f := range []struct {
		name  string
		value string
	}{
		{"issuer.name", i.Issuer.Name},
		{"issuer.ico", i.Issuer.ICO},
		{"issuer.address", i.Issuer.Address},
		{"brand.accent", i.Brand.Accent},
	} {
		if f.value == "" {
			missing = append(missing, f.name)
		}
	}
	if i.Commercial.DayRate == 0 {
		missing = append(missing, "commercial.dayRate")
	}
	return missing
}

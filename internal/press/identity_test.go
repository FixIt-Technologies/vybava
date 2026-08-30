package press

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIdentityPathHonoursOverride(t *testing.T) {
	t.Setenv("PRESS_IDENTITY", "/tmp/somewhere/identity.json")
	if got := IdentityPath(); got != "/tmp/somewhere/identity.json" {
		t.Fatalf("IdentityPath = %q, want the override", got)
	}
	t.Setenv("PRESS_IDENTITY", "")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	if got := IdentityPath(); got != filepath.Join("/tmp/xdg", "press", "identity.json") {
		t.Fatalf("IdentityPath = %q, want the XDG location", got)
	}
}

func TestLoadIdentityWithoutFile(t *testing.T) {
	t.Setenv("PRESS_IDENTITY", filepath.Join(t.TempDir(), "absent.json"))
	if _, err := LoadIdentity(); !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("err = %v, want ErrNoIdentity", err)
	}
}

func TestInitIdentityScaffoldsEmptyAndNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "identity.json")
	t.Setenv("PRESS_IDENTITY", path)

	got, created, err := InitIdentity()
	if err != nil || !created || got != path {
		t.Fatalf("init: path=%q created=%v err=%v", got, created, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("identity file mode = %v, want 0600 — it holds business data", perm)
	}

	// The scaffold must carry no values: this file ships from a public repo.
	identity, err := LoadIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if identity.Issuer.Name != "" || identity.Issuer.ICO != "" || identity.Commercial.DayRate != 0 {
		t.Fatalf("scaffold leaked real values: %+v", identity)
	}
	if missing := identity.MissingIdentityFields(); len(missing) == 0 {
		t.Fatal("an empty scaffold must report missing required fields")
	}

	if err := os.WriteFile(path, []byte(`{"issuer":{"name":"Acme"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, created, err := InitIdentity(); err != nil || created {
		t.Fatalf("init over an existing identity: created=%v err=%v", created, err)
	}
	identity, err = LoadIdentity()
	if err != nil || identity.Issuer.Name != "Acme" {
		t.Fatalf("init overwrote a real identity: %+v (%v)", identity, err)
	}
}

func TestMissingIdentityFieldsClearsWhenComplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.json")
	t.Setenv("PRESS_IDENTITY", path)
	full := Identity{
		Issuer: Issuer{Name: "Acme s.r.o.", ICO: "12345678", DIC: "CZ12345678", Address: "Somewhere 1, Praha"},
		Commercial: Commercial{
			DayRate: 10000, Currency: "CZK", RateUnit: "člověkoden",
			VATNote: "Ceny jsou uvedeny bez DPH.", ValidityWeeks: 6,
		},
		Brand: Brand{
			Accent: "0B5563", TableHead: "E8F1F2", Zebra: "F4F8F9",
			Hairline: "CDD7DB", Text: "232323", Muted: "5A6B70", Font: "Arial",
		},
	}
	b, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := LoadIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if missing := identity.MissingIdentityFields(); len(missing) != 0 {
		t.Fatalf("complete identity still reports %v", missing)
	}
	// dataBox, email and web stay optional — an issuer without a data box must
	// not be blocked from producing a document.
	if identity.Issuer.DataBox != "" || identity.Issuer.Email != "" || identity.Issuer.Web != "" {
		t.Fatal("this fixture deliberately leaves the optional fields empty")
	}
}

func TestLoadIdentityRejectsBrokenJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.json")
	t.Setenv("PRESS_IDENTITY", path)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadIdentity()
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("err = %v, want a clear JSON complaint", err)
	}
}

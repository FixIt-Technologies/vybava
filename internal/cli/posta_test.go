package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runPosta(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	command, err := (App{Stdout: &out, Stderr: &out}).Command("posta")
	if err != nil {
		t.Fatal(err)
	}
	command.SetArgs(args)
	err = command.Execute()
	return out.String(), err
}

// The mailbox identity always arrives from the environment, never a flag, so the
// applet cannot be pointed at the wrong account by a stray argument.
func withMailbox(t *testing.T) {
	t.Helper()
	t.Setenv("POSTA_ADDRESS", "someone@example.com")
	t.Setenv("POSTA_APP_PASSWORD", "injected-by-the-vault")
}

func TestPostaAddressHumanAndJSON(t *testing.T) {
	withMailbox(t)

	out, err := runPosta(t, "address", "--project", "FixIt", "--role", "Customer", "--run", "a1b2")
	if err != nil || strings.TrimSpace(out) != "someone+fixit-customer-a1b2@example.com" {
		t.Fatalf("human address = %q, %v", out, err)
	}

	out, err = runPosta(t, "address", "--project", "fixit", "--run", "a1b2", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var minted struct {
		Address string `json:"address"`
		Project string `json:"project"`
		Run     string `json:"run"`
	}
	if err := json.Unmarshal([]byte(out), &minted); err != nil {
		t.Fatalf("json address = %q: %v", out, err)
	}
	if minted.Address != "someone+fixit-a1b2@example.com" || minted.Run != "a1b2" {
		t.Fatalf("json address = %+v", minted)
	}

	// Omitting --run must mint a fresh id, so two runs of one journey never
	// collide on the same address.
	first, err := runPosta(t, "address", "--project", "fixit")
	if err != nil {
		t.Fatal(err)
	}
	second, err := runPosta(t, "address", "--project", "fixit")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("two mints produced the same address: %q", first)
	}
}

// --out is load-bearing: `onyx run_command` redacts a child's whole stdout once
// it injects a credential, so a result that only reached stdout is lost.
func TestPostaOutWritesTheResultToAFile(t *testing.T) {
	withMailbox(t)
	path := filepath.Join(t.TempDir(), "addr.json")

	out, err := runPosta(t, "address", "--project", "fixit", "--run", "z9", "--json", "--out", path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("stdout must stay empty when --out is set, got %q", out)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "someone+fixit-z9@example.com") {
		t.Fatalf("file = %q", written)
	}
}

func TestPostaRefusesAMailboxItCannotAuthenticate(t *testing.T) {
	t.Setenv("POSTA_ADDRESS", "someone@example.com")
	t.Setenv("POSTA_APP_PASSWORD", "")
	if _, err := runPosta(t, "address", "--project", "fixit"); err == nil {
		t.Fatal("a missing app password must fail before any address is minted")
	}
}

func TestParseSinceAcceptsDurationsAndTimestamps(t *testing.T) {
	before := time.Now()
	got, err := parseSince("10m")
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := before.Sub(got); elapsed < 9*time.Minute || elapsed > 11*time.Minute {
		t.Fatalf("--since 10m resolved %v before now", elapsed)
	}

	got, err = parseSince("2026-09-11T16:45:00Z")
	if err != nil || !got.Equal(time.Date(2026, 9, 11, 16, 45, 0, 0, time.UTC)) {
		t.Fatalf("RFC3339 since = %v, %v", got, err)
	}

	// "any" is the explicit no-cutoff form purge relies on.
	if got, err := parseSince("any"); err != nil || !got.IsZero() {
		t.Fatalf("any = %v, %v", got, err)
	}
	if _, err := parseSince("last tuesday"); err == nil {
		t.Fatal("an unparseable --since must be rejected, not silently ignored")
	}
}

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexsyncJSONLifecycle(t *testing.T) {
	for _, invokedAs := range []string{"codexsync", "vybava"} {
		t.Run(invokedAs, func(t *testing.T) {
			root := t.TempDir()
			claude := filepath.Join(root, "claude")
			if err := os.MkdirAll(filepath.Join(claude, "commands"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(claude, "commands", "tidy.md"), []byte("Tidy things.\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			run := func(verb string) (map[string]json.RawMessage, error) {
				t.Helper()
				var out bytes.Buffer
				cmd, err := (App{Stdout: &out, Stderr: &out}).Command(invokedAs)
				if err != nil {
					t.Fatal(err)
				}
				args := []string{verb, "--json", "--claude-home", claude, "--agents-home", filepath.Join(root, "agents"), "--codex-home", filepath.Join(root, "codex"), "--backup-root", filepath.Join(root, "backups")}
				if invokedAs == "vybava" {
					args = append([]string{"codexsync"}, args...)
				}
				cmd.SetArgs(args)
				err = cmd.Execute()
				var result map[string]json.RawMessage
				if parseErr := json.Unmarshal(out.Bytes(), &result); parseErr != nil {
					t.Fatalf("%s: invalid JSON %q: %v", verb, out.String(), parseErr)
				}
				return result, err
			}
			if result, err := run("plan"); err != nil || result["changes"] == nil || result["entries"] == nil {
				t.Fatalf("plan missing changes: %s, %v", result, err)
			}
			if result, err := run("check"); ExitCode(err) != 1 || string(result["status"]) != `"error"` || result["error"] == nil {
				t.Fatalf("drift did not produce JSON with failure exit: %s, %v", result, err)
			}
			if _, err := run("apply"); err != nil {
				t.Fatal(err)
			}
			if result, err := run("check"); err != nil || string(result["status"]) != `"ok"` {
				t.Fatalf("post-apply check failed: %s, %v", result, err)
			}
			if result, err := run("apply"); err != nil || string(result["written"]) != "[]" || string(result["manifest"]) != "false" {
				t.Fatalf("second apply was not a no-op: %s, %v", result, err)
			}
		})
	}
}

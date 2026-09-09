//go:build !windows

package envbridge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTransferSelectionAndExpiry(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "env.sock")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	secret := "synthetic'\"\\\n$(printf injected)"
	go func() {
		done <- Serve(ctx, socket, map[string]string{"TOKEN": secret, "OTHER": "second"}, func() error { close(ready); return nil })
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("server did not start: %v", err)
	}
	info, err := os.Stat(socket)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("socket permissions: %v %v", info, err)
	}
	got, err := Read(context.Background(), socket, []string{"TOKEN"})
	if err != nil || len(got) != 1 || got["TOKEN"] != secret {
		t.Fatalf("selection failed: %v", err)
	}
	if _, err := Read(context.Background(), socket, []string{"MISSING"}); err == nil {
		t.Fatal("unavailable key accepted")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not expire")
	}
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatalf("socket not removed: %v", err)
	}
	if _, err := Read(context.Background(), socket, []string{"TOKEN"}); err == nil {
		t.Fatal("expired server accepted a read")
	}
}

func TestPrivateDirectoryAndExistingPath(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	socket := filepath.Join(dir, "env.sock")
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := Serve(ctx, socket, map[string]string{"TOKEN": "synthetic"}, nil); err == nil {
		t.Fatal("public directory accepted")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(socket, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Serve(ctx, socket, map[string]string{"TOKEN": "synthetic"}, nil); err == nil {
		t.Fatal("existing path replaced")
	}
	data, err := os.ReadFile(socket)
	if err != nil || string(data) != "keep" {
		t.Fatal("existing file changed")
	}
}

func TestInputAndShellExportBoundary(t *testing.T) {
	for _, input := range []string{`{"TOKEN":`, `{"TOKEN":"\u0000"}`, `{"TOKEN":42}`} {
		if _, err := Input(strings.NewReader(input), []string{"TOKEN"}); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	if _, err := Input(strings.NewReader(`{"BAD;echo":"value"}`), []string{"BAD;echo"}); err == nil {
		t.Fatal("invalid key accepted")
	}
	values, err := Input(strings.NewReader(`{"TOKEN":"synthetic'\n$(printf injected)\\","OTHER":"discard"}`), []string{"TOKEN"})
	if err != nil || len(values) != 1 {
		t.Fatal("allowlist selection failed")
	}
	shell, err := ShellExports(values)
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("bash", "-c", shell+"\nprintf '%s' \"$TOKEN\"").Output()
	if err != nil || string(output) != values["TOKEN"] {
		t.Fatal("shell export changed the value or executed its contents")
	}
}

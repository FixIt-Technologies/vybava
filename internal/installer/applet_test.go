package installer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallAppletPreservesStableExecutableLink(t *testing.T) {
	root := t.TempDir()
	stableExecutable := filepath.Join(root, "bin", "vybava")
	versionOne := filepath.Join(root, "cask", "0.2.1", "vybava")
	versionTwo := filepath.Join(root, "cask", "0.2.2", "vybava")
	applet := filepath.Join(root, "local", "shrt")

	writeExecutable(t, versionOne)
	if err := os.MkdirAll(filepath.Dir(stableExecutable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(versionOne, stableExecutable); err != nil {
		t.Fatal(err)
	}
	if err := installAppletFrom(stableExecutable, applet); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(applet); err != nil || target != stableExecutable {
		t.Fatalf("applet target = %q, %v; want stable %q", target, err, stableExecutable)
	}

	writeExecutable(t, versionTwo)
	if err := os.Remove(stableExecutable); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(versionTwo, stableExecutable); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(applet)
	if err != nil {
		t.Fatalf("applet broke after cask target changed: %v", err)
	}
	resolvedVersionTwo, err := filepath.EvalSymlinks(versionTwo)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != resolvedVersionTwo {
		t.Fatalf("applet resolved to %q, want %q", resolved, resolvedVersionTwo)
	}
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
}

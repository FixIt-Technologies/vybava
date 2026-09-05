// Package ciinstall exercises ci/install.sh — the release installer every
// pipeline uses instead of checking this repository out — against a locally
// built archive, so the contract (checksum verification, binary placement,
// applet linking, failure modes) is proven without touching GitHub.
package ciinstall

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

// buildRelease compiles the real vybava binary and packs it exactly like the
// GoReleaser archive (vybava_<ver>_<os>_<arch>.tar.gz + checksums.txt).
func buildRelease(t *testing.T, version string) string {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "vybava")
	build := exec.Command("go", "build", "-o", binary, "./cmd/vybava")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	name := fmt.Sprintf("vybava_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
	tar := exec.Command("tar", "-czf", filepath.Join(dir, name), "-C", dir, "vybava")
	if out, err := tar.CombinedOutput(); err != nil {
		t.Fatalf("tar: %v\n%s", err, out)
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	// A second, unrelated line proves the installer picks ITS asset's line.
	checksums := fmt.Sprintf("%s  vybava_%s_windows_amd64.zip\n%s  %s\n",
		strings.Repeat("0", 64), version, hex.EncodeToString(sum[:]), name)
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(checksums), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runInstaller(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{filepath.Join(repoRoot(t), "ci", "install.sh")}, args...)...)
	// Skills land in the agent home and `vybava install` records state under
	// XDG_CONFIG_HOME (else HOME); point both at scratch dirs so a developer's
	// real homes are never touched.
	scratch := t.TempDir()
	cmd.Env = append(os.Environ(), "HOME="+scratch, "XDG_CONFIG_HOME="+filepath.Join(scratch, ".config"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestInstallsBinaryAndLinksApplets(t *testing.T) {
	release := buildRelease(t, "9.9.9")
	binDir := filepath.Join(t.TempDir(), "bin")
	out, err := runInstaller(t, "--from-dir", release, "--bin-dir", binDir, "--install", "memorylint,hotfix")
	if err != nil {
		t.Fatalf("installer failed: %v\n%s", err, out)
	}
	for _, name := range []string{"vybava", "memorylint", "hotfix"} {
		if _, err := os.Stat(filepath.Join(binDir, name)); err != nil {
			t.Fatalf("%s missing from bin dir: %v\n%s", name, err, out)
		}
	}
	// The applet dispatches on argv[0]: `memorylint --help` must be the applet,
	// not the root command (this is what FixIt's hooks and CI rely on).
	help, _ := exec.Command(filepath.Join(binDir, "memorylint"), "--help").CombinedOutput()
	if !strings.Contains(string(help), "hook") {
		t.Fatalf("memorylint link does not dispatch as the applet:\n%s", help)
	}
	if !strings.Contains(out, "(+ memorylint,hotfix)") {
		t.Fatalf("summary line lacks the installed items:\n%s", out)
	}
}

func TestBinaryOnlyWhenNoItemsRequested(t *testing.T) {
	release := buildRelease(t, "9.9.9")
	binDir := filepath.Join(t.TempDir(), "bin")
	out, err := runInstaller(t, "--from-dir", release, "--bin-dir", binDir)
	if err != nil {
		t.Fatalf("installer failed: %v\n%s", err, out)
	}
	entries, _ := os.ReadDir(binDir)
	if len(entries) != 1 || entries[0].Name() != "vybava" {
		t.Fatalf("expected only vybava in bin dir, got %v", entries)
	}
}

func TestRefusesChecksumMismatch(t *testing.T) {
	release := buildRelease(t, "9.9.9")
	sums := filepath.Join(release, "checksums.txt")
	data, err := os.ReadFile(sums)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the asset name, replace its digest: the archive itself is intact,
	// so only the verification step can catch this.
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for i, line := range lines {
		if strings.HasSuffix(line, ".tar.gz") {
			lines[i] = strings.Repeat("a", 64) + line[64:]
		}
	}
	if err := os.WriteFile(sums, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(t.TempDir(), "bin")
	out, err := runInstaller(t, "--from-dir", release, "--bin-dir", binDir)
	if err == nil {
		t.Fatalf("installer accepted a tampered checksum:\n%s", out)
	}
	if !strings.Contains(out, "checksum mismatch") {
		t.Fatalf("unexpected failure text:\n%s", out)
	}
	if _, statErr := os.Stat(filepath.Join(binDir, "vybava")); statErr == nil {
		t.Fatal("binary installed despite checksum failure")
	}
}

func TestRequiresVersionOrLocalDir(t *testing.T) {
	out, err := runInstaller(t, "--bin-dir", t.TempDir())
	if err == nil || !strings.Contains(out, "--version is required") {
		t.Fatalf("missing --version not refused: %v\n%s", err, out)
	}
	out, err = runInstaller(t, "--from-dir", t.TempDir(), "--agent", "emacs")
	if err == nil || !strings.Contains(out, "--agent must be") {
		t.Fatalf("bad --agent not refused: %v\n%s", err, out)
	}
}

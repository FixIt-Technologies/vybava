package pwmcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testConfig builds a Config over a throwaway tree.
func testConfig(t *testing.T) Config {
	t.Helper()
	base := t.TempDir()
	return Config{
		Root:     filepath.Join(base, "playwright-mcp"),
		Browsers: filepath.Join(base, "playwright-browsers"),
		Version:  "0.0.78",
		Runtime:  "bun",
	}
}

// writeCore fakes an installed pin: the server entrypoint plus the browsers.json
// that decides which revisions are required.
func writeCore(t *testing.T, c Config, browsers string) {
	t.Helper()
	server := c.ServerCLI()
	if err := os.MkdirAll(filepath.Dir(server), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(server, []byte("// stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.CoreDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.CoreDir(), "browsers.json"), []byte(browsers), 0o644); err != nil {
		t.Fatal(err)
	}
}

const coreManifest = `{"browsers":[
  {"name":"chromium","revision":"1232","installByDefault":true},
  {"name":"chromium-headless-shell","revision":"1232","installByDefault":true},
  {"name":"ffmpeg","revision":"1011","installByDefault":true},
  {"name":"webkit","revision":"2327","installByDefault":true}
]}`

// installBrowser marks a revision directory as fully installed.
func installBrowser(t *testing.T, c Config, directory string) {
	t.Helper()
	path := filepath.Join(c.Browsers, directory)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "INSTALLATION_COMPLETE"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIsolateInjectsByDefault(t *testing.T) {
	args, isolated := Isolate([]string{"--headless", "--output-dir", ".playwright"})
	if !isolated {
		t.Fatal("expected isolation to be injected")
	}
	if args[0] != "--isolated" {
		t.Fatalf("expected --isolated first, got %v", args)
	}
	if len(args) != 4 {
		t.Fatalf("passthrough args were altered: %v", args)
	}
}

// A config that chooses its own profile must be left alone: Chromium only loads
// an unpacked extension into a persistent profile, so overriding it would break
// exactly the setups that need a persistent one.
func TestIsolateRespectsExplicitProfileChoice(t *testing.T) {
	for _, arg := range []string{"--isolated", "--user-data-dir", "--user-data-dir=/tmp/p", "--config=/tmp/c.json", "--storage-state"} {
		args, isolated := Isolate([]string{arg, "--headless"})
		if isolated {
			t.Fatalf("%s: isolation should not have been injected", arg)
		}
		if len(args) != 2 || args[0] != arg {
			t.Fatalf("%s: args altered: %v", arg, args)
		}
	}
}

func TestIsolateSharedProfileFlagIsConsumed(t *testing.T) {
	args, isolated := Isolate([]string{"--headless", SharedProfileFlag})
	if isolated {
		t.Fatal("--shared-profile should suppress isolation")
	}
	for _, arg := range args {
		if arg == SharedProfileFlag {
			t.Fatal("--shared-profile must not reach the server")
		}
	}
	if len(args) != 1 || args[0] != "--headless" {
		t.Fatalf("unexpected passthrough: %v", args)
	}
}

// An inherited PLAYWRIGHT_BROWSERS_PATH is how a second registry gets created,
// so it must be replaced rather than left in place.
func TestEnvOverridesInheritedBrowsersPath(t *testing.T) {
	t.Setenv("PLAYWRIGHT_BROWSERS_PATH", "/somewhere/else")
	c := testConfig(t)
	var seen []string
	for _, entry := range c.Env() {
		if strings.HasPrefix(entry, "PLAYWRIGHT_BROWSERS_PATH=") {
			seen = append(seen, entry)
		}
	}
	if len(seen) != 1 {
		t.Fatalf("expected exactly one browsers path, got %v", seen)
	}
	if seen[0] != "PLAYWRIGHT_BROWSERS_PATH="+c.Browsers {
		t.Fatalf("inherited value survived: %s", seen[0])
	}
}

func TestMissingUsesCompletionMarker(t *testing.T) {
	c := testConfig(t)
	writeCore(t, c, coreManifest)
	installBrowser(t, c, "chromium-1232")
	// A directory without the marker is a half-finished download.
	if err := os.MkdirAll(filepath.Join(c.Browsers, "ffmpeg-1011"), 0o755); err != nil {
		t.Fatal(err)
	}

	missing, err := c.Missing([]string{"chromium", "chromium-headless-shell", "ffmpeg"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"chromium-headless-shell", "ffmpeg"}
	if strings.Join(missing, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", missing, want)
	}
}

func TestRequiredRejectsUnknownBrowser(t *testing.T) {
	c := testConfig(t)
	writeCore(t, c, coreManifest)
	if _, err := c.Required([]string{"chromium", "netscape"}); err == nil {
		t.Fatal("expected an error for an unknown browser name")
	}
}

func TestRequiredFoldsDashesToUnderscores(t *testing.T) {
	c := testConfig(t)
	writeCore(t, c, coreManifest)
	required, err := c.Required([]string{"chromium-headless-shell"})
	if err != nil {
		t.Fatal(err)
	}
	if got := required["chromium-headless-shell"]; got != "chromium_headless_shell-1232" {
		t.Fatalf("got %q", got)
	}
}

func TestPruneKeepsEveryInstalledPin(t *testing.T) {
	c := testConfig(t)
	writeCore(t, c, coreManifest)
	// An older pin still on disk keeps its own revisions alive, so rolling back
	// does not mean re-downloading.
	older := c
	older.Version = "0.0.70"
	writeCore(t, older, `{"browsers":[{"name":"chromium","revision":"1208","installByDefault":true}]}`)

	installBrowser(t, c, "chromium-1232")
	installBrowser(t, c, "chromium-1208")
	installBrowser(t, c, "chromium-1100")
	installBrowser(t, c, "webkit-2327")

	removed, err := c.Prune(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || filepath.Base(removed[0]) != "chromium-1100" {
		t.Fatalf("unexpected prune result: %v", removed)
	}
	for _, kept := range []string{"chromium-1232", "chromium-1208", "webkit-2327"} {
		if !exists(filepath.Join(c.Browsers, kept)) {
			t.Fatalf("%s should have been kept", kept)
		}
	}
}

// Bookkeeping entries do not match the <name>-<revision> shape and must survive.
func TestPruneLeavesNonRevisionEntriesAlone(t *testing.T) {
	c := testConfig(t)
	writeCore(t, c, coreManifest)
	installBrowser(t, c, "chromium-1232")
	for _, name := range []string{".links", "__dirlock", "b"} {
		if err := os.MkdirAll(filepath.Join(c.Browsers, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := c.Prune(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 {
		t.Fatalf("expected nothing removed, got %v", removed)
	}
	for _, name := range []string{".links", "__dirlock", "b"} {
		if !exists(filepath.Join(c.Browsers, name)) {
			t.Fatalf("%s was deleted", name)
		}
	}
}

// Deleting on the basis of an unreadable install tree would wipe browsers that
// are actually in use, so a blind prune has to refuse.
func TestPruneRefusesWithoutAReadablePin(t *testing.T) {
	c := testConfig(t)
	installBrowser(t, c, "chromium-1232")
	if _, err := c.Prune(true); err == nil {
		t.Fatal("expected a refusal when no pin could be read")
	}
	if !exists(filepath.Join(c.Browsers, "chromium-1232")) {
		t.Fatal("browsers must survive a refused prune")
	}
}

func TestPruneDryRunDeletesNothing(t *testing.T) {
	c := testConfig(t)
	writeCore(t, c, coreManifest)
	installBrowser(t, c, "chromium-1100")
	removed, err := c.Prune(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 {
		t.Fatalf("expected one orphan reported, got %v", removed)
	}
	if !exists(filepath.Join(c.Browsers, "chromium-1100")) {
		t.Fatal("dry run deleted the directory")
	}
}

func TestEnsureIsSkippedWhenInstalled(t *testing.T) {
	c := testConfig(t)
	writeCore(t, c, coreManifest)
	called := false
	run := func(context.Context, string, []string, string, ...string) error {
		called = true
		return nil
	}
	if err := c.Ensure(context.Background(), run, io.Discard); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("an installed pin must not reinstall")
	}
}

func TestEnsureWritesThePinnedManifest(t *testing.T) {
	c := testConfig(t)
	// The runner stands in for `bun install`, producing what it would produce.
	run := func(_ context.Context, dir string, env []string, _ string, _ ...string) error {
		if dir != c.PackageDir() {
			t.Fatalf("installed in %s", dir)
		}
		var found bool
		for _, entry := range env {
			if entry == "PLAYWRIGHT_BROWSERS_PATH="+c.Browsers {
				found = true
			}
		}
		if !found {
			t.Fatal("install ran without the shared registry in its environment")
		}
		writeCore(t, c, coreManifest)
		return nil
	}
	if err := c.Ensure(context.Background(), run, io.Discard); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(c.PackageDir(), "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if got := manifest.Dependencies["@playwright/mcp"]; got != c.Version {
		t.Fatalf("pin is %q, want %q", got, c.Version)
	}
}

// An install that reports success but leaves no entrypoint must fail loudly,
// not hand a broken path to Serve.
func TestEnsureFailsWhenTheEntrypointIsAbsent(t *testing.T) {
	c := testConfig(t)
	run := func(context.Context, string, []string, string, ...string) error { return nil }
	err := c.Ensure(context.Background(), run, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("got %v", err)
	}
}

func TestEnsureBrowsersOnlyRequestsWhatIsMissing(t *testing.T) {
	c := testConfig(t)
	writeCore(t, c, coreManifest)
	installBrowser(t, c, "chromium-1232")
	var requested []string
	run := func(_ context.Context, _ string, _ []string, _ string, args ...string) error {
		requested = args[2:] // skip the core cli path and "install"
		return nil
	}
	if err := c.EnsureBrowsers(context.Background(), run, io.Discard, DefaultBrowsers); err != nil {
		t.Fatal(err)
	}
	want := "chromium-headless-shell,ffmpeg"
	if strings.Join(requested, ",") != want {
		t.Fatalf("got %v, want %s", requested, want)
	}
}

func TestEnsureBrowsersIsANoOpWhenComplete(t *testing.T) {
	c := testConfig(t)
	writeCore(t, c, coreManifest)
	installBrowser(t, c, "chromium-1232")
	installBrowser(t, c, "chromium_headless_shell-1232")
	installBrowser(t, c, "ffmpeg-1011")
	run := func(context.Context, string, []string, string, ...string) error {
		t.Fatal("nothing should have been downloaded")
		return nil
	}
	if err := c.EnsureBrowsers(context.Background(), run, io.Discard, nil); err != nil {
		t.Fatal(err)
	}
}

func TestLockIsExclusiveAndReleasable(t *testing.T) {
	c := testConfig(t)
	release, err := c.lock(context.Background(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	// A live holder must not be stolen: that is what turns two installs into a
	// corrupted registry.
	current, age := readHolder(filepath.Join(c.Root, "install.lock"))
	if current.PID != os.Getpid() || stale(current, age) {
		t.Fatalf("own live lock read back as stale: %+v", current)
	}
	release()
	if exists(filepath.Join(c.Root, "install.lock")) {
		t.Fatal("release left the lock behind")
	}
}

// The failure this package exists to fix: a crashed install leaves a lock that
// blocks every later session forever. A dead owner must be reclaimed.
func TestLockReclaimsADeadHolder(t *testing.T) {
	c := testConfig(t)
	if err := os.MkdirAll(c.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	dead, _ := json.Marshal(holder{PID: 1 << 30, Started: time.Now().UTC(), Command: "crashed"})
	if err := os.WriteFile(filepath.Join(c.Root, "install.lock"), dead, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		release, err := c.lock(context.Background(), io.Discard)
		if release != nil {
			release()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stale lock was not reclaimed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("blocked on a lock whose owner is gone")
	}
}

func TestLockHonoursContextCancellation(t *testing.T) {
	c := testConfig(t)
	release, err := c.lock(context.Background(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.lock(ctx, io.Discard); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want a deadline error", err)
	}
}

func TestStatusFlagsASecondRegistry(t *testing.T) {
	c := testConfig(t)
	writeCore(t, c, coreManifest)
	installBrowser(t, c, "chromium-1232")
	installBrowser(t, c, "chromium_headless_shell-1232")
	installBrowser(t, c, "ffmpeg-1011")
	report := c.Status(nil)
	if !report.ServerInstalls {
		t.Fatal("server should read as installed")
	}
	if len(report.Missing) != 0 {
		t.Fatalf("unexpected missing browsers: %v", report.Missing)
	}
	if len(report.Present) != 3 {
		t.Fatalf("expected three present browsers, got %v", report.Present)
	}
}

func TestDefaultConfigKeepsTheRegistryOutOfTheOSCache(t *testing.T) {
	t.Setenv("VYBAVA_PWMCP_ROOT", "")
	t.Setenv("VYBAVA_PWMCP_BROWSERS", "")
	t.Setenv("PLAYWRIGHT_BROWSERS_PATH", "")
	c, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.Browsers, filepath.Join("Library", "Caches")) {
		t.Fatalf("registry defaults into the OS cache: %s", c.Browsers)
	}
	if c.Version != PinnedVersion {
		t.Fatalf("default version is %q, want the pin %q", c.Version, PinnedVersion)
	}
}

func TestDefaultConfigHonoursAnExplicitRegistry(t *testing.T) {
	t.Setenv("VYBAVA_PWMCP_BROWSERS", "")
	t.Setenv("PLAYWRIGHT_BROWSERS_PATH", "/opt/pw")
	c, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.Browsers != "/opt/pw" {
		t.Fatalf("got %q", c.Browsers)
	}
}

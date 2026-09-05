package reclaim

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func write(t *testing.T, path string, size int, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if age > 0 {
		when := time.Now().Add(-age)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}
}

type fakeEnv struct {
	Env
	mu    sync.Mutex
	free  int64
	calls []string
}

func newFakeEnv(t *testing.T, home string, free int64) *fakeEnv {
	f := &fakeEnv{free: free}
	f.Env = Env{
		Home: home, Volume: home, Now: time.Now(), GOOS: "darwin",
		LookPath: func(name string) (string, error) {
			if name == "docker" || name == "xcrun" {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("missing")
		},
		Free: func(string) (int64, int64, error) { f.mu.Lock(); defer f.mu.Unlock(); return f.free, 1 << 40, nil },
		Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.calls = append(f.calls, name+" "+strings.Join(args, " "))
			if name == "docker" && args[0] == "builder" {
				f.free += 10 << 30
				return []byte("Total reclaimed space: 24.4GB\n"), nil
			}
			return nil, nil
		},
		Stderr: func(string) {},
	}
	return f
}

func TestRemoveTreeAccountsAndUnlocks(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a/b/one"), 100, 0)
	write(t, filepath.Join(root, "a/two"), 50, 0)
	if err := os.Chmod(filepath.Join(root, "a/two"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "a/b"), 0o500); err != nil {
		t.Fatal(err)
	}
	n, err := removeTree(context.Background(), filepath.Join(root, "a"), false)
	if err != nil {
		t.Fatalf("removeTree: %v", err)
	}
	if n != 150 {
		t.Fatalf("bytes = %d, want 150", n)
	}
	if _, err := os.Stat(filepath.Join(root, "a")); !os.IsNotExist(err) {
		t.Fatal("tree should be gone")
	}
}

func TestRemoveTreeDryRunLeavesFiles(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "x/f"), 7, 0)
	n, err := removeTree(context.Background(), filepath.Join(root, "x"), true)
	if err != nil || n != 7 {
		t.Fatalf("dry: n=%d err=%v", n, err)
	}
	if _, err := os.Stat(filepath.Join(root, "x/f")); err != nil {
		t.Fatal("dry run must not delete")
	}
}

func TestRemoveAgedKeepsRecentAndTree(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "tmp/old.mov"), 1000, 90*24*time.Hour)
	write(t, filepath.Join(root, "tmp/new.mov"), 10, time.Hour)
	n, err := removeAged(context.Background(), filepath.Join(root, "tmp"), time.Now().AddDate(0, 0, -60), false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1000 {
		t.Fatalf("aged bytes = %d, want 1000", n)
	}
	if _, err := os.Stat(filepath.Join(root, "tmp/new.mov")); err != nil {
		t.Fatal("recent file must survive")
	}
	if _, err := os.Stat(filepath.Join(root, "tmp")); err != nil {
		t.Fatal("the tree itself must survive")
	}
}

func TestPlanRespectsTierOnlySkip(t *testing.T) {
	env := Env{GOOS: "darwin"}
	for _, s := range Plan(env, Options{MaxTier: TierCaches}) {
		if s.Tier > TierCaches {
			t.Fatalf("tier %d leaked into a --tier 2 plan: %s", s.Tier, s.ID)
		}
	}
	only := Plan(env, Options{Only: []string{"trash,go-build"}})
	if len(only) != 2 {
		t.Fatalf("only: got %d steps", len(only))
	}
	for _, s := range Plan(env, Options{Skip: []string{"go-build"}}) {
		if s.ID == "go-build" {
			t.Fatal("skip ignored")
		}
	}
	ids := map[string]bool{}
	for _, s := range Ladder(env) {
		if ids[s.ID] {
			t.Fatalf("duplicate step id %s", s.ID)
		}
		ids[s.ID] = true
		if s.Tier < TierBulk || s.Tier > TierAggressive {
			t.Fatalf("%s: tier %d out of ladder", s.ID, s.Tier)
		}
		if s.Regenerates == "" {
			t.Fatalf("%s: every step says what regenerates it", s.ID)
		}
		if len(s.Paths) == 0 && s.Run == nil {
			t.Fatalf("%s: neither paths nor a run func", s.ID)
		}
	}
	if len(Ladder(Env{GOOS: "linux"})) >= len(Ladder(env)) {
		t.Fatal("linux ladder must drop the mac-only steps")
	}
}

func TestLadderNeverNamesVolumesOrUserData(t *testing.T) {
	for _, s := range Ladder(Env{GOOS: "darwin"}) {
		for _, p := range s.Paths {
			if strings.Contains(p, "ScreenRecordings") || strings.Contains(p, "Messages/Attachments") || strings.Contains(p, "ms-playwright") && !strings.Contains(p, "ms-playwright-mcp") {
				t.Fatalf("%s names user data or the shared browser store: %s", s.ID, p)
			}
			if !strings.HasPrefix(p, "~/") {
				t.Fatalf("%s: path %q must be home-relative", s.ID, p)
			}
		}
	}
}

func TestRunDeletesInTierOrderAndReportsFree(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "Library/Caches/go-build/obj"), 4096, 0)
	write(t, filepath.Join(home, ".Trash/junk"), 512, 0)
	env := newFakeEnv(t, home, 1<<30)
	var seen []Result
	rep, err := Run(context.Background(), env.Env, Options{}, progressFunc(func(r Result) { seen = append(seen, r) }))
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Result{}
	for _, r := range rep.Results {
		byID[r.ID] = r
	}
	if r := byID["go-build"]; r.Status != StatusDone || r.Bytes != 4096 {
		t.Fatalf("go-build: %+v", r)
	}
	if r := byID["docker-builder"]; r.Status != StatusDone || r.Bytes != 24_400_000_000 {
		t.Fatalf("docker-builder should parse the reclaimed line: %+v", r)
	}
	if r := byID["trash"]; r.Status != StatusDone || r.Bytes != 512 {
		t.Fatalf("trash: %+v", r)
	}
	if r := byID["brew"]; r.Status != StatusSkipped || !strings.Contains(r.Reason, "brew") {
		t.Fatalf("missing binary must skip, not fail: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(home, "Library/Caches/go-build")); !os.IsNotExist(err) {
		t.Fatal("go-build not removed")
	}
	if rep.Freed() != 10<<30 {
		t.Fatalf("Freed must be the df delta, got %d", rep.Freed())
	}
	for i := 1; i < len(seen); i++ {
		if seen[i].Tier < seen[i-1].Tier {
			t.Fatalf("tier %d reported after tier %d", seen[i].Tier, seen[i-1].Tier)
		}
	}
}

func TestRunStopsAtTarget(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".Trash/junk"), 512, 0)
	env := newFakeEnv(t, home, 1<<30)
	rep, err := Run(context.Background(), env.Env, Options{Until: 5 << 30}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Reached {
		t.Fatal("target should be reached after the docker prune bumped free space")
	}
	var trash Result
	for _, r := range rep.Results {
		if r.ID == "trash" {
			trash = r
		}
	}
	if trash.Status != StatusSkipped {
		t.Fatalf("tier 3 must not run once the target is met: %+v", trash)
	}
	if _, err := os.Stat(filepath.Join(home, ".Trash/junk")); err != nil {
		t.Fatal("trash was deleted after the target was met")
	}
}

func TestDryRunDeletesNothing(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "Library/Caches/go-build/obj"), 4096, 0)
	env := newFakeEnv(t, home, 1<<30)
	rep, err := Run(context.Background(), env.Env, Options{DryRun: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "Library/Caches/go-build/obj")); err != nil {
		t.Fatal("dry run deleted")
	}
	for _, r := range rep.Results {
		if r.Status == StatusDone {
			t.Fatalf("dry run reported done: %+v", r)
		}
	}
	for _, c := range env.calls {
		if strings.Contains(c, "prune") || strings.Contains(c, "delete") {
			t.Fatalf("dry run executed %q", c)
		}
	}
}

func TestUnusedRuntimes(t *testing.T) {
	runtimes := []byte(`{
	  "A": {"identifier":"A","version":"18.2","platformIdentifier":"iOS","sizeBytes":8000000000},
	  "B": {"identifier":"B","version":"17.5","platformIdentifier":"iOS","sizeBytes":7000000000},
	  "C": {"identifier":"C","version":"11.2","platformIdentifier":"watchOS","sizeBytes":3000000000}}`)
	devices := []byte(`{"devices":{
	  "com.apple.CoreSimulator.SimRuntime.iOS-18-2":[{"isAvailable":true}],
	  "com.apple.CoreSimulator.SimRuntime.iOS-17-5":[],
	  "com.apple.CoreSimulator.SimRuntime.watchOS-11-2":[]}}`)
	unused, err := UnusedRuntimes(runtimes, devices)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, rt := range unused {
		got[rt.Identifier] = true
	}
	if got["A"] || !got["B"] || !got["C"] {
		t.Fatalf("unused = %v", got)
	}
}

func TestSizes(t *testing.T) {
	if ParseReclaimed("Deleted build cache objects:\n\nTotal reclaimed space: 1.5GB") != 1_500_000_000 {
		t.Fatal("docker GB")
	}
	if n, _ := ParseHuman("100G"); n != 100<<30 {
		t.Fatal("100G")
	}
	if n, _ := ParseHuman("1.5T"); n != 1<<40+512<<30 {
		t.Fatal("1.5T")
	}
	if Human(77<<30) != "77.0G" || Human(1536) != "1.5K" || Human(500) != "500B" {
		t.Fatalf("Human: %s %s %s", Human(77<<30), Human(1536), Human(500))
	}
}

type progressFunc func(Result)

func (f progressFunc) Step(r Result)                     { f(r) }
func (progressFunc) TierDone(Tier, int64, time.Duration) {}

func TestSandboxTmpExceptsMessagesAndSignedDelta(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "Library/Containers/com.apple.MobileSMS/Data/tmp/old"), 1000, 90*24*time.Hour)
	write(t, filepath.Join(home, "Library/Containers/com.other.app/Data/tmp/old"), 10, 90*24*time.Hour)
	env := newFakeEnv(t, home, 1<<30)
	rep, err := Run(context.Background(), env.Env, Options{DryRun: true, Only: []string{"sandbox-tmp"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Results[0].Bytes; got != 10 {
		t.Fatalf("sandbox-tmp must skip the Messages tree: %d", got)
	}
	if Signed(-5<<20) != "-5.0M" || Signed(3<<30) != "+3.0G" {
		t.Fatalf("Signed: %s %s", Signed(-5<<20), Signed(3<<30))
	}
}

// A locked file (macOS user-immutable flag) is the one deletion failure a
// non-root test can stage. The tree walk must delete everything around it,
// count only what it removed, surface the error, and the ladder must carry on
// with the next step — a half-freed disk is still the goal.
func TestPartialFailureIsAggregatedAndTheLadderContinues(t *testing.T) {
	if goos := os.Getenv("GOOS"); goos != "" && goos != "darwin" {
		t.Skip("chflags uchg is macOS-only")
	}
	if _, err := exec.LookPath("chflags"); err != nil {
		t.Skip("chflags not available")
	}
	home := t.TempDir()
	locked := filepath.Join(home, "Library/Caches/go-build/locked")
	write(t, locked, 100, 0)
	write(t, filepath.Join(home, "Library/Caches/go-build/sub/free"), 300, 0)
	write(t, filepath.Join(home, ".npm/_cacache/x"), 50, 0)
	if out, err := exec.Command("chflags", "uchg", locked).CombinedOutput(); err != nil {
		t.Skipf("cannot lock file: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("chflags", "nouchg", locked).Run() })

	n, err := removeTree(context.Background(), filepath.Join(home, "Library/Caches/go-build"), false)
	if err == nil {
		t.Fatal("a locked file must surface as an error")
	}
	if n != 300 {
		t.Fatalf("only the deleted bytes count: got %d, want 300", n)
	}
	if _, err := os.Stat(filepath.Join(home, "Library/Caches/go-build/sub")); !os.IsNotExist(err) {
		t.Fatal("the deletable sibling subtree must be gone")
	}
	if _, err := os.Stat(locked); err != nil {
		t.Fatal("the locked file must still exist")
	}

	write(t, filepath.Join(home, "Library/Caches/go-build/sub/free"), 300, 0)
	env := newFakeEnv(t, home, 1<<30)
	rep, err := Run(context.Background(), env.Env, Options{Only: []string{"go-build,npm"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Result{}
	for _, r := range rep.Results {
		byID[r.ID] = r
	}
	if r := byID["go-build"]; r.Status != StatusFailed || r.Bytes != 300 || !strings.Contains(r.Error, "locked") {
		t.Fatalf("go-build should report the partial failure with its bytes: %+v", r)
	}
	if r := byID["npm"]; r.Status != StatusDone || r.Bytes != 50 {
		t.Fatalf("the ladder must continue past a failed step: %+v", r)
	}
}

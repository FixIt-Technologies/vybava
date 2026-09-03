package hotfix

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FixIt-Technologies/vybava/internal/runx"
)

// stubRunner runs git for real against temp repositories and scripts gh.
type stubRunner struct {
	ExecRunner
	gh func(args []string) (string, error)
}

func (s *stubRunner) Run(dir, name string, args ...string) (string, error) {
	if name == "gh" {
		return s.gh(args)
	}
	return s.ExecRunner.Run(dir, name, args...)
}

func (s *stubRunner) Stream(dir, name string, args ...string) error {
	if name == "gh" {
		_, err := s.gh(args)
		return err
	}
	return s.ExecRunner.Stream(dir, name, args...)
}

func (s *stubRunner) Sleep(time.Duration) {}

// ghState is the scripted GitHub side: what `gh pr list` and `gh run list`
// answer, and which commands were issued.
type ghState struct {
	pr    *prRaw
	runs  []RunInfo
	head  string // sha the next dispatched run reports
	calls []string
}

func (g *ghState) handler(args []string) (string, error) {
	g.calls = append(g.calls, strings.Join(args, " "))
	switch {
	case args[0] == "auth":
		return "", nil
	case args[0] == "pr" && args[1] == "list":
		if g.pr == nil {
			return "[]", nil
		}
		b, _ := json.Marshal([]prRaw{*g.pr})
		return string(b), nil
	case args[0] == "pr" && args[1] == "create":
		g.pr = &prRaw{PRInfo: PRInfo{Number: 7, State: "OPEN", URL: "https://example.test/pr/7"}}
		return g.pr.URL, nil
	case args[0] == "pr" && args[1] == "merge":
		if g.pr.MergeStateStatus == "DIRTY" {
			return "", &ExitErr{Cmd: "gh pr merge", Code: 1, Stderr: "Pull request is not mergeable: the merge commit cannot be cleanly created"}
		}
		g.pr.State = "MERGED"
		return "", nil
	case args[0] == "label":
		return "", nil
	case args[0] == "workflow" && args[1] == "run":
		// The dispatch creates the run the next `gh run list` reports.
		g.runs = append([]RunInfo{{ID: 42, Status: "in_progress", URL: "https://example.test/run/42",
			CreatedAt: time.Now().UTC().Format(time.RFC3339), HeadSHA: g.head}}, g.runs...)
		return "", nil
	case args[0] == "run" && args[1] == "list":
		b, _ := json.Marshal(g.runs)
		return string(b), nil
	case args[0] == "run" && args[1] == "watch":
		return "", nil
	}
	return "", errors.New("unexpected gh call: " + strings.Join(args, " "))
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, file, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(msg+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", file)
	git(t, dir, "commit", "-q", "-m", msg)
	return git(t, dir, "rev-parse", "HEAD")
}

// fixture builds origin (bare) + a primary clone on main with a tagged
// release v1.2.3 and two unreleased commits on top — main is "ahead of
// prod", exactly the situation a hotfix exists for.
func fixture(t *testing.T) (root string, gh *ghState, tool *Tool) {
	t.Helper()
	base := t.TempDir()
	origin := filepath.Join(base, "origin.git")
	git(t, base, "init", "-q", "--bare", "-b", "main", origin)
	root = filepath.Join(base, "primary")
	git(t, base, "clone", "-q", origin, root)
	// git reports resolved paths; macOS temp dirs sit behind /private.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	git(t, root, "checkout", "-q", "-b", "main")
	commit(t, root, "a.txt", "release work")
	git(t, root, "tag", "v1.2.3")
	commit(t, root, "b.txt", "unreleased 1")
	commit(t, root, "c.txt", "unreleased 2")
	git(t, root, "push", "-q", "-u", "origin", "main", "--tags")
	if err := os.WriteFile(filepath.Join(root, ConfigFile), []byte("v: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gh = &ghState{}
	tool, err := Open(&stubRunner{gh: gh.handler}, root)
	if err != nil {
		t.Fatal(err)
	}
	return root, gh, tool
}

func TestLifecycle(t *testing.T) {
	root, gh, tool := fixture(t)

	// start: branch cut from v1.2.3 (not main's head), worktree at the default path.
	res, err := tool.Start("sms-token", "")
	if err != nil {
		t.Fatal(err)
	}
	s := res.Data.(*State)
	if s.Phase != PhaseEmpty || s.BaseTag != "v1.2.3" || s.Branch != "hotfix/sms-token" {
		t.Fatalf("after start: %+v", s)
	}
	wt := filepath.Join(root, ".worktrees", "hotfix-sms-token")
	if s.Worktree != wt || !s.WorktreeOK {
		t.Fatalf("worktree = %q ok=%v, want %q", s.Worktree, s.WorktreeOK, wt)
	}
	if got := git(t, wt, "log", "--oneline", "-1"); !strings.Contains(got, "release work") {
		t.Fatalf("worktree head = %q, want the tagged release commit", got)
	}
	if res.Next[1] != "hotfix pr sms-token" {
		t.Fatalf("next after start = %v", res.Next)
	}

	// start is idempotent.
	if res, err = tool.Start("sms-token", ""); err != nil || len(res.Diagnostics) != 2 {
		t.Fatalf("re-start: %v %+v", err, res.Diagnostics)
	}

	// pr before a commit is refused with the closed code.
	if _, err := tool.PR("sms-token", ""); !hasCode(err, DiagNoCommits) {
		t.Fatalf("pr on empty branch: %v", err)
	}
	commit(t, wt, "fix.txt", "fix the token")
	res, _ = tool.Status("sms-token", false)
	if s = res.Data.(*State); s.Phase != PhaseUnpushed || s.Commits != 1 || !s.Pure {
		t.Fatalf("after commit: %+v", s)
	}

	// deploy needs a pushed head + PR.
	if _, err := tool.Deploy("sms-token", false, false); !hasCode(err, DiagUnpushed) {
		t.Fatalf("deploy unpushed: %v", err)
	}
	res, err = tool.PR("sms-token", "")
	if err != nil {
		t.Fatal(err)
	}
	s = res.Data.(*State)
	if s.Phase != PhaseReady || !s.Pushed || s.PR == nil || s.PR.Number != 7 {
		t.Fatalf("after pr: %+v", s)
	}
	if git(t, root, "rev-parse", "origin/hotfix/sms-token") != s.HeadSHA {
		t.Fatal("pr did not push the branch")
	}
	if !strings.Contains(strings.Join(gh.calls, "\n"), "pr create --base main --head hotfix/sms-token") {
		t.Fatalf("gh calls: %v", gh.calls)
	}

	// deploy: red checks block, --force overrides; dispatch goes to the branch ref.
	gh.pr.StatusCheckRollup = []struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		State      string `json:"state"`
	}{{Conclusion: "FAILURE"}}
	if _, err := tool.Deploy("sms-token", false, false); !hasCode(err, DiagCIRed) {
		t.Fatalf("deploy over red CI: %v", err)
	}
	gh.pr.StatusCheckRollup[0].Conclusion = "SUCCESS"
	gh.head = s.HeadSHA
	res, err = tool.Deploy("sms-token", false, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "workflow run deploy-production.yml --ref hotfix/sms-token -f release_type=patch"
	if !strings.Contains(strings.Join(gh.calls, "\n"), want) {
		t.Fatalf("dispatch missing; gh calls: %v", gh.calls)
	}
	if s = res.Data.(*State); s.Phase != PhaseDeploying {
		t.Fatalf("after dispatch: %+v", s)
	}
	// A second deploy while in flight does not dispatch again.
	n := len(gh.calls)
	if _, err := tool.Deploy("sms-token", false, false); err != nil {
		t.Fatal(err)
	}
	for _, c := range gh.calls[n:] {
		if strings.HasPrefix(c, "workflow run") {
			t.Fatal("duplicate dispatch while a run was in flight")
		}
	}

	// finish before success is refused; after success it merges.
	if _, err := tool.Finish("sms-token", false); !hasCode(err, DiagNotDeployed) {
		t.Fatalf("finish before deploy: %v", err)
	}
	gh.runs[0].Status, gh.runs[0].Conclusion = "completed", "success"
	git(t, wt, "tag", "v1.2.4")
	git(t, wt, "push", "-q", "origin", "--tags")
	res, _ = tool.Status("sms-token", false)
	if s = res.Data.(*State); s.Phase != PhaseDeployed || len(s.Released) != 1 || s.Released[0] != "v1.2.4" {
		t.Fatalf("after deploy success: %+v", s)
	}
	res, err = tool.Finish("sms-token", false)
	if err != nil {
		t.Fatal(err)
	}
	if s = res.Data.(*State); s.Phase != PhaseFinished {
		t.Fatalf("after finish: %+v", s)
	}
	if res.Next[0] != "git -C "+root+" pull --ff-only" || !strings.HasPrefix(res.Next[1], "git worktree remove") {
		t.Fatalf("next after finish = %v", res.Next)
	}
}

func TestLineageLeakIsRefused(t *testing.T) {
	root, gh, tool := fixture(t)
	if _, err := tool.Start("leaky", ""); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(root, ".worktrees", "hotfix-leaky")
	commit(t, wt, "fix.txt", "fix")
	git(t, wt, "merge", "-q", "--no-edit", "origin/main")
	res, err := tool.Status("leaky", false)
	if err != nil {
		t.Fatal(err)
	}
	s := res.Data.(*State)
	if s.Phase != PhaseLeaked || s.Pure || s.LeakBase == "" {
		t.Fatalf("leaked branch: %+v", s)
	}
	if !strings.HasPrefix(res.Next[0], "git -C "+wt+" rebase --onto v1.2.3 "+s.LeakBase) {
		t.Fatalf("leak repair = %v", res.Next)
	}
	for _, verb := range []func() error{
		func() error { _, err := tool.PR("leaky", ""); return err },
		func() error { _, err := tool.Deploy("leaky", false, true); return err },
		func() error { _, err := tool.Forward("leaky"); return err },
		func() error { _, err := tool.Finish("leaky", true); return err },
	} {
		if err := verb(); !hasCode(err, DiagLineageLeak) {
			t.Fatalf("verb accepted a leaked branch: %v", err)
		}
	}
	_ = gh
}

func TestFinishConflictRoutesToForward(t *testing.T) {
	root, gh, tool := fixture(t)
	if _, err := tool.Start("conflict", ""); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(root, ".worktrees", "hotfix-conflict")
	// Same file main already changed after the release → the forward-port conflicts.
	commit(t, wt, "b.txt", "hotfix version of b")
	if _, err := tool.PR("conflict", ""); err != nil {
		t.Fatal(err)
	}
	gh.pr.MergeStateStatus = "DIRTY"
	head := git(t, wt, "rev-parse", "HEAD")
	gh.runs = []RunInfo{{ID: 1, Status: "completed", Conclusion: "success", HeadSHA: head, CreatedAt: time.Now().UTC().Format(time.RFC3339)}}
	res, err := tool.Finish("conflict", false)
	if !hasExit(err, 2) || len(res.Diagnostics) == 0 || res.Diagnostics[len(res.Diagnostics)-1].Code != DiagForwardConflict {
		t.Fatalf("finish on conflict: err=%v diags=%+v", err, res.Diagnostics)
	}
	if res.Next[0] != "hotfix forward conflict" {
		t.Fatalf("next = %v", res.Next)
	}
	res, err = tool.Forward("conflict")
	if !hasExit(err, 2) {
		t.Fatalf("forward should stop on the conflict: %v", err)
	}
	fwt := filepath.Join(root, ".worktrees", "hotfix-forward-conflict")
	if res.Data.(map[string]any)["forwardWorktree"] != fwt {
		t.Fatalf("forward data = %+v", res.Data)
	}
	if st := git(t, fwt, "status", "--porcelain"); !strings.Contains(st, "UU b.txt") && !strings.Contains(st, "AA b.txt") {
		t.Fatalf("expected conflict on b.txt in %s, got %q", fwt, st)
	}
	if git(t, wt, "rev-parse", "HEAD") != head {
		t.Fatal("forward-port touched the hotfix branch")
	}
}

func TestOpenDiagnostics(t *testing.T) {
	r := &stubRunner{gh: (&ghState{}).handler}
	if _, err := Open(r, t.TempDir()); !hasCode(err, DiagNotARepo) {
		t.Fatalf("non-repo: %v", err)
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	if _, err := Open(r, dir); !hasCode(err, DiagConfigMissing) {
		t.Fatalf("no config: %v", err)
	}
	res, err := Init(r, dir)
	if err != nil || res.Next[1] != "hotfix start <slug>" {
		t.Fatalf("init: %v %v", err, res.Next)
	}
	if _, err := Init(r, dir); err != nil {
		t.Fatalf("init must be idempotent: %v", err)
	}
	tool, err := Open(r, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Inspect("Not Kebab"); !hasCode(err, DiagSlugInvalid) {
		t.Fatalf("bad slug: %v", err)
	}
	if _, err := tool.InferSlug(dir); !hasCode(err, DiagSlugRequired) {
		t.Fatalf("infer on main: %v", err)
	}
	// A missing hotfix reports exactly one diagnostic: the branch is missing.
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "root")
	res, err = tool.Status("nope", false)
	if err != nil || res.Data.(*State).Phase != PhaseMissing || len(res.Diagnostics) != 1 || res.Diagnostics[0].Code != DiagBranchMissing {
		t.Fatalf("status on missing: %v %+v", err, res.Diagnostics)
	}
}

func TestDerivePhaseTable(t *testing.T) {
	open := &PRInfo{State: "OPEN"}
	cases := []struct {
		name string
		s    State
		want Phase
	}{
		{"merged wins", State{PR: &PRInfo{State: "MERGED"}}, PhaseFinished},
		{"empty", State{Commits: 0, Pure: true}, PhaseEmpty},
		{"leaked", State{Commits: 1}, PhaseLeaked},
		{"unpushed", State{Commits: 1, Pure: true, RemoteBranch: true}, PhaseUnpushed},
		{"no pr", State{Commits: 1, Pure: true, RemoteBranch: true, Pushed: true}, PhaseNoPR},
		{"closed pr is no pr", State{Commits: 1, Pure: true, RemoteBranch: true, Pushed: true, PR: &PRInfo{State: "CLOSED"}}, PhaseNoPR},
		{"ready", State{Commits: 1, Pure: true, RemoteBranch: true, Pushed: true, PR: open}, PhaseReady},
		{"deploying", State{Commits: 1, Pure: true, RemoteBranch: true, Pushed: true, PR: open, HeadSHA: "x", LastRun: &RunInfo{Status: "in_progress", HeadSHA: "x"}}, PhaseDeploying},
		{"stale run does not mask new head", State{Commits: 2, Pure: true, RemoteBranch: true, Pushed: true, PR: open, HeadSHA: "y", LastRun: &RunInfo{Status: "in_progress", HeadSHA: "x"}}, PhaseReady},
		{"deployed", State{Commits: 1, Pure: true, RemoteBranch: true, Pushed: true, PR: open, HeadSHA: "x", DeployedSHA: "x", LastRun: &RunInfo{Status: "completed"}}, PhaseDeployed},
		{"head moved", State{Commits: 2, Pure: true, RemoteBranch: true, Pushed: true, PR: open, HeadSHA: "y", DeployedSHA: "x", LastRun: &RunInfo{Status: "completed"}}, PhaseReady},
	}
	for _, c := range cases {
		if got := derivePhase(&c.s); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

func hasCode(err error, code string) bool {
	var de runx.DiagError
	return errors.As(err, &de) && de.Diag.Code == code
}

func hasExit(err error, code int) bool {
	var ec runx.ExitCoder
	return errors.As(err, &ec) && ec.ExitCode() == code
}

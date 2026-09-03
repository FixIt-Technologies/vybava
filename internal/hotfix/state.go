package hotfix

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Tool binds a runner to a repository: Root is the PRIMARY checkout (the
// one whose .git is the common dir), so branch queries see every worktree.
type Tool struct {
	R    Runner
	Root string
	Cfg  Config
}

var stableTag = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)
var kebab = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Open resolves the primary checkout for cwd and loads its manifest.
func Open(r Runner, cwd string) (*Tool, error) {
	common, err := r.Run(cwd, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, diag(DiagNotARepo, "not inside a git repository: "+cwd, "cd <repo> && hotfix status")
	}
	root := filepath.Dir(common)
	// The manifest is read from the checkout you stand in first (so a branch
	// that introduces hotfix.yaml can drive the lane before it merges), then
	// from the primary checkout. Commands always run in the primary root —
	// branches and worktrees are shared there.
	cfg, err := LoadConfig(root)
	if top, terr := r.Run(cwd, "git", "rev-parse", "--show-toplevel"); terr == nil && top != root {
		if c, cerr := LoadConfig(top); cerr == nil {
			cfg, err = c, nil
		} else if !errors.Is(cerr, ErrConfigMissing) {
			cfg, err = c, cerr
		}
	}
	if errors.Is(err, ErrConfigMissing) {
		return nil, diag(DiagConfigMissing, "no "+ConfigFile+" at "+root, "hotfix init")
	}
	if err != nil {
		return nil, diag(DiagConfigInvalid, err.Error(), "")
	}
	return &Tool{R: r, Root: root, Cfg: cfg}, nil
}

func (t *Tool) git(args ...string) (string, error) { return t.R.Run(t.Root, "git", args...) }
func (t *Tool) gh(args ...string) (string, error)  { return t.R.Run(t.Root, "gh", args...) }

func (t *Tool) fetch() error {
	_, err := t.git("fetch", "--quiet", "--prune", "--tags", "origin")
	return err
}

func (t *Tool) refExists(ref string) bool {
	_, err := t.git("rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}

func (t *Tool) sha(ref string) (string, error) {
	return t.git("rev-parse", "--verify", "--quiet", ref+"^{commit}")
}

// latestStableTag is the highest vX.Y.Z tag — what production runs when the
// deploy lane tags every successful release.
func (t *Tool) latestStableTag() (string, error) {
	out, err := t.git("tag", "--list", t.Cfg.TagGlob, "--sort=-v:refname")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if stableTag.MatchString(strings.TrimSpace(line)) {
			return strings.TrimSpace(line), nil
		}
	}
	return "", nil
}

// baseTag is the nearest stable tag reachable from ref — the release the
// hotfix is built on.
func (t *Tool) baseTag(ref string) (string, error) {
	return t.git("describe", "--tags", "--abbrev=0", "--match", t.Cfg.TagGlob, "--exclude", "*-*", ref)
}

func (t *Tool) countCommits(rangeSpec string) (int, error) {
	out, err := t.git("rev-list", "--count", rangeSpec)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// tagsBetween lists stable tags reachable from head but not from base —
// the releases this branch has produced.
func (t *Tool) tagsBetween(base, head string) ([]string, error) {
	out, err := t.git("tag", "--list", t.Cfg.TagGlob, "--merged", head, "--no-merged", base)
	if err != nil {
		return nil, err
	}
	var tags []string
	for _, line := range strings.Split(out, "\n") {
		if l := strings.TrimSpace(line); stableTag.MatchString(l) {
			tags = append(tags, l)
		}
	}
	sort.Strings(tags)
	return tags, nil
}

// PRInfo is the subset of `gh pr list --json` the lane needs.
type PRInfo struct {
	Number           int    `json:"number"`
	State            string `json:"state"` // OPEN | MERGED | CLOSED
	URL              string `json:"url"`
	IsDraft          bool   `json:"isDraft"`
	MergeStateStatus string `json:"mergeStateStatus"`
	Checks           string `json:"checks"` // derived: PASSING | FAILING | PENDING | NONE
}

type prRaw struct {
	PRInfo
	StatusCheckRollup []struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		State      string `json:"state"`
	} `json:"statusCheckRollup"`
}

func (t *Tool) findPR(branch string) (*PRInfo, error) {
	out, err := t.gh("pr", "list", "--head", branch, "--base", t.Cfg.DefaultBranch, "--state", "all",
		"--limit", "1", "--json", "number,state,url,isDraft,mergeStateStatus,statusCheckRollup")
	if err != nil {
		return nil, err
	}
	var raws []prRaw
	if err := json.Unmarshal([]byte(out), &raws); err != nil {
		return nil, fmt.Errorf("parse gh pr list: %w", err)
	}
	if len(raws) == 0 {
		return nil, nil
	}
	pr := raws[0].PRInfo
	pr.Checks = "NONE"
	pending, failing := false, false
	for _, c := range raws[0].StatusCheckRollup {
		concl := strings.ToUpper(c.Conclusion)
		if concl == "" {
			concl = strings.ToUpper(c.State)
		}
		switch concl {
		case "SUCCESS", "NEUTRAL", "SKIPPED":
		case "FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE":
			failing = true
		default:
			pending = true
		}
	}
	switch {
	case failing:
		pr.Checks = "FAILING"
	case pending:
		pr.Checks = "PENDING"
	case len(raws[0].StatusCheckRollup) > 0:
		pr.Checks = "PASSING"
	}
	return &pr, nil
}

// RunInfo is the subset of `gh run list --json` the lane needs.
type RunInfo struct {
	ID         int64  `json:"databaseId"`
	Status     string `json:"status"`     // queued | in_progress | completed
	Conclusion string `json:"conclusion"` // success | failure | cancelled | ...
	URL        string `json:"url"`
	CreatedAt  string `json:"createdAt"`
	HeadSHA    string `json:"headSha"`
}

func (t *Tool) listRuns(branch string, limit int) ([]RunInfo, error) {
	out, err := t.gh("run", "list", "--workflow", t.Cfg.Deploy.Workflow, "--branch", branch,
		"--event", "workflow_dispatch", "--limit", strconv.Itoa(limit),
		"--json", "databaseId,status,conclusion,url,createdAt,headSha")
	if err != nil {
		return nil, err
	}
	var runs []RunInfo
	if err := json.Unmarshal([]byte(out), &runs); err != nil {
		return nil, fmt.Errorf("parse gh run list: %w", err)
	}
	return runs, nil
}

// Phase is the closed lifecycle vocabulary `hotfix status` reports.
type Phase string

const (
	PhaseMissing   Phase = "MISSING"   // branch exists nowhere
	PhaseEmpty     Phase = "EMPTY"     // no commit beyond the base tag
	PhaseLeaked    Phase = "LEAKED"    // default-branch commits leaked in
	PhaseUnpushed  Phase = "UNPUSHED"  // local commits not on origin
	PhaseNoPR      Phase = "NO_PR"     // pushed, no PR yet
	PhaseReady     Phase = "READY"     // PR open, nothing deployed at head
	PhaseDeploying Phase = "DEPLOYING" // a deploy run is in flight
	PhaseDeployed  Phase = "DEPLOYED"  // head shipped, PR still open
	PhaseFinished  Phase = "FINISHED"  // PR merged
)

// State is the full, re-derived picture of one hotfix.
type State struct {
	Slug         string   `json:"slug"`
	Branch       string   `json:"branch"`
	Worktree     string   `json:"worktree"`
	WorktreeOK   bool     `json:"worktreeExists"`
	BaseTag      string   `json:"baseTag"`
	BaseSHA      string   `json:"baseSha"`
	HeadSHA      string   `json:"headSha,omitempty"`
	LocalBranch  bool     `json:"localBranch"`
	RemoteBranch bool     `json:"remoteBranch"`
	Pushed       bool     `json:"pushed"`
	Commits      int      `json:"commitsOnTop"`
	Pure         bool     `json:"lineagePure"`
	LeakBase     string   `json:"leakMergeBase,omitempty"`
	PR           *PRInfo  `json:"pr,omitempty"`
	LastRun      *RunInfo `json:"lastDeploy,omitempty"`
	DeployedSHA  string   `json:"deployedSha,omitempty"`
	Released     []string `json:"releasedTags"`
	Phase        Phase    `json:"phase"`
}

// InferSlug reads the current branch of cwd and strips the hotfix prefix.
func (t *Tool) InferSlug(cwd string) (string, error) {
	branch, err := t.R.Run(cwd, "git", "branch", "--show-current")
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(branch, t.Cfg.BranchPrefix) {
		return strings.TrimPrefix(branch, t.Cfg.BranchPrefix), nil
	}
	return "", diag(DiagSlugRequired,
		fmt.Sprintf("current branch %q is not a %s* branch; name the hotfix", branch, t.Cfg.BranchPrefix),
		"hotfix status <slug>")
}

func validSlug(slug string) error {
	if !kebab.MatchString(slug) {
		return diag(DiagSlugInvalid, fmt.Sprintf("slug %q must be kebab-case (a-z, 0-9, single dashes)", slug), "")
	}
	return nil
}

// Inspect re-derives the state of one hotfix from git and gh. It never
// mutates anything; verbs call it before and after acting.
func (t *Tool) Inspect(slug string) (*State, error) {
	if err := validSlug(slug); err != nil {
		return nil, err
	}
	v := t.Cfg.VarsFor(t.Root, slug, "")
	s := &State{Slug: slug, Branch: v.Branch, Worktree: v.Path, Released: []string{}}
	if st, err := os.Stat(v.Path); err == nil && st.IsDir() {
		s.WorktreeOK = true
	}
	s.LocalBranch = t.refExists("refs/heads/" + v.Branch)
	s.RemoteBranch = t.refExists("refs/remotes/origin/" + v.Branch)
	if !s.LocalBranch && !s.RemoteBranch {
		s.Phase = PhaseMissing
		return s, nil
	}
	head := "refs/heads/" + v.Branch
	if !s.LocalBranch {
		head = "refs/remotes/origin/" + v.Branch
	}
	var err error
	if s.HeadSHA, err = t.sha(head); err != nil {
		return nil, err
	}
	if s.RemoteBranch {
		remote, err := t.sha("refs/remotes/origin/" + v.Branch)
		if err != nil {
			return nil, err
		}
		s.Pushed = remote == s.HeadSHA
	}
	// The base is the release the branch forked from: the nearest stable tag
	// reachable from the merge-base with the default branch. Not "nearest tag
	// from head" — the deploy lane tags the branch head itself, which would
	// make a shipped hotfix read as empty.
	mainRef := "refs/remotes/origin/" + t.Cfg.DefaultBranch
	if !t.refExists(mainRef) {
		mainRef = "refs/heads/" + t.Cfg.DefaultBranch
	}
	mb, err := t.git("merge-base", mainRef, head)
	if err != nil {
		return nil, err
	}
	if s.BaseTag, err = t.baseTag(mb); err != nil {
		return nil, diag(DiagTagMissing, "no stable tag is reachable from the fork point of "+v.Branch+": "+err.Error(), "")
	}
	if s.BaseSHA, err = t.sha("refs/tags/" + s.BaseTag); err != nil {
		return nil, err
	}
	if s.Commits, err = t.countCommits(s.BaseTag + ".." + head); err != nil {
		return nil, err
	}
	// Lineage purity: the fork point IS the release tag. A merge-base past
	// it means default-branch commits leaked in (someone merged main).
	s.Pure = mb == s.BaseSHA
	if !s.Pure {
		s.LeakBase = mb
	}
	if s.Released, err = t.tagsBetween(s.BaseSHA, head); err != nil {
		return nil, err
	}
	if s.Released == nil {
		s.Released = []string{}
	}
	if s.RemoteBranch {
		if s.PR, err = t.findPR(v.Branch); err != nil {
			return nil, err
		}
		runs, err := t.listRuns(v.Branch, 10)
		if err != nil {
			return nil, err
		}
		if len(runs) > 0 {
			s.LastRun = &runs[0]
		}
		for _, r := range runs {
			if r.Status == "completed" && r.Conclusion == "success" {
				s.DeployedSHA = r.HeadSHA
				break
			}
		}
	}
	s.Phase = derivePhase(s)
	return s, nil
}

func derivePhase(s *State) Phase {
	switch {
	case s.PR != nil && s.PR.State == "MERGED":
		return PhaseFinished
	case s.Commits == 0:
		return PhaseEmpty
	case !s.Pure:
		return PhaseLeaked
	case !s.RemoteBranch || !s.Pushed:
		return PhaseUnpushed
	case s.LastRun != nil && s.LastRun.Status != "completed" && s.LastRun.HeadSHA == s.HeadSHA:
		// Only a run building THIS head counts; an older in-flight run must
		// not mask a new commit (eve r1).
		return PhaseDeploying
	case s.DeployedSHA != "" && s.DeployedSHA == s.HeadSHA:
		return PhaseDeployed
	case s.PR == nil || s.PR.State != "OPEN":
		return PhaseNoPR
	default:
		return PhaseReady
	}
}

// Next is the protocol: the exact commands for the phase, success and
// failure alike.
func (t *Tool) Next(s *State) []string {
	v := t.Cfg.VarsFor(t.Root, s.Slug, "")
	switch s.Phase {
	case PhaseMissing:
		return []string{"hotfix start " + s.Slug}
	case PhaseEmpty:
		return []string{"git -C " + v.Path + " status", "hotfix pr " + s.Slug}
	case PhaseLeaked:
		return []string{
			fmt.Sprintf("git -C %s rebase --onto %s %s %s", v.Path, s.BaseTag, s.LeakBase, s.Branch),
			"hotfix status " + s.Slug,
		}
	case PhaseUnpushed, PhaseNoPR:
		return []string{"hotfix pr " + s.Slug}
	case PhaseReady:
		return []string{"hotfix deploy " + s.Slug + " --watch"}
	case PhaseDeploying:
		return []string{"hotfix deploy " + s.Slug + " --watch"}
	case PhaseDeployed:
		return []string{"hotfix finish " + s.Slug}
	case PhaseFinished:
		return []string{
			"git -C " + t.Root + " pull --ff-only",
			Expand(t.Cfg.Worktree.Cleanup, v),
		}
	}
	return []string{}
}

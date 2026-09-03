package hotfix

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/FixIt-Technologies/vybava/internal/runx"
)

// Result is what every verb hands the CLI: the payload plus the diagnostics
// and next commands the envelope carries. Verbs never print.
type Result struct {
	Data        any
	Diagnostics []runx.Diagnostic
	Next        []string
}

func (t *Tool) result(s *State, extra ...runx.Diagnostic) Result {
	return Result{Data: s, Diagnostics: extra, Next: t.Next(s)}
}

// Init writes the default manifest into the primary checkout of cwd.
func Init(r Runner, cwd string) (Result, error) {
	common, err := r.Run(cwd, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return Result{}, diag(DiagNotARepo, "not inside a git repository: "+cwd, "cd <repo> && hotfix init")
	}
	root := strings.TrimSuffix(common, "/.git")
	path, err := WriteDefaultConfig(root)
	if errors.Is(err, os.ErrExist) {
		return Result{Data: map[string]string{"config": path},
			Diagnostics: []runx.Diagnostic{info("CONFIG_EXISTS", path+" already exists; left untouched", "")},
			Next:        []string{"hotfix status <slug>"}}, nil
	}
	if err != nil {
		return Result{}, err
	}
	return Result{Data: map[string]string{"config": path},
		Next: []string{"$EDITOR " + path, "hotfix start <slug>"}}, nil
}

func (t *Tool) requireGh() error {
	if _, err := t.gh("auth", "status"); err != nil {
		return diag(DiagGhUnauthenticated, "gh is not authenticated for this repository", "gh auth login")
	}
	return nil
}

// Start cuts <prefix><slug> from the release production runs (or --from)
// and creates its worktree through the project's create command. Re-running
// is safe: an existing branch is reused, an existing worktree reported.
func (t *Tool) Start(slug, from string) (Result, error) {
	if err := validSlug(slug); err != nil {
		return Result{}, err
	}
	if err := t.fetch(); err != nil {
		return Result{}, err
	}
	if from == "" {
		latest, err := t.latestStableTag()
		if err != nil {
			return Result{}, err
		}
		if latest == "" {
			return Result{}, diag(DiagTagMissing, "the repository has no stable "+t.Cfg.TagGlob+" tag to hotfix from", "hotfix start "+slug+" --from <ref>")
		}
		from = latest
	} else if !t.refExists(from) {
		return Result{}, diag(DiagTagMissing, "ref "+from+" does not exist", "git tag --list "+t.Cfg.TagGlob+" --sort=-v:refname | head")
	}
	v := t.Cfg.VarsFor(t.Root, slug, from)
	local := t.refExists("refs/heads/" + v.Branch)
	remote := t.refExists("refs/remotes/origin/" + v.Branch)
	var notes []runx.Diagnostic
	if !local && remote {
		if _, err := t.git("branch", "--track", v.Branch, "origin/"+v.Branch); err != nil {
			return Result{}, err
		}
		notes = append(notes, info("BRANCH_ADOPTED", v.Branch+" existed on origin; tracking it locally", ""))
	} else if local {
		notes = append(notes, info("BRANCH_REUSED", v.Branch+" already exists locally; base tag left as is", ""))
	}
	if st, err := os.Stat(v.Path); err == nil && st.IsDir() {
		notes = append(notes, info("WORKTREE_REUSED", "worktree already at "+v.Path, ""))
	} else {
		cmd := Expand(t.Cfg.Worktree.Create, v)
		if err := t.R.Stream(t.Root, "sh", "-c", cmd); err != nil {
			return Result{}, fmt.Errorf("worktree create failed (%s): %w", cmd, err)
		}
		if st, err := os.Stat(v.Path); err != nil || !st.IsDir() {
			return Result{}, diag(DiagWorktreeMissing,
				"create command finished but "+v.Path+" does not exist; worktree.path in "+ConfigFile+" disagrees with worktree.create", "")
		}
	}
	s, err := t.Inspect(slug)
	if err != nil {
		return Result{}, err
	}
	return t.result(s, notes...), nil
}

// Status re-derives everything and hands back the protocol for the phase.
func (t *Tool) Status(slug string, fetch bool) (Result, error) {
	if fetch {
		if err := t.fetch(); err != nil {
			return Result{}, err
		}
	}
	s, err := t.Inspect(slug)
	if err != nil {
		return Result{}, err
	}
	var notes []runx.Diagnostic
	if s.Phase == PhaseMissing {
		notes = append(notes, warn(DiagBranchMissing, s.Branch+" exists neither locally nor on origin", "hotfix start "+slug))
	}
	if s.Phase != PhaseMissing && !s.WorktreeOK && s.Phase != PhaseFinished {
		notes = append(notes, warn(DiagWorktreeMissing, "no worktree at "+s.Worktree, "hotfix start "+slug))
	}
	if s.Phase != PhaseMissing && !s.Pure {
		notes = append(notes, warn(DiagLineageLeak, leakDetail(s), t.Next(s)[0]))
	}
	if s.LastRun != nil && s.LastRun.Status != "completed" && s.LastRun.HeadSHA != s.HeadSHA && s.Phase != PhaseFinished {
		notes = append(notes, info(DiagDeployInProgress, "a run for an OLDER head "+short(s.LastRun.HeadSHA)+" is still in flight: "+s.LastRun.URL+"; a new dispatch supersedes it", ""))
	}
	if s.DeployedSHA != "" && s.DeployedSHA != s.HeadSHA && s.Phase != PhaseFinished {
		notes = append(notes, info(DiagHeadMoved, "a deploy succeeded at "+short(s.DeployedSHA)+" but the branch head is "+short(s.HeadSHA), "hotfix deploy "+slug+" --watch"))
	}
	return t.result(s, notes...), nil
}

func leakDetail(s *State) string {
	return fmt.Sprintf("%s contains default-branch commits past %s (merge-base %s): someone merged main in; deploying would ship unreleased work as a hotfix",
		s.Branch, s.BaseTag, short(s.LeakBase))
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// PR pushes the branch and opens (or reuses) the PR against the default
// branch. That PR is the CI + review gate for the deploy AND the forward-
// port that lands the fix on main afterwards.
func (t *Tool) PR(slug, title string) (Result, error) {
	if err := t.requireGh(); err != nil {
		return Result{}, err
	}
	if err := t.fetch(); err != nil {
		return Result{}, err
	}
	s, err := t.Inspect(slug)
	if err != nil {
		return Result{}, err
	}
	if s.Phase == PhaseMissing {
		return Result{}, diag(DiagBranchMissing, s.Branch+" does not exist", "hotfix start "+slug)
	}
	if s.Commits == 0 {
		return Result{}, diag(DiagNoCommits, s.Branch+" has no commit beyond "+s.BaseTag+" yet", "git -C "+s.Worktree+" status")
	}
	if !s.Pure {
		return Result{}, diag(DiagLineageLeak, leakDetail(s), t.Next(s)[0])
	}
	if !s.Pushed {
		if _, err := t.git("push", "--quiet", "--set-upstream", "origin", s.Branch+":"+s.Branch); err != nil {
			return Result{}, err
		}
	}
	var notes []runx.Diagnostic
	if s.PR != nil && s.PR.State != "CLOSED" {
		notes = append(notes, info("PR_REUSED", "PR #"+strconv.Itoa(s.PR.Number)+" already exists: "+s.PR.URL, ""))
	} else {
		for _, label := range t.Cfg.PR.Labels {
			// --force makes label creation idempotent (updates when it exists).
			if _, err := t.gh("label", "create", label, "--force", "--color", "B60205", "--description", "Release-lineage hotfix (deployed from its branch; PR is the forward-port)"); err != nil {
				return Result{}, err
			}
		}
		if title == "" {
			title = "hotfix(" + s.BaseTag + "): " + strings.ReplaceAll(slug, "-", " ")
		}
		args := []string{"pr", "create", "--base", t.Cfg.DefaultBranch, "--head", s.Branch, "--title", title, "--body", t.prBody(s)}
		for _, label := range t.Cfg.PR.Labels {
			args = append(args, "--label", label)
		}
		if _, err := t.gh(args...); err != nil {
			return Result{}, err
		}
	}
	if s, err = t.Inspect(slug); err != nil {
		return Result{}, err
	}
	return t.result(s, notes...), nil
}

func (t *Tool) prBody(s *State) string {
	log, _ := t.git("log", "--oneline", "--no-decorate", s.BaseTag+".."+s.Branch)
	return strings.Join([]string{
		"## Hotfix on " + s.BaseTag,
		"",
		"Production is deployed **from this branch** (`hotfix deploy " + s.Slug + "`), which tags the next patch",
		"release on it. Merging this PR is the forward-port: the merge commit makes that tag reachable from `" + t.Cfg.DefaultBranch + "`.",
		"",
		"**Never merge `" + t.Cfg.DefaultBranch + "` into this branch** — it would ship unreleased work as a hotfix. If the",
		"forward-port conflicts, resolve it in a separate cherry-pick worktree (`hotfix forward " + s.Slug + "`), not here.",
		"",
		"### Commits on top of " + s.BaseTag,
		"```",
		log,
		"```",
	}, "\n")
}

// Deploy dispatches the production workflow ON the hotfix branch. Guards:
// pure lineage, pushed head, a PR (the CI gate) whose checks are not red.
// A run already in flight is reported, never duplicated.
func (t *Tool) Deploy(slug string, watch, force bool) (Result, error) {
	if err := t.requireGh(); err != nil {
		return Result{}, err
	}
	if err := t.fetch(); err != nil {
		return Result{}, err
	}
	s, err := t.Inspect(slug)
	if err != nil {
		return Result{}, err
	}
	var notes []runx.Diagnostic
	switch s.Phase {
	case PhaseMissing:
		return Result{}, diag(DiagBranchMissing, s.Branch+" does not exist", "hotfix start "+slug)
	case PhaseEmpty:
		return Result{}, diag(DiagNoCommits, s.Branch+" has no commit beyond "+s.BaseTag, "git -C "+s.Worktree+" status")
	case PhaseLeaked:
		return Result{}, diag(DiagLineageLeak, leakDetail(s), t.Next(s)[0])
	case PhaseUnpushed:
		return Result{}, diag(DiagUnpushed, "local "+s.Branch+" is not what origin has; production must build a pushed sha", "hotfix pr "+slug)
	case PhaseFinished:
		return Result{}, diag(DiagAlreadyFinished, "PR #"+strconv.Itoa(s.PR.Number)+" is merged; this hotfix is done", t.Next(s)[0])
	case PhaseDeploying:
		notes = append(notes, info(DiagDeployInProgress, "run already in flight: "+s.LastRun.URL, ""))
		if watch {
			return t.watch(slug, s.LastRun, notes)
		}
		return t.result(s, notes...), nil
	case PhaseDeployed:
		notes = append(notes, info("ALREADY_DEPLOYED", "head "+short(s.HeadSHA)+" already shipped: "+s.LastRun.URL, ""))
		return t.result(s, notes...), nil
	case PhaseNoPR:
		if !force {
			return Result{}, diag(DiagPRMissing, "no open PR from "+s.Branch+" to "+t.Cfg.DefaultBranch+" — it is the CI and review gate", "hotfix pr "+slug)
		}
		notes = append(notes, warn(DiagPRMissing, "deploying without a PR (--force)", "hotfix pr "+slug))
	}
	if s.PR != nil {
		switch s.PR.Checks {
		case "FAILING":
			if !force {
				return Result{}, diag(DiagCIRed, "PR #"+strconv.Itoa(s.PR.Number)+" has failing checks: "+s.PR.URL, "hotfix deploy "+slug+" --force")
			}
			notes = append(notes, warn(DiagCIRed, "deploying over failing checks (--force)", s.PR.URL))
		case "PENDING":
			notes = append(notes, info(DiagCIPending, "PR checks still running; the deploy lane runs its own gates", s.PR.URL))
		}
	}
	args := []string{"workflow", "run", t.Cfg.Deploy.Workflow, "--ref", s.Branch}
	keys := make([]string, 0, len(t.Cfg.Deploy.Inputs))
	for k := range t.Cfg.Deploy.Inputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-f", k+"="+t.Cfg.Deploy.Inputs[k])
	}
	dispatched := t.R.Now().Add(-5 * time.Second)
	if _, err := t.gh(args...); err != nil {
		return Result{}, err
	}
	run, err := t.awaitRun(s.Branch, dispatched)
	if err != nil {
		return Result{}, err
	}
	notes = append(notes, info("DEPLOY_DISPATCHED", "run "+run.URL+" building "+short(run.HeadSHA), ""))
	if watch {
		return t.watch(slug, run, notes)
	}
	s, err = t.Inspect(slug)
	if err != nil {
		return Result{}, err
	}
	return t.result(s, notes...), nil
}

// awaitRun polls for the run the dispatch created (gh workflow run does
// not return its id).
func (t *Tool) awaitRun(branch string, since time.Time) (*RunInfo, error) {
	for i := 0; i < 20; i++ {
		runs, err := t.listRuns(branch, 3)
		if err != nil {
			return nil, err
		}
		for i := range runs {
			created, err := time.Parse(time.RFC3339, runs[i].CreatedAt)
			if err == nil && !created.Before(since) {
				return &runs[i], nil
			}
		}
		t.R.Sleep(3 * time.Second)
	}
	return nil, diag(DiagDeployNotFound, "dispatch accepted but no run appeared on "+branch+" within 60s",
		"gh run list --workflow "+t.Cfg.Deploy.Workflow+" --branch "+branch)
}

func (t *Tool) watch(slug string, run *RunInfo, notes []runx.Diagnostic) (Result, error) {
	err := t.R.Stream(t.Root, "gh", "run", "watch", strconv.FormatInt(run.ID, 10), "--exit-status", "--interval", "15")
	s, ierr := t.Inspect(slug)
	if ierr != nil {
		return Result{}, ierr
	}
	if err != nil {
		return Result{Data: s, Diagnostics: append(notes, runx.Diagnostic{Code: DiagDeployFailed, Severity: "error",
			Detail: "run did not succeed: " + run.URL, Fix: "gh run view " + strconv.FormatInt(run.ID, 10) + " --log-failed"}),
			Next: []string{"gh run view " + strconv.FormatInt(run.ID, 10) + " --log-failed"}}, runx.ExitError{Code: 2}
	}
	return t.result(s, notes...), nil
}

// Forward creates a cherry-pick worktree from the default branch for the
// case where the PR merge conflicts. The hotfix branch itself stays pure.
func (t *Tool) Forward(slug string) (Result, error) {
	if err := t.fetch(); err != nil {
		return Result{}, err
	}
	s, err := t.Inspect(slug)
	if err != nil {
		return Result{}, err
	}
	if s.Phase == PhaseMissing || s.Commits == 0 {
		return Result{}, diag(DiagNoCommits, s.Branch+" has nothing to forward-port", "hotfix status "+slug)
	}
	if !s.Pure {
		return Result{}, diag(DiagLineageLeak, leakDetail(s), t.Next(s)[0])
	}
	fv := Vars{Slug: "forward-" + slug, Root: t.Root, From: "origin/" + t.Cfg.DefaultBranch, Branch: "work/forward-" + slug}
	fv.Name = Expand(t.Cfg.Worktree.Name, fv)
	fv.Path = t.Cfg.VarsFor(t.Root, "forward-"+slug, fv.From).Path
	var notes []runx.Diagnostic
	if st, err := os.Stat(fv.Path); err == nil && st.IsDir() {
		notes = append(notes, info("WORKTREE_REUSED", "forward-port worktree already at "+fv.Path, ""))
	} else {
		cmd := Expand(t.Cfg.Worktree.Create, fv)
		if err := t.R.Stream(t.Root, "sh", "-c", cmd); err != nil {
			return Result{}, fmt.Errorf("worktree create failed (%s): %w", cmd, err)
		}
	}
	data := map[string]any{"hotfix": s, "forwardBranch": fv.Branch, "forwardWorktree": fv.Path, "range": s.BaseTag + ".." + s.Branch}
	if _, err := t.R.Run(fv.Path, "git", "cherry-pick", "-x", s.BaseTag+".."+s.Branch); err != nil {
		return Result{Data: data, Diagnostics: append(notes, runx.Diagnostic{Code: DiagForwardConflict, Severity: "error",
			Detail: "cherry-pick stopped on conflicts in " + fv.Path, Fix: "git -C " + fv.Path + " status"}),
			Next: []string{"git -C " + fv.Path + " status", "git -C " + fv.Path + " cherry-pick --continue",
				"git -C " + fv.Path + " push -u origin " + fv.Branch}}, runx.ExitError{Code: 2}
	}
	return Result{Data: data, Diagnostics: notes, Next: []string{
		"git -C " + fv.Path + " push -u origin " + fv.Branch,
		"gh pr create --base " + t.Cfg.DefaultBranch + " --head " + fv.Branch + " --title \"forward-port: " + slug + "\"",
		"gh pr close " + s.Branch + " --comment \"superseded by the forward-port PR\"",
	}}, nil
}

// Finish merges the PR once the branch head has shipped — the merge commit
// is the forward-port and makes the release tag reachable from main.
func (t *Tool) Finish(slug string, force bool) (Result, error) {
	if err := t.requireGh(); err != nil {
		return Result{}, err
	}
	if err := t.fetch(); err != nil {
		return Result{}, err
	}
	s, err := t.Inspect(slug)
	if err != nil {
		return Result{}, err
	}
	switch s.Phase {
	case PhaseMissing:
		return Result{}, diag(DiagBranchMissing, s.Branch+" does not exist", "hotfix start "+slug)
	case PhaseFinished:
		return t.result(s, info(DiagAlreadyFinished, "PR #"+strconv.Itoa(s.PR.Number)+" is already merged", "")), nil
	case PhaseLeaked:
		return Result{}, diag(DiagLineageLeak, leakDetail(s), t.Next(s)[0])
	}
	if s.PR == nil || s.PR.State != "OPEN" {
		return Result{}, diag(DiagPRMissing, "no open PR to merge for "+s.Branch, "hotfix pr "+slug)
	}
	if s.Phase != PhaseDeployed && !force {
		return Result{}, diag(DiagNotDeployed, "branch head "+short(s.HeadSHA)+" has not shipped successfully yet (phase "+string(s.Phase)+")", "hotfix deploy "+slug+" --watch")
	}
	var notes []runx.Diagnostic
	if s.Phase != PhaseDeployed {
		notes = append(notes, warn(DiagNotDeployed, "merging without a successful deploy of the head (--force)", ""))
	}
	if len(s.Released) == 0 {
		notes = append(notes, warn("TAG_NOT_ON_BRANCH", "no release tag is reachable from "+s.Branch+" beyond "+s.BaseTag+"; the deploy lane may not have tagged", "git tag --contains "+short(s.HeadSHA)))
	}
	args := append([]string{"pr", "merge", strconv.Itoa(s.PR.Number)}, t.Cfg.PR.MergeFlags...)
	if _, err := t.gh(args...); err != nil {
		if s.PR.MergeStateStatus == "DIRTY" || strings.Contains(strings.ToLower(err.Error()), "conflict") {
			return Result{Data: s, Diagnostics: append(notes, runx.Diagnostic{Code: DiagForwardConflict, Severity: "error",
				Detail: "PR #" + strconv.Itoa(s.PR.Number) + " conflicts with " + t.Cfg.DefaultBranch + "; never resolve by merging main into the hotfix branch",
				Fix:    "hotfix forward " + slug}), Next: []string{"hotfix forward " + slug}}, runx.ExitError{Code: 2}
		}
		return Result{}, err
	}
	if s, err = t.Inspect(slug); err != nil {
		return Result{}, err
	}
	return t.result(s, notes...), nil
}

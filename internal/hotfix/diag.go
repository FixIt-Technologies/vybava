package hotfix

import "github.com/FixIt-Technologies/vybava/internal/runx"

// The CLOSED diagnostic-code enum for the hotfix applet. Adding a code means
// a doc comment here stating when it fires and what the fix is.
const (
	// DiagConfigMissing: no hotfix.yaml at the primary checkout — fix is
	// `hotfix init`, which writes the commented defaults.
	DiagConfigMissing = "CONFIG_MISSING"
	// DiagConfigInvalid: hotfix.yaml exists but does not parse/validate.
	DiagConfigInvalid = "CONFIG_INVALID"
	// DiagNotARepo: cwd is not inside a git repository.
	DiagNotARepo = "CONTEXT_NOT_A_REPO"
	// DiagSlugRequired: the verb needs a slug and none was given nor could
	// be inferred from the current branch (<prefix><slug>).
	DiagSlugRequired = "SLUG_REQUIRED"
	// DiagSlugInvalid: slug is not kebab-case.
	DiagSlugInvalid = "SLUG_INVALID"
	// DiagGhUnauthenticated: `gh auth status` fails.
	DiagGhUnauthenticated = "GH_UNAUTHENTICATED"
	// DiagTagMissing: --from names a tag that does not exist, or the repo
	// has no stable tag at all.
	DiagTagMissing = "TAG_MISSING"
	// DiagBranchMissing: the hotfix branch exists neither locally nor on
	// origin — the fix is `hotfix start`.
	DiagBranchMissing = "BRANCH_MISSING"
	// DiagWorktreeMissing: the branch exists but its worktree does not — the
	// fix is `hotfix start` (idempotent: it re-creates only the worktree).
	DiagWorktreeMissing = "WORKTREE_MISSING"
	// DiagNoCommits: the branch has no commit beyond its base tag yet.
	DiagNoCommits = "NO_COMMITS"
	// DiagLineageLeak: commits from the default branch beyond the base tag
	// reached the hotfix branch (someone merged main in). Deploying it would
	// ship unreleased work as a "hotfix". The fix replays only the hotfix
	// commits onto the base tag.
	DiagLineageLeak = "LINEAGE_LEAK"
	// DiagUnpushed: local branch head differs from origin — `hotfix pr` pushes.
	DiagUnpushed = "UNPUSHED"
	// DiagPRMissing: no PR from the hotfix branch to the default branch —
	// deploy needs it as the CI + review gate (override: --force).
	DiagPRMissing = "PR_MISSING"
	// DiagCIRed: the PR's checks include a failure (override: --force).
	DiagCIRed = "CI_RED"
	// DiagCIPending: checks are still running — info, deploy proceeds.
	DiagCIPending = "CI_PENDING"
	// DiagDeployInProgress: a deploy run for this branch is already running;
	// no second dispatch is made.
	DiagDeployInProgress = "DEPLOY_IN_PROGRESS"
	// DiagDeployNotFound: the dispatch was accepted but no run appeared in
	// the wait window — the fix lists runs.
	DiagDeployNotFound = "DEPLOY_RUN_NOT_FOUND"
	// DiagDeployFailed: the watched run concluded without success.
	DiagDeployFailed = "DEPLOY_FAILED"
	// DiagNotDeployed: finish was asked before a successful deploy of the
	// branch head — the fix is `hotfix deploy`.
	DiagNotDeployed = "NOT_DEPLOYED"
	// DiagForwardConflict: the forward-port (PR merge or cherry-pick) hit
	// conflicts — the fix names the worktree to resolve them in.
	DiagForwardConflict = "FORWARD_PORT_CONFLICT"
	// DiagAlreadyFinished: the PR is merged already — nothing left but cleanup.
	DiagAlreadyFinished = "ALREADY_FINISHED"
	// DiagHeadMoved: a deploy succeeded, but the branch has newer commits
	// than the deployed sha — info; deploy again or finish with what shipped.
	DiagHeadMoved = "HEAD_MOVED_SINCE_DEPLOY"
)

func diag(code, detail, fix string) runx.DiagError {
	return runx.DiagError{Diag: runx.Diagnostic{Code: code, Severity: "error", Detail: detail, Fix: fix}}
}

func warn(code, detail, fix string) runx.Diagnostic {
	return runx.Diagnostic{Code: code, Severity: "warning", Detail: detail, Fix: fix}
}

func info(code, detail, fix string) runx.Diagnostic {
	return runx.Diagnostic{Code: code, Severity: "info", Detail: detail, Fix: fix}
}

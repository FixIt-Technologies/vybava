---
name: merge
description: "Manual only: use when explicitly invoked as `/git:merge` to verify a pull request's safety, CI, and approval gates, merge it, and perform only clearly configured post-merge cleanup."
---

# Safely merge a pull request

The explicit invocation authorizes merging the selected pull request after all
gates pass. It does not authorize bypassing protection, fabricating approval,
discarding work, or deleting resources outside that PR's isolated worktree.

## Preflight

Resolve the PR, full URL, head branch, base branch, checkout/worktree, review
decision, unresolved threads, mergeability, and required checks. Stop when:

- the checkout is on the default branch or does not match the PR head;
- the worktree contains unrelated or uncommitted changes not covered by the
  user's request;
- required CI is failing or pending;
- review changes remain unresolved or required human/bot approval is missing;
- the PR is draft, closed, conflicting, or otherwise not mergeable.

Use the repository's documented PR/review workflow to resolve gates when it is
safe and within scope. Re-run the entire preflight after every head change.
Bound this convergence loop to six iterations.

## Merge and cleanup

Use the repository's configured merge strategy; otherwise use a merge commit.
Never pass an administrative bypass unless the user explicitly requested it.

After GitHub confirms the merge, run a repository-provided post-merge cleanup
command when one is documented. Otherwise remove only a clean, isolated
worktree and its merged local branch. Move the session out of a worktree before
removing it. Never delete Docker volumes, kill broad process sets, switch a
dirty primary checkout, or infer cleanup authority from the merge itself.

Finish with the full merged PR URL, merge commit, cleanup performed or skipped,
and the current working directory.

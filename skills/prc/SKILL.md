---
name: prc
description: "Manual only: use when explicitly invoked as `/prc` to create or find the pull request for the current branch and then resolve its review feedback until it is ready to merge."
---

# Create and monitor a pull request

The explicit invocation authorizes creating a pull request for the current
feature branch and performing the scoped review-resolution work described
below. It does not authorize merging.

1. Verify the checkout is clean enough to publish, is not on the default
   branch, and has an upstream branch. Never switch the user's primary checkout
   or publish unrelated local changes.
2. Find an open pull request for the exact head branch. If one exists, reuse it.
   Otherwise push the branch normally and create a ready-for-review PR with a
   concise title and body derived from the actual diff and verification.
3. Print the full pull-request URL.
4. Continue with the installed `prm` skill for that PR. If `prm` is unavailable,
   follow its core contract: verify every finding, resolve valid issues in
   bounded rounds, push back on invalid claims with evidence, and stop when the
   PR is ready or genuinely blocked.

Never create a PR from the default branch, force-push, self-approve, or merge as
part of this workflow.

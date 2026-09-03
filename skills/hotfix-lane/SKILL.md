---
name: hotfix-lane
description: "Use when production needs a fix that main cannot ship — 'hotfix', 'patch the last release', 'fix prod without shipping main', 'branch from the tag', or when a release-lineage hotfix branch (hotfix/<slug>) must be driven to deploy and forward-port. Trunk-based repos only; the repo must carry hotfix.yaml (run `hotfix init` if not)."
---

# hotfix-lane

The `hotfix` applet (Výbava) owns the whole lane: the branch cut from the
release tag production runs, the isolated worktree, the PR to main, the
production deploy dispatched ON the branch, and the merge that forward-ports
the fix. Re-derives state from git + gh on every call; the skill only orders
the verbs.

## Protocol

```sh
hotfix start <slug> --json            # cut hotfix/<slug> from the latest release tag (or --from vX.Y.Z) + worktree
# … commit the fix in the printed worktree — ONLY the fix …
hotfix pr <slug> --json               # push + open the PR to main (CI gate AND forward-port)
hotfix deploy <slug> --watch --json   # dispatch production on the branch; the lane tags the patch release there
hotfix finish <slug> --json           # after a green deploy: merge the PR; the merge commit carries the tag to main
hotfix status <slug> --json           # anytime: phase + the exact next command
hotfix forward <slug> --json          # only when finish reports FORWARD_PORT_CONFLICT: cherry-pick worktree off main
```

Every verb emits `{v, ok, verb, data, diagnostics, next}` under `--json`.
**The envelope's `next` field IS the protocol** — run what it says,
verbatim, on success and failure alike; diagnostics carry a closed code and
an exact fix. A failure without an actionable diagnostic and `next` is a
CLI bug: fix it in Výbava (`internal/hotfix/`) — never investigate around it.

## Hard laws

1. **Never merge main into a hotfix branch.** The branch must contain only
   the fix on top of the release tag; `LINEAGE_LEAK` blocks pr/deploy/finish
   and `next` carries the rebase that repairs it. Unreleased main work does
   not ship as a "hotfix".
2. **Production deploys from the hotfix branch, never from main.** `hotfix
   deploy` is the only way to dispatch; the workflow computes the version
   from the tag reachable from THAT branch.
3. **The PR is not optional.** It is the CI gate before deploy and the
   forward-port after. `--force` exists for an outage where CI is the thing
   that is broken; say so in the hand-back when used.
4. **A conflicting forward-port is resolved in the `forward` worktree, not
   on the hotfix branch.** The hotfix branch stays deployable as-is.
5. **`finish` merges only after the branch head shipped.** Merging first
   would let a later main release re-ship the fix untested.
6. **Worktrees are created by the project's own command** (`worktree.create`
   in `hotfix.yaml`), never bare `git worktree add`, so ports/env/deps come
   along. Cleanup is the last `next` after finish; run it.

Per-project notes: the repo's `hotfix.yaml` and its CLAUDE.md pointer.

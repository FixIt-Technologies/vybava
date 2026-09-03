# hotfix — release-lineage hotfixes for trunk-based repos

Trunk-based development with tagged releases has one weak spot: when
production needs a fix and main already carries unreleased work, the fix
must be built on the tag, deployed from a branch, and brought back to main.
Done by hand that is five decisions and three places to get wrong. `hotfix`
makes it one deterministic lane.

```text
v3.6.51 ──●──●──●──●── main (unreleased work)
          └──●── hotfix/sms-token ──▶ deploy-production --ref hotfix/sms-token ──▶ v3.6.52
                  └──────── PR → main (merge commit = forward-port; tag becomes reachable)
```

## Verbs

| Verb | Does | Refuses (closed code) |
|---|---|---|
| `init` | writes a commented `hotfix.yaml` | — |
| `start <slug> [--from vX.Y.Z]` | branch `<prefix><slug>` from the highest stable tag; worktree via the project's create command | `TAG_MISSING`, `SLUG_INVALID` |
| `status [slug]` | re-derives phase + `next`; slug inferred from a `hotfix/*` cwd | — |
| `pr [slug]` | pushes, opens/reuses the PR to main with the `hotfix` label | `NO_COMMITS`, `LINEAGE_LEAK` |
| `deploy [slug] [--watch] [--force]` | `gh workflow run <deploy.workflow> --ref <branch>` + configured inputs; never double-dispatches | `UNPUSHED`, `PR_MISSING`, `CI_RED`, `LINEAGE_LEAK` |
| `finish [slug] [--force]` | merges the PR once the head shipped (`--merge` keeps the tag reachable) | `NOT_DEPLOYED`, `FORWARD_PORT_CONFLICT` → `forward` |
| `forward [slug]` | cherry-pick worktree off main for a conflicting forward-port | stops on conflict with the resolve commands |

Phases (`data.phase`): `MISSING → EMPTY → UNPUSHED → NO_PR → READY →
DEPLOYING → DEPLOYED → FINISHED`, plus `LEAKED` when main was merged in.

## Lineage purity

The merge-base of the hotfix branch and main must not be past the base tag.
If it is, main leaked in and the branch would ship unreleased work; every
mutating verb refuses with `LINEAGE_LEAK` and `next` carries
`git rebase --onto <tag> <merge-base> <branch>`.

## What the deploy workflow must do

- accept `workflow_dispatch` on any ref and deploy `github.sha`;
- compute the next version from the nearest stable tag **reachable from
  HEAD** (`git describe --tags --abbrev=0 --match 'v[0-9]*' --exclude '*-*'`),
  not the globally highest tag, and step past an existing tag instead of
  aborting — on main that also skips over hotfix tags cleanly;
- tag the deployed sha on success.

FixIt's `deploy-production.yml` is the reference.

## hotfix.yaml

```yaml
v: 1
default_branch: main
tag_glob: "v[0-9]*"
branch_prefix: hotfix/
worktree:
  name: "hotfix-{slug}"
  path: ".worktrees/{name}"
  create: "bun run worktree:create {name} --no-clone-db --from {from} --branch {branch}"
  cleanup: "/wk:cleanup {name} --remove --yes --delete-remote"
deploy:
  workflow: deploy-production.yml
  inputs: { release_type: patch, skip_tests: "false" }
pr:
  labels: [hotfix]
  merge_flags: ["--merge", "--admin"]
```

Templates expand `{slug} {name} {branch} {from} {path} {root}`; `create`
runs in the primary checkout and must leave `{branch}` checked out at
`{path}` (an existing local branch is reused — FixIt's script does that).

## Contract

`internal/hotfix/` is envelope-first per the `cli-craft` skill: verbs return
`Result`/`DiagError`, `internal/runx` emits exactly one envelope and owns
the exit code (0 ok, 1 infra, 2 diagnostics). Codes are the closed enum in
`internal/hotfix/diag.go`. `internal/cli/hotfix_test.go` walks every verb
on a forced failure; `internal/hotfix/hotfix_test.go` runs the whole lane
against real temp git repositories with a scripted `gh`.

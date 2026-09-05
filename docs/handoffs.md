# handoffs — handoff ledger upkeep

`~/.claude/handoffs/` accumulates `/handoff` runbooks faster than `/continue`
closes them: hundreds sit at `status: open` long after their branch merged.
`handoffs reconcile` decides which of them are still live and archives the
rest, so the ledger's first digest is signal.

```sh
handoffs reconcile                       # dry run: table + summary
handoffs reconcile --project fixit       # one project slug
handoffs reconcile --apply               # archive the dead ones
handoffs reconcile --stale-days 30       # be more patient with evidence-less handoffs
handoffs reconcile --home <dir> --json   # stable report for agents
```

Scope: every `<project>/<slug>.md` and `<project>/<slug>/handoff.md` whose
frontmatter status is `open` or `in-progress`. `archive/` and context files
are never read.

## Evidence

Only the handoff **header** — everything above the first `## ` heading — is
evidence. From its `**Branch:**` / `**Branches…:**` line (plus wrapped lines):
`branch @ sha`, `` `branch` @ `sha` ``, `` repo `branch` ``, `` `repo@branch` ``,
`` `branch` — repo @ sha · repo2 @ sha ``, and the word MERGED. From the header text: `PR #N`, `owner/repo#N`,
`github.com/owner/repo/pull/N`. PRs named below the first heading ("start after
PR #644 merges") are reported as `mentions` and never make a verdict dead.

A repo name or the handoff's project slug resolves to a checkout through the
Path column of `~/.claude/docs/timesheet-repo-registry.md` (basename
slugified), then a directory under `~/Work/Projects` (depth ≤ 4). A bare
`PR #N` takes its repo from that checkout's `origin`.

## Verdicts

- **live** — any branch exists locally, on `origin` (as last fetched — the
  tool never fetches) or in a worktree; or any PR is OPEN.
- **dead** — every branch is gone and every PR is MERGED/CLOSED; or the
  Branch line says MERGED with nothing else alive; or there is no evidence
  at all (no Branch line, or only `main`/`master`) and the file has not been
  touched for more than `--stale-days` (14).
- **unknown** — a repo could not be resolved, `gh` could not answer for a
  PR, or there is no evidence yet the file is recent. Never archived.

`--apply` rewrites only the frontmatter `status:` line to `abandoned` and
moves the file (or the whole `<slug>/` directory) under `<project>/archive/`,
suffixing `-YYYYMMDD` when the name is taken. Nothing is ever deleted.

The `--json` shape: `{items: [{path, project, slug, status, verdict, reason,
branches, prs, mentions, archived?}], summary: {live, dead, unknown, archived}}`.

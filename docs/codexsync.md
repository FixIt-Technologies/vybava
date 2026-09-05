# codexsync

Render the Claude Code personal home into the structure Codex actually
discovers, deterministically, so one set of skills and commands serves both
runtimes.

```sh
codexsync plan            # what would change, touching nothing
codexsync apply           # render, prune, update config.toml
codexsync apply --dry-run # the same, without writing
codexsync check           # exit 1 when the rendered tree has drifted
```

Every verb takes `--json` and honours `--claude-home`, `--agents-home`,
`--codex-home`, `--backup-root`. Relative overrides resolve against the current
directory. The homes must not overlap, and the Claude home must exist.

`plan --json` includes the rendered entries and the same change report as
`apply --dry-run --json`, including config and manifest changes. Operational
errors and drift produce `{"status":"error","error":"..."}` on stdout and a
non-zero exit code. Successful applies report empty change lists as `[]`.

## What Codex actually supports

This is the part worth internalising, because most of the confusion around
sharing skills between the two runtimes comes from assuming symmetry that does
not exist.

**Claude command files need conversion.** Claude Code's
`~/.claude/commands/*.md` is not a Codex skill surface.
Prompts (`~/.codex/prompts/*.md`) are a separate, flat mechanism —
Codex does not derive slash commands from a `SKILL.md`
([openai/codex#13893](https://github.com/openai/codex/issues/13893)). A prompt
entry that is a *directory*, or a dangling symlink, is invisible.

**A skill is a directory holding `SKILL.md`** with `name` and `description` in
frontmatter. Discovery scopes, per
[the build-skills doc](https://learn.chatgpt.com/docs/build-skills):

| Scope | Path |
|---|---|
| Repository | `$CWD/.agents/skills`, walking up to `$REPO_ROOT/.agents/skills` |
| **User** | **`$HOME/.agents/skills`** |
| Admin | `/etc/codex/skills` |
| System | shipped with Codex |

`$HOME/.agents/skills` is the user scope codexsync renders into.

**Nesting works.** Codex treats *any* directory in the tree containing a
`SKILL.md` as its own skill, so `git/commit/SKILL.md` and `git/merge/SKILL.md`
are two skills under one bundle. Claude's nesting survives the crossing
untouched.

**Duplicate discovery is real.** Codex versions with legacy-compatible Claude
paths can show the same skill several times in the picker. The fix is
`[[skills.config]]` entries in `~/.codex/config.toml` with `enabled = false` —
hand-maintaining those is the part that rots, so codexsync owns them.

**Implicit invocation has a Codex equivalent.** A skill directory may carry
`agents/openai.yaml` with `policy.allow_implicit_invocation`. That is the
counterpart of Claude's `disable-model-invocation` frontmatter.

## What it renders

| Source | Becomes |
|---|---|
| `~/.claude/skills/<name>/**` | `~/.agents/skills/<name>/**`, copied whole — nesting, `references/`, `scripts/` and all |
| `~/.claude/commands/<path>.md` | `~/.agents/skills/source-command-<path-with-dashes>/SKILL.md` |
| `disable-model-invocation: true` | `agents/openai.yaml` with `allow_implicit_invocation: false` |
| a duplicate `SKILL.md` on a `.claude` / `.codex` path | a `[[skills.config]] enabled = false` entry in the managed config block |

A command's skill takes its `description` from the command's frontmatter, or
falls back to the first prose line — Codex needs a description to decide
relevance, so an empty one is never emitted.

Commands flatten (`me/timesheet/backfill.md` →
`source-command-me-timesheet-backfill`) because Codex has no command namespace
to mirror, and a flat name is what gets typed and grepped.

Collisions between flattened commands, or between a command and a copied skill,
fail the plan with both source paths. Rename one source before applying.
YAML descriptions support quoting, comments, and multiline values. Invocation
opt-outs merge into existing `agents/openai.yaml` metadata without removing UI
settings or tool dependencies.

A directory under `~/.claude/skills` that holds no `SKILL.md` anywhere is not a
skill and is skipped.

## Ownership and safety

Copies are **full and generated** — never symlinks. Edit the Claude side and
re-run; edits to managed files in the rendered tree are backed up and overwritten.
Source symlinked directories are materialized, including top-level bundles and
repeated aliases. File permissions (including executable scripts) are preserved.
Broken source links and unreadable files fail the plan rather than silently
omitting their content.

`~/.agents/skills/.codexsync.json` records exactly what the last run produced.
Pruning removes only recorded files and then empty directories. Hand-written
files added inside a generated directory survive its retirement too. Existing
unmanaged files at a planned destination, and destination symlinks, cause an
error: move them aside before applying. A corrupt or unsupported manifest also
stops the apply before anything is changed.

Anything about to be displaced — edited or retired managed files, config,
manifest, unreadable prompt entries — is copied to a unique
`~/Backups/codexsync/backup-<random>/` directory first. Backup directories are
private; file permissions and symlinks are preserved. Backup errors abort the
apply. Each output file is replaced atomically; the complete run is not a
filesystem transaction, so keep the source stable and run one apply at a time.

Legacy prompt cleanup is separate from manifest ownership: visible directories,
non-Markdown files, and dangling links directly under `~/.codex/prompts` are
backed up and removed. Keep other personal files outside that legacy prompt tree.

In `~/.codex/config.toml` only the region between the `codexsync managed`
markers is rewritten. Every hand-written setting around it is left alone, and a
second run replaces the block rather than stacking another copy.
Malformed or unmatched markers cause an error, never truncation of the file.
Legacy Codex skills are suppressed only when the relative path and `SKILL.md`
content match; a different Codex variant remains enabled.

Restart Codex after an apply — skills load at startup.

## Drift

`codexsync check` exits non-zero for missing, changed or orphaned managed files,
file-mode changes, config or manifest drift, and stale prompt entries.
Because the render is deterministic, this is safe to run in CI or from a
`doctor` sweep.

Repository verification runs remotely with `devbox run verify`; the Go tests
use temporary homes and never apply to the developer's actual agent homes.

## Known limitations

Suppression entries written into `~/.codex/config.toml` *before* codexsync
existed sit outside the managed markers and are left alone. They are harmless —
they name the same paths with the same `enabled = false` — but they are not
pruned when a skill is retired. Delete them by hand once, and the managed block
carries it from then on.

A `source-command-*` directory in `~/.agents/skills` whose Claude command no
longer exists is unmanaged if codexsync never generated it, so it survives.
Check for orphans with:

```sh
ls -d ~/.agents/skills/source-command-* | while read -r d; do
  grep -q "\"$(basename "$d")\"" ~/.agents/skills/.codexsync.json || echo "orphan: $d"
done
```

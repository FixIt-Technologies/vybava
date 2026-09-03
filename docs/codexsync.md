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
`--codex-home`, `--backup-root`.

## What Codex actually supports

This is the part worth internalising, because most of the confusion around
sharing skills between the two runtimes comes from assuming symmetry that does
not exist.

**Codex has no commands.** Claude Code's `~/.claude/commands/*.md` has no
counterpart. Prompts (`~/.codex/prompts/*.md`) are a separate, flat mechanism —
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

**Duplicate discovery is real.** Codex also scans legacy-compatible Claude
paths, so the same skill can appear several times in the picker. The fix is
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

A directory under `~/.claude/skills` that holds no `SKILL.md` anywhere is not a
skill and is skipped.

## Ownership and safety

Copies are **full and generated** — never symlinks. Edit the Claude side and
re-run; edits to the rendered tree are overwritten.

`~/.agents/skills/.codexsync.json` records exactly what the last run produced.
Pruning only ever removes paths that manifest claims, so a skill you wrote by
hand into `~/.agents/skills` survives every run untouched.

Anything about to be displaced — retired skills, unreadable prompt entries — is
copied to `~/Backups/codexsync/<timestamp>/` first. Symlinks are preserved as
`<name>.symlink` files recording their target, so a legacy layout stays
reconstructible.

In `~/.codex/config.toml` only the region between the `codexsync managed`
markers is rewritten. Every hand-written setting around it is left alone, and a
second run replaces the block rather than stacking another copy.

Restart Codex after an apply — skills load at startup.

## Drift

`codexsync check` exits non-zero listing every missing, changed or orphaned
path. Because the render is deterministic, this is safe to run in CI or from a
`doctor` sweep.

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

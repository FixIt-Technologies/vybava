# memorylint — AI memory home linting and maintenance

Understands Obsidian-style Markdown memory homes. `check` verifies frontmatter
schema and allowed types, kebab-case filenames, duplicate names, index/entry
line caps, missing index targets, dangling `[[wikilinks]]`, memories absent
from `MEMORY.md`, and unallowlisted email/IP fixture values.

## Lifecycle

Memory that never changed behaviour is context tax, so `check` also enforces
how notes age:

- a `provisional` note must carry `expires: YYYY-MM-DD`; promoting it to
  `active` must drop the field (error either way);
- an expired provisional is reported as deletable on sight;
- a note whose newest `last-used` / `last-verified` signal is over 90 days old
  is an eviction candidate (warning; undated notes are skipped, since no date
  is not evidence of disuse);
- a home over its note ceiling — 15 for personal (`user`/`feedback` only), 30
  otherwise — is flagged to consolidate or evict, never to raise the cap.

## Commands

```sh
memorylint check <home>                 # the rules above
memorylint fix [--dry-run] <home>       # normalize notes onto the flat v2 schema
memorylint new --home <home> --type project --name project-topic --description "Use when …"
memorylint new --provisional …          # status: provisional + expires 60 days out
memorylint reindex [--write] <home>     # render MEMORY.md deterministically
memorylint refs [--bare] <home> <file>… # references to notes that no longer exist
memorylint graph [--similar] <home>…    # wikilink graph, or likely-duplicate pairs
memorylint hook                         # Claude Code / Codex write hook
```

All commands honor `--json`. `reindex --team-index <path>` adds the routing
line pointing at the companion team home. `check` only walks the home; `refs`
covers pointers *into* the home from code and docs (`--bare` widens matching
to unqualified `<note>.md` names).

## Write hooks

`memorylint hook` reads a Claude Code / Codex hook payload on stdin, exits 0
outside memory homes, exits 2 to refuse a write. It refuses two things:

- **secrets**, before they are written;
- **the Edit/Write tools inside the agent-managed home**
  (`~/.claude/projects/<slug>/memory/`) — Claude Code normalizes frontmatter
  there *after* the post-write hook (flat v2 → nested `metadata:` envelope),
  which no in-session check can detect. The only reliable defence is refusing
  the tool and sending the caller to Bash or `memorylint new`. Team homes are
  unaffected; `memorylint fix` repairs already-drifted notes.

## Configuration

`.memorylint.yaml` at the lint root:

```yaml
version: 1
max_index_lines: 100
max_entry_lines: 150
allowed_emails: [qa-*@example.test]
allowed_ips: [192.0.2.*]
allowed_values: [fixture-token-value]
ignore: [archive/**]
```

# Výbava

Výbava is FixIt Technologies' portable engineering environment: small tools,
agent skills, conventions, and diagnostics that can be installed independently
on a workstation or into a project.

## Design

The repository is a catalog, not a monolith. Each item is independently
installable. Groups such as `recommended` and `experimental` are ordinary,
composable catalog presets, so creating another workstation profile never
requires changing installer code.

```text
catalog/catalog.yaml      package and group source of truth
cmd/vybava/               management CLI and multicall entrypoint
internal/                 focused Go modules for catalog/install/doctor/tools
skills/<id>/              canonical cross-agent skill payloads
docs/decisions/           architectural decisions and extension contracts
```

## Quick start from source

```sh
go build -o ./bin/vybava ./cmd/vybava
./bin/vybava catalog list
./bin/vybava install recommended
./bin/vybava install experimental --agent all
./bin/vybava doctor
```

## Playwright MCP: one pin, one browser registry

`pwmcp` makes every Playwright MCP server on the workstation resolve the same
`@playwright/mcp` version and share one browser registry that lives outside the
OS cache directory:

```sh
vybava install pwmcp
pwmcp install          # pinned server + the browser revisions it needs
pwmcp status           # pin, registry location, orphaned revisions
pwmcp prune            # delete only what no installed pin still needs
```

Point project MCP configs at it instead of a floating tag:

```json
{ "mcpServers": { "playwright": { "command": "pwmcp", "args": ["serve", "--headless"] } } }
```

Arguments are forwarded to the server untouched, and `--isolated` is prepended
unless they already choose a profile. Profiles are isolated per server; the
browser binary is shared on purpose — duplicating it is what multiplies
downloads. See `docs/decisions/0003-pinned-playwright-mcp.md`.

## Install with Homebrew

Výbava is public; its Homebrew tap is still private, so Homebrew needs a token
that can read the tap's release assets:

```sh
gh auth setup-git
export HOMEBREW_GITHUB_API_TOKEN="$(gh auth token)"
brew install --cask FixIt-Technologies/tap/vybava
```

Tagged releases publish the cross-platform archives and update
`FixIt-Technologies/homebrew-tap` automatically. Subsequent upgrades use:

```sh
brew upgrade --cask vybava
```

`install` defaults to the `recommended` group when no selector is supplied.
Selectors can be mixed:

```sh
vybava install memorylint prm
vybava install recommended experimental
vybava install ai-git --agent codex
```

Skills support `--agent claude`, `--agent codex`, or `--agent all`. The default
scope is `user`; use `--scope project` for repository-local installation. Use
`--dry-run` to preview mutations and `--json` for agent/automation consumption.

Installed applets are lightweight links to the `vybava` executable. Therefore
both forms are equivalent after installing `memorylint`:

```sh
vybava memory lint .
memorylint .
```

## Memorylint

Memorylint understands Obsidian-style Markdown and checks:

- required YAML frontmatter and allowed memory types;
- kebab-case filenames and duplicate memory names;
- memory/index line caps;
- missing index targets and dangling `[[wikilinks]]`;
- memories missing from their local `MEMORY.md` index;
- email/IP fixture values that are not explicitly allowlisted.

Beyond checking, it maintains memory homes:

```sh
memorylint check <home>                 # the rules above
memorylint fix [--dry-run] <home>       # normalize notes onto the flat v2 schema
memorylint new --home <home> --type project --name project-topic --description "Use when …"
memorylint reindex [--write] <home>     # render MEMORY.md deterministically
memorylint refs [--bare] <home> <file>… # find references to notes that no longer exist
memorylint graph [--similar] <home>…    # wikilink graph, or likely-duplicate pairs
memorylint hook                         # run as a Claude Code / Codex write hook
```

`reindex --team-index <path>` adds the routing line pointing at the companion
home, which is what keeps a personal index telling readers where project and
reference memory lives. Every command above honours `--json`.

`check` only ever walks the home, so it cannot see a source comment or a design
doc pointing at a note that has been merged away — `refs` covers that half.
`--bare` widens it from `<home>/<note>.md` paths to unqualified `<note>.md`
names.

### Write hooks

`memorylint hook` reads a Claude Code or Codex hook payload on stdin, exits 0
for anything outside a memory home, and exits 2 to refuse the write. Register it
pre- and post-write in `~/.claude/settings.json`, `~/.codex/hooks.json`, and the
equivalents inside a repo that carries a committed team home.

It refuses two things:

- **secrets**, before they are written; and
- **the Edit/Write tools inside the agent-managed home**
  (`~/.claude/projects/<slug>/memory/`). Claude Code normalizes frontmatter it
  writes there, silently reverting the flat v2 properties to a nested
  `metadata:` envelope and stamping `originSessionId`/`modified`. That rewrite
  lands *after* the post-write hook, so nothing can detect it in-session — the
  only reliable defence is to refuse the tool and send the caller to Bash or
  `memorylint new`. Writes to a committed team home are unaffected, and
  `memorylint fix` repairs a note that already drifted.

Place `.memorylint.yaml` at the lint root to override limits or allow fixtures:

```yaml
version: 1
max_index_lines: 100
max_entry_lines: 150
allowed_emails:
  - qa-*@example.test
allowed_ips:
  - 192.0.2.*
allowed_values:
  - fixture-token-value
ignore:
  - archive/**
```

## Extending Výbava

See [`docs/decisions/0001-modular-catalog.md`](docs/decisions/0001-modular-catalog.md).
The short version: implement or add one payload, register one item, and add its
ID to a group only when the preset should include it. Catalog validation and CI
catch broken sources, group references, duplicate IDs, and invalid statuses.

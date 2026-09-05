| `handoffs` | applet | Handoff ledger upkeep — `handoffs reconcile` judges every open handoff by whether its branches and PRs are still alive and archives the dead ones; unknown is never touched. → [docs/handoffs.md](docs/handoffs.md) |
# Výbava

Výbava is FixIt Technologies' portable engineering environment: small tools,
agent skills, and workstation diagnostics, distributed as one catalog where
every item installs independently.

## Packages

| ID | Kind | What it does |
|---|---|---|
| `memorylint` | applet | Validate and maintain AI memory homes — schema, indexes, wikilinks, fixtures, write hooks. → [docs/memorylint.md](docs/memorylint.md) |
| `shrt` | applet | Terminal-safe short links on luko.to — offline repo rules, team-shared dynamic rules, minted codes; also the redirector server. → [docs/shrt.md](docs/shrt.md) |
| `fontfreeze` | applet | Freeze variable webfonts at rendered axis positions and subset per language. |
| `perfrig` | applet | Performance drills from a `testing/<project>/perf` manifest — ramp to first failure, percentile report. |
| `ingressgen` | applet | Render and drift-check complete default-deny Docker ingress policies from a manifest. |
| `reconcile` | applet | Pull-based GitOps for the infra boxes — converge a VPS to its infra repo's merged main from a per-box manifest: HELD hotfixes, transactional nginx hooks, commit rollback, textfile metrics, mesh-only status page + estate hub. → [docs/reconcile.md](docs/reconcile.md) |
| `prm` / `prc` / `merge` | skills | PR create → review-resolve → gated merge workflows for Claude Code and Codex. |
| `codexsync` | applet | Render `~/.claude` skills and commands into `~/.agents/skills`, the structure Codex discovers — nesting preserved, each command a `source-command` skill, duplicate discovery suppressed. → [docs/codexsync.md](docs/codexsync.md) |
| `press` | applet | Deterministic state for the document family — project resolution, `~/Exports/<project>/` config and index, ARES lookups, shared doctrine. → [docs/press.md](docs/press.md) |
| `press-pdf` / `press-logo` / `press-offer` / `press-email` | skills | Offer, documentation and legal PDFs; brand marks; Czech commercial DOCX; Outlook-paste client emails. Issuer identity stays machine-local. → [docs/press.md](docs/press.md) |

Groups (`recommended`, `experimental`, `ai-git`, `press-family`, `everything`)
are composable presets in the catalog — never code.

## Install

Homebrew (everything is public — no authentication needed):

```sh
brew install --cask FixIt-Technologies/tap/vybava
```

From source:

```sh
go build -o ./bin/vybava ./cmd/vybava
./bin/vybava catalog list
./bin/vybava install recommended
```

`install` takes item or group selectors (default: the `recommended` group)
and supports `--agent claude|codex|all`,
`--scope user|project`, `--dry-run`, and `--json`. Installed applets are links
to the `vybava` binary, so `vybava memory lint .` and `memorylint .` are
equivalent.

## Layout

```text
catalog/catalog.yaml   package and group source of truth
cmd/vybava/            multicall entrypoint
internal/<id>/         one focused Go package per capability
skills/<id>/           canonical cross-agent skill payloads (SKILL.md plus any
                       references/ and assets/ the skill ships)
docs/                  per-tool references, release flow, decisions
Dockerfile             the luko.to redirector image (deployik app "luko")
```

## Extending

One payload + one catalog entry = one package; presets are one more catalog
line. Contract: [docs/decisions/0001-modular-catalog.md](docs/decisions/0001-modular-catalog.md)
· checklist: [CONTRIBUTING.md](CONTRIBUTING.md) · releases: [docs/homebrew.md](docs/homebrew.md).

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
| `prm` / `prc` / `merge` | skills | PR create → review-resolve → gated merge workflows for Claude Code and Codex. |

Groups (`recommended`, `experimental`, `ai-git`, `everything`) are composable
presets in the catalog — never code.

## Install

Homebrew (repo and release assets are public; the tap is still private, so
Homebrew needs Git credentials for it):

```sh
gh auth setup-git
export HOMEBREW_GITHUB_API_TOKEN="$(gh auth token)"
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
skills/<id>/           canonical cross-agent skill payloads
docs/                  per-tool references, release flow, decisions
Dockerfile             the luko.to redirector image (deployik app "luko")
```

## Extending

One payload + one catalog entry = one package; presets are one more catalog
line. Contract: [docs/decisions/0001-modular-catalog.md](docs/decisions/0001-modular-catalog.md)
· checklist: [CONTRIBUTING.md](CONTRIBUTING.md) · releases: [docs/homebrew.md](docs/homebrew.md).

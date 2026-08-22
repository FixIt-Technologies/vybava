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

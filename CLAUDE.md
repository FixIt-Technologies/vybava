# Výbava

Výbava is FixIt Technologies' modular distribution hub for small engineering
utilities, reusable agent skills, and workstation diagnostics.

## Architecture laws

- `catalog/catalog.yaml` is the package and group source of truth.
- One item owns one capability. Groups only compose item IDs; group behavior is
  never hard-coded into the CLI.
- Agent skills have one canonical `SKILL.md` payload under `skills/<id>/`.
  Installer adapters copy that payload into Claude Code or Codex homes.
- Human-readable output is the default. Every command used by automation must
  support stable JSON and non-interactive execution.
- The `vybava` binary is multicall: installed applets are links to the same
  executable and dispatch by `argv[0]`.
- Utility implementations stay in focused `internal/<utility>/` packages.
  CLI wiring must not contain domain logic.
- Adding a package should normally require its payload/implementation and one
  catalog entry. Adding it to a preset is one additional catalog line.

## Commands

```sh
go test ./...
go vet ./...
go run ./cmd/vybava catalog list
go run ./cmd/vybava doctor
go run ./cmd/vybava memory lint <path>
```

Run `go fmt ./...` after Go edits. Use Go for utilities and system automation;
do not introduce Python helpers.

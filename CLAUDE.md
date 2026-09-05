# Výbava

FixIt Technologies' modular distribution hub for small engineering utilities,
reusable agent skills, and workstation diagnostics. Per-tool references live
in `docs/`; lessons in `.claude/memory/MEMORY.md`.

## Architecture laws

- `catalog/catalog.yaml` is the package and group source of truth. One item
  owns one capability; groups only compose item IDs — group behavior is never
  hard-coded into the CLI. Adding a package = its payload + one catalog entry;
  a preset membership is one more catalog line.
- The `vybava` binary is multicall: installed applets are links dispatching by
  `argv[0]`. Implementations stay in focused `internal/<id>/` packages; CLI
  wiring carries no domain logic.
- Agent skills have ONE canonical `SKILL.md` under `skills/<id>/`; installer
  adapters copy it into Claude Code or Codex homes — never per-agent forks.
- Human-readable output is the default; every automation-facing command must
  support stable `--json` and non-interactive execution.
- Tagged releases publish a Homebrew cask via the tap deploy key — read
  `docs/homebrew.md` before touching release distribution.
- `ci/` is the ONLY pipeline-facing surface: CI images, workflows and
  provisioning scripts install a tagged release through `ci/install.sh` and
  never check this repository out (`scripts/`, `skills/`, `docs/` and the Go
  sources are internal). Contract + consumers: `ci/README.md`; tests in
  `internal/ciinstall`. Pins move only after a release is cut.
- The repo-root `Dockerfile` is the luko.to redirector image (deployik app
  `luko`, build context = Dockerfile's directory, so it must stay at the
  root). `internal/shrt/rules.go` ships in BOTH the CLI and the server —
  changing it means redeploying luko. Ops details: `docs/shrt.md`.

## Commands

```sh
go test ./...  &&  go vet ./...
go run ./cmd/vybava catalog list
go run ./cmd/vybava doctor
```

Run `go fmt ./...` after Go edits. Utilities are Go — never Python helpers.

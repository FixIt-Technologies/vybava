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

Run verification remotely with `devbox run verify` using `devbox.yaml`. Its
`repo` app holds the CLI test workspace open; its reserved port serves no UI.
Exception: Lukáš authorized local verification for the operator trial on
2026-09-06; Devbox capacity must not block that trial.
`internal/codexsync/storage.go` owns codexsync's destination validation,
atomic file writes, and empty-directory cleanup; rendering stays in
`internal/codexsync/codexsync.go`. See `docs/codexsync.md` for ownership rules.

`internal/operator` owns the local Codex operator trial: incremental Claude/Codex
observations, durable delivery receipts, revision-bound proposals and actual
human scores. `docs/operator.md` documents its CLI and the human-only send rule.
`internal/operator/companion.go` owns the native companion snapshot and free-text
feedback contract; source scan time is separate from snapshot read time.
`history.go` owns stable observation pagination and immutable, revision-bound
human review decisions. Approving a draft records review only; it never executes.
`sqlite.go` owns explicit JSON-to-SQLite migration, indexed event/history queries
and content revisions independent of heartbeat metadata. The v5 JSON marker blocks
legacy writers after migration; preserve the private pre-migration archive.
`outcome.go` records execution/verification evidence separately from approval.
`messages_reader.go` owns read-only SQLite metadata and pinned imsg content reads;
`messages.go` owns baseline, changes, removal, retry and per-source coverage.
Agent scan time is independent of Messages check time. All trial writers must be
upgraded together before a live v4 write; see `docs/operator.md`.
Production operator reads/writes use scoped `Store` APIs (event, queue, metadata,
history, snapshot, revision). `View`/`With` retain full-archive compatibility for
explicit export/tests; do not use them in polling paths.
`Store.Scan` serializes source scans separately and merges observations/cursors
under the writer lock, preserving concurrent feedback. `attention.go` owns the
conservative, freshness-gated Claude attention selection; the CLI only wires it.

`internal/plaud` reads the Plaud account directly (PKCE login, vault-injected
refresh token, cached access token only); the manual-only skill is
`skills/plaud/`. `docs/plaud.md` has the auth model and the API map.

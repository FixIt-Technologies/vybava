# Contributing to Výbava

Keep additions small, independently installable, and catalog-driven.

## Package checklist

1. Choose a stable lowercase kebab-case ID.
2. Add a focused implementation or canonical payload.
3. Register it once in `catalog/catalog.yaml`.
4. Add focused behavior tests.
5. Add it to `experimental` first unless its interface and safety behavior are
   already proven. Promotion to `recommended` is a deliberate catalog-only
   change.
6. Run `go fmt ./...`, `go vet ./...`, `go test ./...`, and
   `goreleaser check`.

Do not fork payloads for individual AI runtimes. Add or extend an installer
adapter when a runtime needs a different destination or metadata wrapper.

An applet must be non-interactive by default or provide an explicit stable
non-interactive mode. Output intended for agents or CI must be available as
JSON, with meaningful exit codes and no terminal decoration.

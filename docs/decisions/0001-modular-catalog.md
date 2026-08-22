# 0001 — Modular catalog and multicall distribution

Status: accepted

## Context

Výbava needs to distribute small utilities and AI skills without forcing every
workstation or project to accept the whole collection. Human operators need a
pleasant interactive interface, while agents and CI need deterministic,
machine-readable behavior.

## Decision

`catalog/catalog.yaml` is the single package graph. Items declare a kind,
lifecycle status, description, and payload. Groups contain item IDs and have no
special behavior in Go code.

The initial item kinds are:

- `applet`: a command implemented inside the multicall Go binary and installed
  as a lightweight executable link;
- `skill`: a canonical Agent Skills-compatible folder copied through a thin
  Claude Code or Codex destination adapter.

The CLI defaults to readable text and exposes JSON on commands intended for
automation. Interactive selection is additive and never required.

## Extension contract

To add an applet:

1. Put its logic in `internal/<id>/` and keep CLI wiring thin.
2. Register its dispatcher in `internal/cli/`.
3. Add one `kind: applet` catalog item.
4. Add focused tests and, if stable enough, one item ID to a group.

To add a skill:

1. Create `skills/<id>/SKILL.md` using Agent Skills-compatible frontmatter.
2. Add `agents/openai.yaml` only when UI metadata is useful.
3. Add one `kind: skill` item whose source is that directory.
4. Validate the skill and add it to a group only deliberately.

Future artifact kinds—standalone release binaries, hooks, templates, or config
profiles—must implement the same plan/apply interface. They must not introduce
kind-specific conditionals into group resolution.

## Consequences

The shipped manager contains all small applet code, but users expose and track
only selected applets. This trades a negligible binary-size increase for one
release, one updater, atomic compatibility, and much simpler recovery.

Skills remain canonical once and are adapted only at installation boundaries,
preventing Claude/Codex copies from drifting.

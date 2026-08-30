# 0003 — Absorb the press family, and keep issuer identity out of the repository

Status: accepted

## Context

The press document family was split across three homes. The `press` CLI and its
doctrine lived in a private `FixIt-Technologies/press` repository. The three
skills that drive it — `press-pdf`, `press-logo`, `press-offer` — lived only in
a personal `~/.claude` checkout and were excluded from its public mirror. The
skills reached the doctrine by absolute filesystem path into the CLI's
repository.

That arrangement had three defects:

- The doctrine path was a cross-repository dependency that broke whenever the
  CLI's checkout moved, which it had already done once.
- The skills existed in exactly one directory on one machine, with no CI, no
  versioning against the binary they call, and no way to install them anywhere
  else.
- The CLI needed its own installer, its own `GOPRIVATE` handling, and its own
  release story to work around being in a private repository.

Výbava already solves all three for every other package it ships.

The blocker was content, not structure. Výbava is public and distributed as a
tokenless Homebrew cask. `press-offer` hardcoded an issuer's registry identity,
its data box, its day rate, and the names and contract values of real clients.

## Decision

Absorb the family as four catalog packages: the `press` applet in
`internal/press`, and `press-pdf` / `press-logo` / `press-offer` as skill
payloads. The standalone repository, its `install.sh`, and its `GOPRIVATE`
workaround are retired.

**The doctrine ships inside the binary.** `internal/press` embeds
`CONVENTIONS.md` and `press.conf.schema.json` and serves them as
`press doctrine` and `press doctrine --schema`. Skills call the command instead
of reading a path, so the family keeps the single enforcement point it was
designed around without a path that can rot.

**Issuer identity is machine-local and never committed.** Company identity,
commercial rate, and brand tokens move out of the payload into a file outside
any checkout — `~/.config/press/identity.json` by default, `PRESS_IDENTITY` to
override, mode 0600. The repository ships the shape and an empty scaffold; the
values never enter git. `press-offer`'s generator reads the file through
`press identity show --json` and refuses to render while a required field is
empty, rather than emitting a document with a blank header.

This is a correctness improvement, not only a privacy one. The family was
previously usable by exactly one issuer; it is now usable by any issuer who
fills in their own file.

**Skill payloads may be directory trees.** `bundle.go` embedded
`skills/*/SKILL.md`, which silently drops anything else. `press-pdf` ships
`references/` and an `assets/` pipeline, so the embed became the whole `skills`
tree. The installer already walked nested payloads correctly, so this was the
only change needed. The bare pattern — rather than `all:skills` — keeps `_` and
`.` prefixed entries such as `__pycache__` out of the binary for free.

## Consequences

- `vybava install press-family` installs the CLI and all three skills, for
  Claude Code or Codex, on any machine.
- The CLI and the skills version together. A schema change and the prose that
  describes it land in one commit and ship in one release.
- A fresh machine needs one manual step the packages cannot do for it:
  `press identity init` and filling in the file. `press identity show` names
  exactly which fields are still empty, and the offer generator fails loudly
  rather than silently producing a blank-header document.
- Any future payload with real client data must be sanitized the same way
  before it enters this repository. Public distribution is the constraint that
  makes the identity indirection mandatory rather than optional.

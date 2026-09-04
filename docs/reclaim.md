# reclaim — emergency disk reclaim

When a dev Mac hits a full disk, processes start crashing within minutes.
`reclaim` exists for exactly that moment: it frees the most space in the
fewest seconds, visibly, without asking anything and without scanning first.

```sh
reclaim                  # everything reversible, biggest-first (tiers 1–3)
reclaim --until 100G     # stop the ladder as soon as 100G is free
reclaim --tier 1         # only the huge build/package caches
reclaim --dry-run        # size every step, delete nothing
reclaim --list           # print the ladder
reclaim --only go-build,docker-builder
reclaim --skip trash --keep-days 30
reclaim --json           # stable report for agents
```

## How it works

A **fixed ladder** of steps, each deleting something that regenerates on its
own. No `du`, no classification pass — the first delete starts immediately.

- Steps in a tier run **concurrently**; each prints the moment it finishes
  with the volume's free space at that instant, so partial wins land while the
  slow ones (Docker prune) are still working.
- **`--until`** checks free space after every finished step and cancels the
  rest of the ladder once the target is met.
- "Freed" in the summary is the **df delta**, never a sum of `du` figures —
  APFS clones and hardlinks make per-tree sizes lie. Per-step bytes are the
  logical size seen during deletion (rm walks the tree anyway) or what the
  tool itself reports (Docker's "Total reclaimed space").
- Aged steps (`messages-tmp`, `sandbox-tmp`) delete only files older than
  `--keep-days` (60) and never the tree — a warm media cache is not garbage,
  purging it just costs an iCloud re-fetch and a slow app.
- A missing tool (`docker`, `pnpm`, `xcrun`, `brew`, `pwmcp`) skips its step;
  a failing one reports and the ladder continues.

## The ladder

| Tier | Step | What goes | Comes back via |
|---|---|---|---|
| 1 | `go-build` | `~/Library/Caches/go-build`, `~/.cache/go-build` | next `go build` |
| 1 | `docker-builder` | `docker builder prune -af` | next build |
| 1 | `docker-images` | `docker image prune -af` (unreferenced only) | re-pull / rebuild |
| 1 | `bun` | `~/.bun/install/cache` | next `bun install` |
| 1 | `npm` | `~/.npm/_cacache`, `~/.npm/_npx` | next install / npx |
| 1 | `derived-data` | `~/Library/Developer/Xcode/DerivedData/*` | next Xcode build |
| 1 | `gradle` | `~/.gradle/caches` | next gradle build |
| 1 | `pnpm` | `~/Library/Caches/pnpm` + `pnpm store prune` | next `pnpm install` |
| 1 | `cargo` | registry cache, git checkouts | next `cargo build` |
| 1 | `py` | pip / uv caches | next install |
| 2 | `tool-caches` | playwright-mcp, CocoaPods, dotslash, claude, codex, copilot, composer, yarn, turbo, nx, JetBrains | next use |
| 2 | `browser-caches` | Brave, Chrome, Spotify caches | next launch |
| 2 | `playwright` | `pwmcp prune` — unpinned browser revisions only | nothing |
| 2 | `brew` | `brew cleanup -s` + `~/Library/Caches/Homebrew` | next `brew install` |
| 2 | `maven` | `~/.m2/repository` | next `mvn` build |
| 2 | `logs` | JetBrains, CreativeCloud, rotated `*.log.old.*` | nothing |
| 2 | `xcode-caches` | Xcode + CoreSimulator caches | next run |
| 2 | `sim-unavailable` | `simctl delete unavailable` | nothing |
| 3 | `device-support` | iOS / watchOS / tvOS DeviceSupport symbols | re-sync from a plugged device |
| 3 | `sim-runtimes` | simulator runtimes no device uses | re-download via Xcode |
| 3 | `sim-logs` | per-sim diagnostics logs (shuts sims down; apps + data survive) | nothing |
| 3 | `messages-tmp` | Messages sandbox tmp, files older than keep-days (quits Messages) | iCloud re-fetch |
| 3 | `sandbox-tmp` | every app sandbox tmp, aged slice only | app re-creates |
| 3 | `trash` | `~/.Trash` | nothing |

Measured on a working Mac when this was written: go-build 77G, Docker build
cache 24G, bun 17G, DeviceSupport 17G, npm 8G, gradle 5G — none of them in a
named-bucket cache list until someone ranked by size.

Adding a step is one entry in `internal/reclaim/ladder.go` (ID, tier, what
regenerates it, paths or a run func) plus a row here.

## What it refuses to touch

- **Docker volumes and containers.** A dangling volume may be a paused
  worktree's database; the owner is a compose project *name*, not a path.
  The run prints the reclaimable volume size as a by-hand note pointing at
  the audit flow that classifies ORPHAN vs MOVED and deletes by name.
- **Unsaved screen recordings** — user data; listed with sizes, opened by
  hand.
- **`~/Library/Caches/ms-playwright`** — every parallel session would
  re-download and serialize on a silent lock; only `pwmcp prune` goes near it.
- **`Messages/Attachments`** — that *is* the conversation media.
- Anything not on the ladder. Big and unclassified means user data until a
  human says otherwise.

## Exit codes and JSON

`--json` emits the full report: volume, free before/after, per-step status
(`done` / `dry-run` / `skipped` / `failed` / `stopped`), bytes, seconds, free
space after each step, notes. Exit is 0 even when a step fails — a half-freed
disk is still the goal met; failures are in the report.

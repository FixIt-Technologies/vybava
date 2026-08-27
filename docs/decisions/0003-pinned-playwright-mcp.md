# 0003 — One pinned Playwright MCP and one shared browser registry

Status: accepted

## Summary

A workstation running many parallel agent sessions was re-downloading Chromium
several times a day, and periodically wedging on an install that produced no
output and never finished. Three independent causes compounded:

1. **Cache sweeps.** Playwright's default registry is
   `~/Library/Caches/ms-playwright`, and every "free up disk" routine reaches for
   `~/Library/Caches` first. Deleting it is scored as zero-risk because the bytes
   regenerate — but the regeneration is a 150 MB–1 GB download charged to the
   next session, times however many sessions notice at once.
2. **Version fan-out.** `@playwright/mcp@latest` resolves separately per project,
   per git worktree, and per `bunx`/`npx` temp directory. Each distinct
   `playwright-core` pins its own Chromium revision, so N configurations mean N
   browser downloads that never converge. Temp-directory installs additionally
   evaporate whenever the OS prunes `/var/folders`.
3. **Silent lock contention.** Concurrent installs meet on Playwright's
   `__dirlock`. The loser blocks with no output and no timeout, so a crashed or
   slow install wedges every later session until someone finds the pid by hand.

`pwmcp` addresses all three: one pinned version, one registry outside the OS
cache directory, and an install lock that always terminates with an explanation.

## Decisions

| # | Decision | Call | Why |
|---|----------|------|-----|
| 1 | Isolation unit | Per-server **profile**, shared **binary** | `--isolated` means "keep the profile in memory", not "own browser build". Sessions interfere through profiles (cookies, storage, dirty state after a crash), never through the read-only binary. Duplicating binaries is the disease, not the cure. |
| 2 | Registry location | `~/.local/share/vybava/playwright-browsers` | Outside `~/Library/Caches`, so no cleanup routine and no OS cache purge can charge a download. `pwmcp status` warns when a second registry reappears. |
| 3 | Version policy | Single Go constant, never `@latest` | A floating tag is what turns one browser revision into N. Bumping is a deliberate, reviewable edit. |
| 4 | Install layout | One directory per pinned version | Bumping the pin is additive and rollback is free, because `prune` keeps every revision any installed pin still references. |
| 5 | Reclaiming disk | `pwmcp prune`, never `rm -rf` the registry | Prune drops only revisions no installed pin needs, and refuses entirely if it cannot read a pin — deleting on the basis of an unreadable install tree would wipe browsers in active use. |
| 6 | Locking | Own lock file with holder pid, age, and a timeout | Playwright's lock is silent and unbounded. A dead or clearly abandoned holder is reclaimed automatically; a live one is named, never stolen. |
| 7 | Flag handling on `serve` | Parsing disabled, everything forwarded | The server's flag surface is large and moves upstream. Re-declaring it here would mean silently rejecting new flags between releases. |
| 8 | Isolation override | Injected unless the args already choose a profile | Chromium loads an unpacked extension only into a persistent profile, so a config passing `--user-data-dir` or `--config` is doing the one thing isolation forbids. `--shared-profile` is the explicit escape hatch. |
| 9 | Default browser set | chromium, chromium-headless-shell, ffmpeg | The MCP server drives Chromium. Firefox and WebKit would add ~300 MB nothing on the workstation opens; `--browser` adds them on demand. |

## Assumptions

- `bun` is the runtime, per house convention; `pwmcp` fails with a clear message
  rather than silently falling back to another package manager.
- Completion is judged by Playwright's own `INSTALLATION_COMPLETE` marker, so an
  interrupted download counts as missing rather than launching a broken browser.
- `prune` only ever considers directories matching Playwright's
  `<name>-<revision>` shape, leaving bookkeeping entries (`.links`, `__dirlock`)
  untouched.
- Projects that run Playwright directly (`playwright test`) bypass `pwmcp`
  entirely; `pwmcp env` emits the `PLAYWRIGHT_BROWSERS_PATH` export that points
  those runs at the same registry.

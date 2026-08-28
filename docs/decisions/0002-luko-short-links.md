# 0002 — luko.to short links for terminal-clickable URLs

Status: accepted

## Summary

Long URLs printed by AI sessions wrap in narrow Warp panes, and Warp's per-line
URL detection truncates a wrapped link at the visual line break — clicking opens
a broken prefix. Warp has no user-configurable link regexes, and Claude Code
cannot emit OSC 8 hyperlinks from assistant prose, so the fix is at the URL
layer: a public redirector on **luko.to** plus a `shrt` applet that every agent
(and human) uses to print short, never-wrapping forms.

## Decisions

| # | Decision | Call | Why |
|---|----------|------|-----|
| 1 | Custom Warp parsing? | No — URL-layer fix | Warp link patterns are built-in only (warpdotdev/Warp#950 open); OSC 8 works in Warp but Claude Code doesn't emit it (anthropics/claude-code#13008, #54606). |
| 2 | Host | Real public domain **luko.to** (user-purchased) | Works off-mesh and on phone, normal DNS, real TLS; single-label fake hosts were unverified in Warp and dotted mesh names need per-machine DNS. |
| 3 | Reachability | Public DNS, HTTPS | Follows from 2; no /etc/hosts or mesh-DNS maintenance. |
| 4 | Link shape | Per-domain static rewrites + minted tails | `luko.to/gh/fixit/1088` is readable and derivable offline; everything else gets an idempotent minted code (`luko.to/<7-char hash>`) that is permanent so old scrollback keeps working. |
| 5 | Gating | Open redirects, authed minting | Targets are GitHub-auth-gated or mesh-only; a guessed path leaks only a repo path. Minting requires a bearer token (onyx-held). |
| 6 | Minting interface | vybava applet `shrt` + one global CLAUDE.md line | Works for Claude, Codex, any AI, and humans; no per-session MCP context cost. `shrt --osc8` covers label links from Bash output. |
| 7 | Shortening policy | Agents run EVERY printed URL through `shrt`; the CLI decides (revised 2026-08-28) | No judgment left to the agent — `shrt` returns URLs under 40 chars unchanged (below that nothing wraps even in ~55-col panes), so piping everything is always safe. |

## Assumptions

- Hosting: the redirector deploys via deployik (custom domain + auto-TLS) —
  the house PaaS doing exactly its job.
- Minted codes start at 7 base32 chars and extend per-entry when the prefix
  collides with a different URL or spells a reserved segment — a code must
  never redirect to the wrong target. The store is capped at 100k links
  (token-authed minting makes this a runaway backstop, not a security bound).
- The CLI mints only URLs ≥ 40 chars; shorter ones pass through unchanged — a
  code buys nothing over an already-short URL. This threshold is the CLI's,
  not the agent's: the global instruction is unconditional (decision 7).
- GitHub redirects `/pull/N` ↔ `/issues/N` automatically, so one `gh` form
  covers PRs and issues.
- Verification: after DNS + deploy, click-test a short link in a deliberately
  narrow Warp pane (the user's explicit requirement).

## Architecture notes

- `internal/shrt/` owns everything; CLI wiring stays thin per 0001.
- `rules.go` is the single source of truth for static rewrites, used by both
  the CLI (offline shorten) and the server (expand). Server and CLI ship from
  the same commit; a server that doesn't know an alias 404s loudly.
- Minted store: append-only JSONL + in-memory map; code = first 7 chars of
  base32(sha256(url)) — deterministic, so minting is idempotent with no
  read-before-write coordination.
- CLI token lookup: `$LUKO_TOKEN`, then macOS Keychain (service `luko.to`).
  Server token: `$LUKO_MINT_TOKEN` env (set via deployik variables).

## Open questions

- ~~Where luko.to's DNS is hosted~~ — spaceship.io; apex A record set to the
  deployik endpoint 2026-08-27.
- ~~The global CLAUDE.md line lands only after the live click test~~ — landed
  2026-08-28 after verification: `Print every URL through `` `shrt <url>` `` —
  bare, alone on its own line.`

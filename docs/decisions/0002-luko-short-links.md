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
| 4 | Link shape | Static rewrites + DYNAMIC prefix rules + minted tails (revised 2026-08-28) | `luko.to/gh/fixit/1088` stays compiled-in; `shrt rule add` creates server-owned prefix rules (`luko.to/sentry/123`) live without a deploy, cached per-origin for offline use; everything else gets an idempotent permanent code. |
| 5 | Gating | Open redirects, authed minting | Targets are GitHub-auth-gated or mesh-only; a guessed path leaks only a repo path. Minting requires a bearer token (onyx-held). |
| 6 | Minting interface | vybava applet `shrt` + one global CLAUDE.md line | Works for Claude, Codex, any AI, and humans; no per-session MCP context cost. `shrt --osc8` covers label links from Bash output. |
| 7 | Shortening policy | Agents run EVERY printed URL through `shrt`; the CLI decides (revised 2026-08-28) | No judgment left to the agent — `shrt` returns URLs under 40 chars unchanged (below that nothing wraps even in ~55-col panes), so piping everything is always safe. |

## Team access (added 2026-08-28)

Five-person team. The env token (`LUKO_MINT_TOKEN`) is the ADMIN identity:
it alone issues/revokes named member tokens (`shrt token issue|revoke|list`).
Member tokens mint and manage rules; the server stores only SHA-256 hashes
(`tokens.json` beside the stores) and logs every mint/rule change with the
caller's name. Revocation is immediate and per-person. Dynamic rules and the
mint namespace are shared team-wide by design — one vocabulary.

Self-enrollment (added 2026-08-28): `shrt enroll <name>` issues your own
token with NO admin in the loop — authorization is network position. The
endpoint honors only requests whose real client IP falls inside
`LUKO_ENROLL_CIDRS` (the WireGuard ranges); unset = disabled, fail closed.
The client IP is the TCP peer — unless the peer is inside
`LUKO_TRUSTED_PROXY_CIDRS` (the deployik edge / docker pool ranges), in
which case its `X-Real-IP` is required and authoritative (missing or
malformed → 403; a proxy's own address is never a client). X-Forwarded-For
is never consulted. Both vars unset = enrollment fully disabled. The CLI dials the mesh gateway IP
directly (TLS still verified against luko.to) so the request provably
arrives from the WG address, and the token lands straight in the Keychain,
never displayed. `admin` stays unregistrable; revocation stays admin-only.

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
- `rules.go` is the source of truth for COMPILED static rewrites (CLI and
  server ship from the same commit); dynamic prefix rules live server-side in
  `rules.json` with the server authoritative and the CLI holding a per-origin
  offline cache that self-heals through the mint path.
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

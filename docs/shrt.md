# shrt — luko.to short links

Long URLs wrap in narrow terminal panes and truncate on click. `shrt` prints
short forms on the public redirector **https://luko.to**; following a link
needs nothing, minting and managing need a member token. Full rationale:
[decisions/0002-luko-short-links.md](decisions/0002-luko-short-links.md).

## Shorten

```sh
shrt <url>...          # prints the short form per URL (or unchanged if already short)
shrt --osc8 --label "board" <url>   # OSC 8 hyperlink for Bash-emitted output
shrt --json <url>      # {long, short, static, minted}
```

Resolution order: compiled repo rules (`gh/<alias>/<n>`, `b/<id>` — offline) →
team dynamic rules (offline via per-origin cache) → minted 7-char code
(idempotent, permanent; collisions extend the hash). URLs under 40 chars pass
through unchanged.

## Team onboarding (self-service)

```sh
brew install --cask FixIt-Technologies/tap/vybava
vybava install shrt
shrt enroll <yourname>   # WireGuard on; token lands in the macOS Keychain
```

Enrollment is authorized by network position: the endpoint only honors
requests from `LUKO_ENROLL_CIDRS` (WireGuard ranges), the CLI dials the mesh
gateway (`--via`, default 10.8.4.1) so the request provably arrives from the
WG address, and the token is never displayed. `admin` is unregistrable.

Agents adopt it with one global instruction line:
``Print every URL through `shrt <url>` — bare, alone on its own line.``

## Dynamic rules (shared team vocabulary, full CRUD)

```sh
shrt rule add sentry https://sentry.dev.lovinka.com/organizations/lovinka/issues/
shrt rule list                    # any member token
shrt rule update <name> <prefix>  # prefixes must end with "/"
shrt rule rm <name>
```

Rules are server-owned and live everywhere immediately;
`~/.config/shrt/rules-<host>.json` caches them per origin for offline use.
Both a prefix's bare root and its deeper paths use the rule, so a
`https://github.com/` prefix also covers `https://github.com`.

## Admin

```sh
shrt token issue <name>   # prints the value ONCE (admin env token only)
shrt token revoke <name>  # immediate, per-person
shrt token list           # names + issue dates, never values
shrt token set            # store YOUR token in the Keychain (stdin)
```

The `LUKO_MINT_TOKEN` env value is the admin identity; member tokens are
stored as SHA-256 hashes and every mint/rule/token mutation is logged with the
caller's name.

## Server & deployment

`shrt serve` runs the redirector (flags: `--addr`, `--store`, `--rules`,
`--base`). Stores are flat files under the data dir: `links.jsonl` (append-only
minted codes), `rules.json`, `tokens.json` (hashes, 0600).

Production: deployik app **luko** builds the repo-root `Dockerfile` (deployik
uses the Dockerfile's directory as build context — it must stay at the root),
domain `luko.to`, port 8080, `/healthz`, `/data` volume. Env:
`LUKO_MINT_TOKEN` (admin), `LUKO_ENROLL_CIDRS` (unset = enrollment disabled),
`LUKO_TRUSTED_PROXY_CIDRS` (peers whose `X-Real-IP` is honored; unset = header
ignored). The CLI's compiled rules and the server ship from the same commit —
after changing `internal/shrt/rules.go`, redeploy.

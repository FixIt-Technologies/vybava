# claude-guards

claude-guards is the enforcement layer under `~/.claude/CLAUDE.md`. CLAUDE.md is
context — Claude reads it and usually complies. The bans in this applet are
incident-born and must hold unconditionally, including under bypass
permissions and inside subagents, where skills do not even load. It runs as a
Claude Code PreToolUse hook: one compiled process per Bash or Read call,
single-digit milliseconds, no fork storms.

Wire it once in `~/.claude/settings.json` (the applet symlink lives at
`~/.local/bin/claude-guards`, so an older `~/.claude/hooks/claude-guards`
symlink keeps working):

```text
PreToolUse    Bash                        claude-guards bash
PreToolUse    Read                        claude-guards read
PreToolUse    playwright|chrome-devtools  claude-guards browser
SessionStart                              claude-guards swarm-teardown --dead-only
SessionEnd                                claude-guards swarm-teardown
SessionEnd                                claude-guards browser-teardown
```

A block prints its reason and the sanctioned alternative on stderr and exits 2;
that text is what Claude sees, so every message ends in the next command, not
in a bare "denied". Malformed payloads fail open — a guard that blocks
everything on a parse error would brick the session.

Rule families, each with its own escape hatch named in the block message:

```text
destructive:*     git stash · checkout/switch/restore . in the primary clone ·
                  compose down -v · db volume rm/prune · keychain value reads
secrets:*         env / printenv dumps, /proc/*/environ, docker inspect .Config.Env
simulator:*       cliclick / AppleScript System Events against the Simulator
e2e:*             raw simctl screenshots and raw .e2e PNG reads
commit-secrets    key files, secret-shaped lines, private infra strings in a public repo
context:*         inline python/node scripts that write files · cat/tee over an
                  existing file · cat/sed/head/tail or Read above 200 lines ·
                  dumping a ~/.claude/projects transcript · any raw read of a
                  locale catalog declared in vybava.config.ts (lok.catalogs) —
                  ranges included; the message points at lok get/grep/add
```

The `context:*` family exists because the bypass-permissions harness text
tells Claude to prefer Bash over Read, Edit and Write. Measured on one epic
session (2026-09-10, 750k tokens): 271k of the agent's own shell-written
files, ~190k of whole-file dumps, 18k of reading its own transcript, and zero
Edit calls. The rules make Edit and ranged reads the only cheap path. Piped or
redirected reads never reach the context and are never blocked; `/tmp`,
`/var/folders` and `$TMPDIR` targets are exempt from the write rules.

`swarm-teardown` and `browser-teardown` are the lifecycle verbs. They block
nothing and decide nothing: they clean up what the ending session owns, log a
line to stderr, and always exit 0.

`browser-teardown` sends exactly one `browser_stop` to the resident onyx-mcp
daemon, for the ending session's own id (the SessionEnd payload's `session_id`,
else `CLAUDE_CODE_SESSION_ID`). Since Onyx went HTTP-only one daemon owns every
session's Helium, and it reaps a browser when the browser's owning process dies
— which is now the daemon itself, which never dies. The idle watchdog is no
backstop either: agents are routinely told to pass `idle_timeout_seconds: 0`,
which disables it. So browsers accumulate at ~0.5 GB apiece — 23 of them on one
Mac on 2026-09-12, the oldest 21 hours old. The daemon cannot fix this from
inside, because a session that will never call again looks exactly like a
session thinking; session end is knowledge only the ending session has, and
this hook is how it hands it over.

Two properties, both incident-born. It stops its OWN browser and nothing else —
no sweep, no pkill, never a peer's id, because an earlier cleanup that reached
wider killed every peer's browser and wiped their logged-in profiles. And it
fails open on everything: no token file, no daemon, refused connection, non-200,
JSON-RPC error. A hook that errors or hangs here would degrade every session end
on the machine, so the only failure it even prints is one the daemon itself
reported.

Try a rule without a hook payload:

```text
claude-guards check bash "git stash" --json
claude-guards check bash "cat apps/client/locales/cs.json" --cwd ~/Work/Projects/FixIt --json
claude-guards check read apps/client/locales/cs.json --json
claude-guards browser-teardown --session "$CLAUDE_CODE_SESSION_ID"
```

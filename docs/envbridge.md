# envbridge

Bridge vault-injected startup values into another local process without an
environment file. The server reads a JSON object from stdin and retains only the
explicit `--key` allowlist. It listens on a mode0600 Unix socket in an existing
directory owned by the current user with no group/other permissions.

The injector owns the process and closes it after startup. Lifetime defaults to
two minutes and cannot exceed ten minutes. Existing paths are never replaced.
Status and errors omit values. An absent or expired server is an error, never a
fallback to saved credentials. Processes running as the same OS user are trusted;
this is not a substitute for separate user identities.

The bridge requires Unix ownership and permission checks (macOS/Linux). Windows
builds retain the other applets; envbridge refuses directories on Windows rather
than treating Unix permission bits as an ACL check.

```text
envbridge serve --socket /private/workspace/env.sock --key TOKEN --ttl 2m --json
envbridge read --socket /private/workspace/env.sock --key TOKEN --format shell
```

The first command must receive its JSON through the vault's consuming wrapper,
not a file or command-line value. The second command's output is sensitive: capture
it only inside the consuming process, never in a transcript or log. JSON output is
available with `--format json` or `--json`. Shell output contains quoted `export`
statements; the utility itself never evaluates them. Do not enable shell tracing
in a consumer. A PowerFLOW startup caller selects its own keys and socket path;
the utility has no project or vault-specific behavior.

The server removes its own socket on normal cancellation/expiry. If killed without
cleanup, the stale socket is deliberately retained rather than overwritten by a
new invocation; establish that its owner is gone before removing it.

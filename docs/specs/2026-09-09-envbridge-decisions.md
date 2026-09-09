# Fileless startup environment — Decisions

## Summary

PowerFLOW acceptance needs Onyx-injected credentials in Devbox-managed Compose
startup without plaintext environment files. A bounded Unix-socket bridge lets the
normal startup path consume values from a private, memory-only process.

## Decisions

| Decision | Call | Basis |
|---|---|---|
| Storage | Process memory and a private Unix socket; no secret-bearing files | Existing user credential constraint |
| Scope | Explicit variable allowlist, private parent directory, bounded lifetime | Limit access and avoid ambient environment export |
| Ownership | Reusable envbridge applet in Výbava; PowerFLOW owns its caller | Existing utility-routing rule |
| Lifecycle | Refuse existing sockets; close on deadline/cancellation; fail closed if absent | Preserve other processes and prevent silent fallback |

## Assumptions

The socket is local to one operating-system user, not a boundary between processes
running as that same user. The injecting Onyx wrapper owns the server's lifetime.
CLI status never includes values. Reading values requires an explicit machine
output format; callers must keep that output inside the consuming process.

## Architecture notes

`envbridge serve` reads a JSON object from stdin, retains only the named keys, and
serves them on a mode0600 socket inside a private directory for at most ten minutes.
`envbridge read` requests named keys and emits JSON or correctly quoted shell exports.
There is no network listener, persistence, vault retrieval, or shell execution.
The feature is enabling work for Vitrinka forge epic1 and the Forge release map.

## Open questions

None for this bounded utility. Managed PowerFLOW startup and restored-data acceptance
remain separate verification gates.

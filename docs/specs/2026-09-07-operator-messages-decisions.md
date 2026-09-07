# Messages observation — decisions

## Summary
The operator needs a background Messages reader that notices new messages and
invalidates context after edits, retractions or local removal. Observations feed
the existing history and proposal workflow; they never send or modify Messages.

## Decisions
| Decision | Call | Why |
|---|---|---|
| Content decoding | Reuse pinned `imsg` 0.15.2 through read-only RPC methods | Verified attributed-body decoding; avoid a second parser. |
| Change detection | Read-only SQLite metadata/content fingerprints, including edit/retraction fields | The upstream row-ID watcher misses mutations to existing messages. |
| Initial scope | Baseline existing row IDs, then record new messages and changes to recorded messages | Avoid importing historical unread mail as fresh attention. Existing archive remains in Messages. |
| Freshness | Publish per-source success/error and baseline scope independently of UI reads | A successful Claude scan does not prove Messages was checked. |
| Attention | Record observations first; proposals require current-state verification | Receipt and incoming text never authorize actions. |
| Execution | Explicit reader path, non-TTY stdin, bounded calls; no bridge/injection or source writes | Preserve the human desktop and avoid Contacts prompts. |

## Assumptions
Lukáš delegated routine implementation choices and authorized local read-only app
observation. External sends remain human-only. Store changes need an explicit
version upgrade so older writers cannot discard new cursor/coverage data.

## Architecture notes
The operator package owns source scanning, fingerprints, atomic event/cursor
publication and coverage. CLI wiring only schedules reads. A failed or incomplete
read cannot advance a cursor or certify current coverage. A missing local row is
described as local removal, not proof that the sender retracted it.

## Open questions
Outlook account coverage and desktop-independent agent messaging remain separate
tasks. Live edited/retracted-message coverage must be verified before claiming a
complete Messages integration.

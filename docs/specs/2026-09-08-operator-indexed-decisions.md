# Indexed local operator — Decisions

## Summary

The operator trial grew to 28,097 observations in a 20 MB JSON file. Repeated archive decoding, rewriting and session discovery, together with eager native history rendering, consumed substantial CPU. The user approved an indexed replacement; existing watchers remain disabled until replacement performance is verified.

## Decisions

| Decision | Call | Why |
|---|---|---|
| Durable history | Local SQLite with indexed event identity, source identity and stable history order | Query or update the relevant records without rewriting the archive |
| Migration | Explicit transactional import, retained private JSON backup, old-writer rejection | Preserve observations, proposal revisions, human feedback and receipts |
| Observation | Incremental source cursors and changed-file detection, reconciliation for missed changes | Bound steady-state work while retaining restart correctness |
| Native UI | Bounded pages, lazy rows and content-revision polling | Avoid rebuilding every loaded observation on every heartbeat |
| Completion evidence | Keep observation, proposal, human approval and actual outcomes distinct | Approval or a source claim is not proof that an action was executed or verified |

## Assumptions

- Sending and privileged source-app actions remain human-controlled.
- Existing history and feedback must survive migration without invented outcomes or scores.
- Scanner timestamps are liveness metadata, separate from content revision.
- No restart of disabled watchers or suspended native instances during implementation.

## Architecture notes

Výbava owns transactional state and indexed queries. EVE consumes bounded CLI pages and a cheap revision/liveness query. Source cursors and coverage metadata are separate from event records. Existing event IDs remain stable pagination cursors.

## Open questions

None for this slice. End-to-end autonomous execution is not introduced by the storage migration.

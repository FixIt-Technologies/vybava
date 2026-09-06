# Operator companion — decisions

## Summary
Lukáš approved all three next steps: a persona interaction prototype, real local
operator observations and feedback, and verification of background operation.
Codex remains the operator; EVE is the native surface. Sending remains human-only.

## Decisions
| Decision | Call | Why |
|---|---|---|
| Native surface | Extend the existing EVE SwiftUI app and partner-home branch | Preserve the existing investment and a single installed app. |
| Presence | Compact nonactivating panel, explicit expansion to a briefing | Stay visible without claiming the user's keyboard. |
| Connection | Versioned local CLI snapshot | Reuse the operator's locked source of truth; no copied proposal ledger in the UI. |
| Feedback | Optional numeric score or the human's own words | Qualitative approval must not fabricate a score. |
| Background evidence | Last successful source scan distinct from UI refresh | A live UI must not conceal a stopped observer. |
| State upgrade | v1 to v2; older binaries fail closed | Preserve new feedback fields against old writers. |

## Assumptions
The accepted direction supplies the first slice's visual and interruption choices:
quiet presence, expanded briefing, prepared responses and human judgment. The
existing example day remains available separately. No sending or observed-session
merge authority is added. Independent idle wake requires a timed runtime probe.

## Open questions
Automatic sending eligibility and broader life/calendar ingestion remain outside
this slice and require the user's future decisions.

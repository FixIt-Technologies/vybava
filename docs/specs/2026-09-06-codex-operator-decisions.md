# Codex operator trial — decisions

## Summary

Codex is Lukáš's primary operator on his Mac. Test reacting to new Claude Code activity and WhatsApp conversations, preparing responses and recording human scores. Eve remains an optional execution destination.

## Decisions

| Decision | Call | Why |
|---|---|---|
| Operator | Resume/queue into the explicitly selected Codex thread | Preserve the user's conversation; do not create another independent leader. |
| Claude input | Incrementally read top-level Claude session JSONL, starting at the current end on first attachment | Observe new work without replaying historical conversations or modifying Claude's hooks. |
| Codex input | Opt-in dated session logs, explicit operator-session exclusion, assistant messages with source cwd | Detect cross-repository work without observing the operator's own output or duplicating event mirrors. |
| WhatsApp input | Observe and prefill through computer use in the existing app | User explicitly chose app interaction rather than a connector. Actual control must be verified. |
| Delivery | Durable event IDs, queue acceptance and operator acknowledgement are separate | CLI acceptance alone does not establish that the operator reacted. |
| Replies | Store proposed text against the exact observed context; changes retire pending proposals | Never overwrite an old proposal with a new context or imply a stale reply remains ready. |
| Evaluation | User scores 1–5, optionally supplying a correction | Collect actual feedback; unscored drafts are not successes. |
| Sending | No send command and no automatic promotion | Sending remains the user's action. Good scores are evidence for a future decision, not authorization. |
| Packaging | Go applet in Výbava; private local state outside git | The event and scoring mechanism is reusable across projects. |

## Assumptions

- The active user goal authorizes implementing and testing this trial. A first trial can be operated through Codex plus the CLI; another UI is unnecessary to prove the loop.
- Claude messages, app content and tool output are observations, not new user instructions. Responses remain subject to the active user's authority.
- On September 6, the probe and real Claude event references arrived in the target conversation and were acknowledged after inspection. Next-turn delivery is verified; an event first generated while already idle remains untested.
- The session's native computer-use tool fails with `Sky Computer Use native pipe startup failed`. No WhatsApp read or prefill is claimed until actual UI observation succeeds.

## Open questions

Which reactions become eligible for a future auto mode, the required score history, and any sending authorization remain explicitly undecided.

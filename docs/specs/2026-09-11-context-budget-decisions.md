# Context budget second pass — decisions

The continuation follows the design and prior rulings in context-budget-pass-2,
tracked by https://app.vitrinka.ai/w/fixit/p/claudik/t/857.

| Decision | Call | Reason |
|---|---|---|
| Context accounting | Latest input + creation + cached-read usage | Measures occupied context rather than cumulative spend |
| Reminder | 50%, once per transcript, PreToolUse additionalContext | Exit-zero stderr is not visible to the model |
| Large reads | Above 100 text lines at 70%; explicit escape | Enforcement backs the advisory reminder |
| Screenshots | Remain allowed by budget rules | The handoff's recorded user ruling overrides its contradictory image-ban phase |
| Configuration | Shared TypeScript helpers, per-call guards section | One canonical payload; no process-global policy cache |
| Discovery | Advisory, tracked files, absent sections only | Preserve human catalog choices and source configuration |
| Package boundary | internal/configdiscover | Reuses Lok without a vconfig → lok → vconfig cycle |
| Diagnosis | Recorded usage plus explicitly estimated text/images | Separate measurement from approximation |

The reference transcript continued after the handoff: its current maximum is
676,918, below a 70% threshold in a 1M window. Historical 638k is therefore not
a valid denial assertion. Synthetic fixtures verify the exact 50%/70% boundaries;
the real replay verifies accounting and image estimates.

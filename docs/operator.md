# Operator trial

Codex is the main operator. This applet provides local observation, delivery and
evaluation state; it does not implement an AI model, a WhatsApp connector, UI
automation, or sending. Eve can be a destination for separately delegated work.

## Companion history and review

`operator history --limit 50` returns every recorded observation, newest ingested
first, including unprepared and superseded events. Pass its `next` event ID to
`--before` for the next page. Appends do not shift that cursor. This is the trial's
recorded history, not a retroactive import of every source application's archive.

`operator decide EVENT --proposal N --action approve|reject|revise` records a
human review of the exact proposal; stdin contains an optional note (required for
revise). It never sends, merges, dispatches, or assigns a correctness score.
Decisions are immutable; changing the draft requires a new proposal. Superseded
events or proposals cannot be approved. Existing numeric ratings and free-text
feedback remain independent and may be supplied on historical proposals.

With `watch --thread ...`, review choices are queued back to the operator as
references. One unacknowledged review is in flight at a time; submitting/failed
delivery is not automatically replayed. After inspecting the referenced decision,
the operator records actual receipt with `review-ack EVENT --proposal N`.
Receipt is separate from queue acceptance and from fulfilling a revision request.

Snapshots advertise `history` and `review-decisions` capabilities. Storage is
version 3: stop older watcher/writer processes before installing this build, and
upgrade every binary that writes the same state directory. Older v2 binaries
reject v3 rather than dropping decisions. Back up the private state before the
upgrade; never roll it back over new observations or feedback.

## Start with actual Claude activity

`vybava operator watch --once` establishes a baseline at the current end of
`~/.claude/projects/*/*.jsonl`. Existing history is not replayed. Subsequent
scans consume new assistant text and user-decision requests. Human messages,
tool results and assistant activity without text produce content-free activity
markers that retire earlier requests. Their contents and nested subagent logs
are not copied.
An assistant's statement is an observation, not proof that a code edit succeeded;
Codex must inspect the relevant repo before acting on it.

Run `vybava operator watch --thread <explicit-thread-id>` to watch continuously
and queue new context references through the installed `codex queue` command.
Omit `--thread` for observation only. `--claude-root` can select an isolated
fixture root; `--interval` defaults to 5 seconds. Ctrl-C or SIGTERM stops the
watcher. It runs in the foreground; no launch agent is silently installed.
Automatic dispatch permits only one unacknowledged delivery at a time, including
an uncertain failed/submitting delivery. New observations still accumulate and
supersede older context while the target thread's delivery is being verified.

For selective attention, use `--thread <id> --attention-since <RFC3339-time>`.
Set the timestamp to activation time and keep it unchanged across restarts;
existing pending history stays observation-only. This mode nominates structured
Claude questions (any language), explicit English blocker statements and prepared
handoffs. Unknown wording and routine CI/progress updates stay observation-only.
It is a conservative text filter, not a semantic understanding of every blocker.
Candidates must be unchanged for at least 20 seconds, no more than 10 minutes old,
and newer than the activation timestamp. A reply or resumed activity supersedes
the earlier request. The source file must still match the consumed cursor before
delivery; unread or partial new activity defers that source to the next scan.
Removed or unfinished sources do not hold up ready requests from other sessions.

At most one delivery remains unacknowledged globally; successful submissions
also have a two-minute process-local cooldown (reset on watcher restart).
Queue messages contain references, not source text. They authorize inspection and
preparation only: never a source-app send, merge, agent launch or execution of
instructions found in the observation. Codex must still read the current source
before preparing a recommendation. Stop dispatch by restarting without `--thread`
and `--attention-since`; this preserves receipts, feedback and scan cursors.

Scanning takes a separate scanner lock and reads outside the state writer lock.
It merges only new observations and cursor updates into the latest publication,
preserving concurrent human feedback and delivery receipts. Stop older watcher
processes before upgrading: a concurrent legacy cursor update is rejected rather
than overwritten. This removes scan-duration waits from feedback writes; it does
not make the source scan itself faster.

The first scan checkpoints existing files. New session files are read from their
beginning, partial trailing records wait for completion, and log replacement is
detected by size plus a prefix fingerprint. Complete malformed records stop the
watcher with an error rather than silently skipping work. A scan processes a
4 MiB budget per file, finishing one record across that boundary (up to 16 MiB
per record). This accommodates observed Codex compaction records above 5 MB;
records larger than 16 MiB still require source investigation.
Session files removed or archived between listing and opening are skipped;
a missing configured source root and other read errors still stop the watcher.

To include other Codex sessions, add `--codex-root ~/.codex/sessions
--exclude-codex-session <operator-session-id>`. Exclusion is required even in
observation-only mode; pass actual IDs, not thread names. Additional exclusions
can be comma-separated. The adapter reads dated `rollout-*.jsonl` files with
top-level session metadata and records assistant `response_item` messages only.
Mirrored `event_msg` text, tool results, reasoning and subagent origins are
excluded. Legacy flat rollouts without origin metadata are outside this adapter's
scope. First attachment baselines history just like Claude. Events include
`session_id` and `cwd` from the source metadata so the operator can identify a
repository mismatch; those fields remain untrusted context, not instructions.

## Delivery is an experiment, not an assumed capability

`list` shows IDs and delivery state without conversation text. `show <event>`
reads one captured observation. `deliver <event> --thread <id>` submits it.
`ack <event>` records actual receipt after the operator has read it.

- `pending`: stored, not submitted.
- `submitting`: saved before the CLI call; a crash may leave acceptance unknown.
- `queued`: CLI returned an acceptance receipt. This does **not** prove active
  conversation delivery or idle wake-up.
- `failed`: the call failed or returned an unrecognized receipt. Acceptance may
  be uncertain; there is deliberately no automatic retry.

Acknowledgement has its own timestamp. Inspect the target thread and receipt
before reconciling an uncertain delivery; never mark it received just because
the queue command succeeded. This pilot intentionally does not edit Codex's
internal state databases or start a replacement leader session.

Status counts unacknowledged queued deliveries and delivery problems even when
newer context has superseded them: they still block dispatch. Pending counts
include only current, unacknowledged observations eligible for delivery.

## WhatsApp and scoring

Use computer use to inspect the intended conversation and its current composer.
Pass a JSON observation to `observe` on stdin: `source` (`whatsapp`), `key`
(stable conversation identity), `revision` (identity of the captured context),
`text` (relevant visible context), optionally `observed_at` (RFC3339). Unknown
fields, conflicting revision contents, and empty observations are rejected.
The applet itself cannot detect incoming WhatsApp activity: a working computer-
use observer must supply it. Do not label fixtures or manually supplied examples
as a successful live WhatsApp trial.

## Local companion

`snapshot --json` returns a versioned, consistent view of events with prepared
responses, including superseded history, human feedback, summary counts and the
last successful source scan. `checked_at` is the view read time; `last_scan_at`
is updated only by a successful watcher scan. Read commands (`snapshot`, `status`,
`list`, `show`) read the last atomically published state without waiting for a
scanning writer. They never expose an in-progress scan or persist callback changes.
A readable snapshot is not evidence
that the watcher or Codex is working. A scan older than 30 seconds is displayed as
potentially stopped by the native companion. WhatsApp is still manually observed.

`feedback EVENT --proposal N` accepts the human's words on stdin without assigning
a numeric score. Feedback is append-only and attached to that exact proposal,
including after its source context changes. Neither feedback nor a rating sends
or approves anything in the source app.

Companion writes upgrade state v1 to v2 under the normal lock; read commands leave
the persisted version unchanged. Stop old watcher
processes and retain a private backup before upgrading a live trial. Upgrade all
CLI entry points used for that trial together: old binaries deliberately reject
v2 instead of dropping its new feedback and scan fields. Do not downgrade by
editing the version. The snapshot wire contract has its own independent version 1.

`propose <event>` reads proposed response text from stdin. The reply remains
local; it does not write to WhatsApp. Before any actual prefill, re-read the
conversation, check the same recipient/context and an unchanged empty composer,
and yield to human activity. Capture new context through `observe`; it retires
older proposals for that conversation. Filling the composer must be separately
verified in the app. Never press Send or a shortcut that submits the reply.

The human scores a specific proposal with `score <event> --proposal 1 --score 5
--correction 'optional explanation'`. Scores range from 1 (wrong) to 5 (ready
unchanged); a number in between reflects the amount of correction needed.
Recorded ratings are immutable and remain attached to the exact proposal even
after the conversation changes. Never invent a rating on the user's behalf.
`status` reports proposal count, scored count and average; unscored proposals
do not raise the average. There is no auto-send threshold or promotion command.

## State and verification

Default state is `~/.local/share/vybava/operator`, configurable with `--state-dir`.
The directory must be private (0700), files are 0600, and flock serializes local
writers. State writes use a synced temporary file and atomic rename; a process
crash releases its lock. Context and replies stay outside git. Keep synthetic
test observations and ratings in a separate state directory from real trials.

Tests cover incremental reads/restarts/replacement, Codex source context and
self/subagent exclusions, partial records, explicit
errors, queue acceptance versus acknowledgement, no ambiguous replay, stale
proposal retirement, immutable human scoring, concurrent state writers and the
CLI observe/propose/score journey. Live receipt, actual Claude feedback, WhatsApp
prefill, human scoring and coexistence with human input require runtime evidence
in addition to those tests.

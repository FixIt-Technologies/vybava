# The envelope contract

One versioned JSON document per invocation, on stdout under `--json`,
success and failure alike:

```json
{
  "v": 3,
  "ok": false,
  "verb": "setup",
  "data": {},
  "diagnostics": [
    {"code": "CONFIG_MISSING_OR_INVALID", "severity": "error",
     "detail": "no devbox.yaml at the primary checkout",
     "fix": "devbox config fix --apply --json"}
  ],
  "next": ["devbox config fix --apply --json"]
}
```

## Fields

- `v` — protocol version, bumped only on breaking shape changes. The
  emitter stamps it; verbs never set it.
- `ok` — verb outcome. `false` with empty diagnostics is forbidden.
- `verb` — the invoked verb; the emitter fills it from invocation state.
- `data` — the verb's structured payload; omitted when nil (streaming verbs).
- `diagnostics` — `[]` when none, never null. Each: closed `code`,
  `severity` (`error|warning|info`), plain-language `detail`, exact `fix`
  command when one exists.
- `next` — exact commands the operator runs next, on success AND failure.
  `[]` only when the flow genuinely ends.

## Rules

- The envelope prints exactly once per invocation: one emitter function
  plus one post-execute Finish path that emits the envelope a failing or
  streaming verb still owes.
- Diagnostic codes are a CLOSED enum in one file; adding a code means a
  doc comment stating when it fires and what the fix is.
- Misuse (unknown verb, missing flag, wrong cwd/context) is a diagnostic
  with the corrected invocation in `fix` — never a usage dump.
- Exit codes are real: 0 ok, 1 unstructured infra failure, 2 diagnostics
  present, propagated codes for wrapped commands.
- Without `--json`, the same diagnostic prints as `CODE: detail — fix`
  text; the code vocabulary is shared across modes.
- One test walks every verb and asserts envelope shape on success and on a
  forced failure (devbox: `envelope_surface_test.go`).

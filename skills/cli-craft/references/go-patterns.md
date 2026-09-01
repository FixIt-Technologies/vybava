# Go patterns — lifted from devbox `cli/internal/runx/`

devbox's `cli/internal/runx/{envelope.go, diag.go}` is canonical; copy its
shapes, not its codes.

```go
// 1. The wire document + one emitter. The emitter stamps V, backfills Verb
// from invocation state, normalizes nil slices to [], and records that this
// invocation has emitted so the post-execute fallback stays silent.
type Envelope struct {
    V           int          `json:"v"`
    OK          bool         `json:"ok"`
    Verb        string       `json:"verb"`
    Data        any          `json:"data,omitempty"`
    Diagnostics []Diagnostic `json:"diagnostics"`
    Next        []string     `json:"next"`
}
func EmitEnvelope(e Envelope) error

// 2. The one structured finding shape + the CLOSED code enum (one file,
// one doc comment per code stating when it fires and the fix).
type Diagnostic struct {
    Code     string `json:"code"`     // closed enum
    Severity string `json:"severity"` // error|warning|info
    Detail   string `json:"detail"`   // plain language
    Fix      string `json:"fix,omitempty"` // exact command
}

// 3. The error-path carrier: verbs return it instead of printing. Error()
// renders "CODE: detail — fix" for text mode; ExitCode defaults to 2.
type DiagError struct {
    Diag Diagnostic
    Exit int
}

// 4. The ONE post-Execute path, called from main: prints the envelope a
// streaming or failing verb still owes under --json, the text error
// otherwise, and returns the real exit code (0 ok, 1 infra, 2 diagnostics,
// else the propagated code). Unwraps error chains to find DiagError.
func Finish(err error, stderr func(format string, a ...any)) int
```

## Rules

- Verbs NEVER print errors or call os.Exit — they return `DiagError`
  (structured) or a plain error (infra, becomes `INFRA_ERROR`); `Finish`
  owns emission and the exit code.
- Streaming verbs emit their envelope themselves and mark it emitted;
  `Finish` then stays silent.
- Keep an `INFRA_ERROR` catch-all for unstructured failures (transport,
  unexpected I/O) — detail carries the raw error, exit 1.
- Wrap with `%w` so `Finish` can unwrap to the DiagError.
- Verbs are idempotent: re-running the same command after a crash IS the
  recovery path (devbox: digest-gated reconcile); long operations
  checkpoint state and skip completed steps on re-run.
- TUI (bubbletea/lipgloss) only for verbs where a human takes the
  keyboard; the TUI calls the same core functions the `--json` path uses.

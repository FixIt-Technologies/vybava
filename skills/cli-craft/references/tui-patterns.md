# TUI patterns — Charm without dead ends

Elm architecture is the only primitive: `tea.Model` holds all state,
`Update` is the sole place state changes, `View` is a pure render.

## Structure

- Tree of models: every screen is a full tea.Model; the top level is only
  a message router + compositor. Adding a screen = one child + one router
  entry, no other file changes.
- Three routing tiers in the parent's Update: global (quit), current child
  (navigation), broadcast (tea.WindowSizeMsg — each child sizes itself).
- Stack of visited models: push on enter, pop on back/esc.
- Layout: `cmd/` (verbs), `internal/tui/` (all display code),
  `internal/<domain>/` (logic). Business logic never lives in Update.
- Sizes from tea.WindowSizeMsg + lipgloss.Height/Width — never hard-coded
  arithmetic.

## No hangs

- ALL IO through tea.Cmd — never inline in Update/View; the event loop is
  single-threaded.
- Timeouts ON the IO (http.Client{Timeout}); program deadline via
  tea.WithContext(ctx).
- Never mutate the model from an outside goroutine — p.Send(msg) or
  command results only; results arrive unordered, tea.Sequence when order
  matters.
- Spinners start via tea.Batch(tick, work) and stop only on the typed
  doneMsg/errMsg — never on a timer.

## No dead ends

- Every state quits on ctrl+c — handled in the PARENT tier so no child can
  eat it.
- Errors are typed messages stored in the model and rendered WITH recovery
  choices: retry (re-issue the Cmd) / back (pop the stack) / quit. Never
  swallowed, never fatal-only.
- huh: standalone Run() returns huh.ErrUserAborted — errors.Is, exit
  cleanly. Embedded in bubbletea, ctrl+c aborts the FORM not the app: set
  `form.CancelCmd = tea.Quit` or the user gets a blank screen. Wizards are
  huh groups (free back-navigation), never N sequential forms.
- Subprocess needing the terminal: tea.Exec, never hand-rolled
  ReleaseTerminal/RestoreTerminal.
- Recovery from quit/crash = re-running the same command: step index +
  collected state serialize to a state file; on start, skip completed
  steps and offer resume. Checkpoint AFTER a step validates.

## Testing & ecosystem

- Unit: drive Update with constructed msgs, assert on the model. E2E:
  teatest (charmbracelet/x/exp/teatest) — WithInitialTermSize, golden
  files, lipgloss.SetColorProfile(termenv.Ascii) in CI.
- Debug: tea.LogToFile gated on a DEBUG env var — stdout belongs to the TUI.
- huh for anything form-shaped; bubbles for stock widgets (spinner, list,
  table, textinput) — embed, don't reimplement; lipgloss styles and
  measures only, never holds state; fang wraps cobra for styled errors +
  silenced usage dump — presentation only, cause + next command stay the
  envelope's job.

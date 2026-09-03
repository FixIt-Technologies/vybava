// Package runx is the AI-first envelope contract shared by Výbava applets:
// one versioned JSON document per invocation under --json, a closed
// diagnostic vocabulary, and real exit codes. Shapes are lifted from devbox
// cli/internal/runx (the reference implementation named by the cli-craft
// skill); codes are per-tool.
package runx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// EnvelopeVersion is bumped only on breaking shape changes.
const EnvelopeVersion = 3

// Envelope is the wire document. Data carries the verb's structured payload;
// Diagnostics use a closed per-tool enum; Next carries exact next commands on
// success AND failure — it IS the protocol.
type Envelope struct {
	V           int          `json:"v"`
	OK          bool         `json:"ok"`
	Verb        string       `json:"verb"`
	Data        any          `json:"data,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
	Next        []string     `json:"next"`
}

// Diagnostic is the one structured finding shape: closed code, severity
// (error|warning|info), plain-language detail, exact fix command when one
// exists.
type Diagnostic struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix,omitempty"`
}

// DiagInfraError is the catch-all for unstructured failures (transport,
// unexpected I/O) — detail carries the sanitized error message, exit 1.
const DiagInfraError = "INFRA_ERROR"

// DiagError carries a Diagnostic through the error path so the one Finish
// path can emit it structurally (JSON) or textually with the same code.
// Exit defaults to 2 (diagnostics present).
type DiagError struct {
	Diag Diagnostic
	Exit int
}

func (e DiagError) Error() string {
	msg := fmt.Sprintf("%s: %s", e.Diag.Code, e.Diag.Detail)
	if e.Diag.Fix != "" {
		msg += " — " + e.Diag.Fix
	}
	return msg
}

func (e DiagError) ExitCode() int {
	if e.Exit == 0 {
		return 2
	}
	return e.Exit
}

// ExitError is the silent exit-code carrier Finish hands back to the
// multicall main: the envelope or text diagnostic was already printed, so
// Error() is empty and main must print nothing more.
type ExitError struct{ Code int }

func (e ExitError) Error() string { return "" }
func (e ExitError) ExitCode() int { return e.Code }

// ExitCoder is implemented by errors that own their process exit code.
type ExitCoder interface{ ExitCode() int }

// Session is the per-invocation emitter state: one envelope per invocation,
// text mode otherwise. Tool names the text-mode prefix.
type Session struct {
	Tool    string
	JSON    bool
	Verb    string
	Stdout  io.Writer
	Stderr  io.Writer
	printed bool
}

// Emit prints one envelope and records it so Finish stays silent.
func (s *Session) Emit(e Envelope) error {
	e.V = EnvelopeVersion
	if e.Verb == "" {
		e.Verb = s.Verb
	}
	if e.Diagnostics == nil {
		e.Diagnostics = []Diagnostic{}
	}
	if e.Next == nil {
		e.Next = []string{}
	}
	s.printed = true
	if !s.JSON {
		return s.emitText(e)
	}
	enc := json.NewEncoder(s.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(e)
}

// emitText renders the same envelope for a human: data as indented JSON,
// diagnostics as "CODE: detail — fix", next commands one per line.
func (s *Session) emitText(e Envelope) error {
	if e.Data != nil {
		enc := json.NewEncoder(s.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(e.Data); err != nil {
			return err
		}
	}
	for _, d := range e.Diagnostics {
		line := fmt.Sprintf("%s %s: %s", severityMark(d.Severity), d.Code, d.Detail)
		if d.Fix != "" {
			line += " — " + d.Fix
		}
		if _, err := fmt.Fprintln(s.Stdout, line); err != nil {
			return err
		}
	}
	if len(e.Next) > 0 {
		if _, err := fmt.Fprintln(s.Stdout, "next:"); err != nil {
			return err
		}
		for _, n := range e.Next {
			if _, err := fmt.Fprintf(s.Stdout, "  %s\n", n); err != nil {
				return err
			}
		}
	}
	return nil
}

func severityMark(severity string) string {
	switch severity {
	case "error":
		return "✗"
	case "warning":
		return "!"
	default:
		return "·"
	}
}

// Finish is the ONE post-execute path: prints the envelope a failing verb
// still owes (JSON) or the text diagnostic, and returns the real exit code
// — 0 ok, 1 infra, 2 diagnostics, else the propagated code.
func (s *Session) Finish(err error) int {
	if err == nil {
		if !s.printed {
			_ = s.Emit(Envelope{OK: true})
		}
		return 0
	}
	var diagErr DiagError
	if errors.As(err, &diagErr) {
		if !s.printed {
			next := []string{}
			if diagErr.Diag.Fix != "" {
				next = append(next, diagErr.Diag.Fix)
			}
			_ = s.Emit(Envelope{OK: false, Diagnostics: []Diagnostic{diagErr.Diag}, Next: next})
		}
		return diagErr.ExitCode()
	}
	var ec ExitCoder
	if errors.As(err, &ec) && s.printed {
		return ec.ExitCode()
	}
	if !s.printed {
		_ = s.Emit(Envelope{OK: false, Diagnostics: []Diagnostic{{Code: DiagInfraError, Severity: "error", Detail: err.Error()}}})
	}
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return 1
}

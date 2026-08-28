package perfrig

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// StageResult is what perfrig records after holding one ramp stage.
type StageResult struct {
	Stage       StagePlan `json:"stage"`
	Concurrency int       `json:"concurrency"`
	Failed      bool      `json:"failed"`            // the target failed here: a generator exited non-zero or the stage deadline hit
	Aborted     bool      `json:"aborted,omitempty"` // the guard (or operator) killed this stage — NOT a target failure
	Reason      string    `json:"reason,omitempty"`
	// Latencies are per-generator observed p95 in milliseconds, as reported by
	// the generator on stdout (a "p95_ms=<n>" line) — perfrig stays agnostic to
	// how each generator measures.
	P95Ms     map[string]float64 `json:"p95_ms,omitempty"`
	ErrorRate map[string]float64 `json:"error_rate,omitempty"`
}

// Drill is the full run record that becomes the artifact.
type Drill struct {
	Project   string        `json:"project"`
	Mode      Mode          `json:"mode"`
	Target    string        `json:"target"`
	StartedAt time.Time     `json:"started_at"`
	Stages    []StageResult `json:"stages"`
	Aborted   bool          `json:"aborted"`
	AbortNote string        `json:"abort_note,omitempty"`
}

// FirstFailure returns the first stage that failed, or nil if the drill ran
// clean to the top of the ramp.
func (d Drill) FirstFailure() *StageResult {
	for i := range d.Stages {
		if d.Stages[i].Failed {
			return &d.Stages[i]
		}
	}
	return nil
}

// Markdown renders the drill as a percentile-vs-concurrency report — the
// deliverable both projects want. Kept dependency-free so it can be dropped
// straight into an ~/Exports artifact or a vitrinka /artifact page.
func (d Drill) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Perf drill: %s\n\n", d.Project)
	fmt.Fprintf(&b, "- Mode: `%s`\n- Target: `%s`\n- Started: %s\n\n",
		d.Mode, d.Target, d.StartedAt.Format(time.RFC3339))

	// Collect the generator ids across all stages for stable columns.
	genSet := map[string]bool{}
	for _, s := range d.Stages {
		for id := range s.P95Ms {
			genSet[id] = true
		}
	}
	gens := make([]string, 0, len(genSet))
	for id := range genSet {
		gens = append(gens, id)
	}
	sort.Strings(gens)

	b.WriteString("| stage | concurrency | ")
	for _, g := range gens {
		fmt.Fprintf(&b, "%s p95 (ms) | ", g)
	}
	b.WriteString("result |\n|---|---|")
	for range gens {
		b.WriteString("---|")
	}
	b.WriteString("---|\n")

	for _, s := range d.Stages {
		fmt.Fprintf(&b, "| %s | %d | ", s.Stage.Label(), s.Concurrency)
		for _, g := range gens {
			if v, ok := s.P95Ms[g]; ok {
				fmt.Fprintf(&b, "%.0f | ", v)
			} else {
				b.WriteString("– | ")
			}
		}
		switch {
		case s.Failed:
			fmt.Fprintf(&b, "**FAIL: %s** |\n", s.Reason)
		case s.Aborted:
			b.WriteString("guard abort |\n")
		default:
			b.WriteString("ok |\n")
		}
	}
	b.WriteString("\n")

	if ff := d.FirstFailure(); ff != nil {
		fmt.Fprintf(&b, "**First failure at concurrency %d** (%s): %s\n",
			ff.Concurrency, ff.Stage.Label(), ff.Reason)
	}
	switch {
	case d.Aborted:
		fmt.Fprintf(&b, "**Aborted**: %s — the ramp was cut short; this is NOT the target's ceiling.\n", d.AbortNote)
	case d.FirstFailure() == nil && len(d.Stages) > 0:
		fmt.Fprintf(&b, "**No failure** through the top of the ramp (concurrency %d).\n",
			d.Stages[len(d.Stages)-1].Concurrency)
	case len(d.Stages) == 0:
		b.WriteString("**No stages ran.**\n")
	}
	return b.String()
}

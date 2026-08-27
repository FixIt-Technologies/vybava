package perfrig

import (
	"fmt"
	"sort"
)

// StagePlan is one resolved ramp step: an ordered, fully-populated count for
// every generator (missing generators default to 0 so a stage that only names
// "movers" implicitly holds "sessions" at 0).
type StagePlan struct {
	Index  int            `json:"index"`
	Counts map[string]int `json:"counts"`
}

// Total is the sum of all actor counts in the stage — the headline concurrency
// number the report plots latency against.
func (s StagePlan) Total() int {
	t := 0
	for _, c := range s.Counts {
		t += c
	}
	return t
}

// Label renders the stage as a stable "gen=count,gen=count" string (generators
// sorted) for logs and report rows.
func (s StagePlan) Label() string {
	ids := make([]string, 0, len(s.Counts))
	for id := range s.Counts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := ""
	for i, id := range ids {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprintf("%s=%d", id, s.Counts[id])
	}
	return out
}

// PlanRamp resolves the manifest's stages into fully-populated StagePlans in
// declaration order. Every generator id appears in every stage's Counts.
func PlanRamp(m Manifest) []StagePlan {
	genIDs := make([]string, 0, len(m.Generators))
	for _, g := range m.Generators {
		genIDs = append(genIDs, g.ID)
	}
	plans := make([]StagePlan, 0, len(m.Ramp.Stages))
	for i, stage := range m.Ramp.Stages {
		counts := make(map[string]int, len(genIDs))
		for _, id := range genIDs {
			counts[id] = stage[id] // zero value when the stage omits it
		}
		plans = append(plans, StagePlan{Index: i, Counts: counts})
	}
	return plans
}

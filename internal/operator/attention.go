package operator

import (
	"regexp"
	"strings"
	"time"
)

// Conservative signals nominate source data for inspection, never execution.
// Unknown wording stays observation-only.
var blockerSignal = regexp.MustCompile("(?im)^(?:\\*\\*)?(?:i(?:'m| am) blocked|blocked(?: on| by|:)|the (?:current |remaining |only )?blocker is|i (?:cannot|can't) (?:continue|proceed)|waiting (?:on|for) (?:your|lukáš|lukas) (?:approval|decision|input))")
var handoffSignal = regexp.MustCompile("(?im)^(?:\\*\\*)?(?:handoff (?:written|ready|prepared):|the handoff is written)")

func attentionReason(o Observation) string {
	if o.Source != Claude {
		return ""
	}
	if strings.HasPrefix(o.Text, "Claude requested a user decision") {
		return "question"
	}
	if blockerSignal.MatchString(o.Text) {
		return "blocker"
	}
	if handoffSignal.MatchString(o.Text) {
		return "handoff"
	}
	return ""
}

// NextAttention excludes history, transient progress and changed context.
// Receipt backpressure applies even to superseded deliveries.
func (s *State) NextAttention(since, now time.Time) string {
	for _, e := range s.Events {
		if e.AcknowledgedAt == nil && (e.Delivery == "queued" || e.Delivery == "submitting" || e.Delivery == "failed") {
			return ""
		}
	}
	for _, e := range s.Events {
		age := now.Sub(e.ObservedAt)
		if !e.Superseded && e.Delivery == "pending" && e.AcknowledgedAt == nil &&
			e.ObservedAt.After(since) && age >= 20*time.Second && age <= 10*time.Minute &&
			attentionReason(e.Observation) != "" {
			return e.ID
		}
	}
	return ""
}

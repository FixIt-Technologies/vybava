package operator

import (
	"errors"
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
		if eligibleAttention(e, since, now) {
			return e.ID
		}
	}
	return ""
}

func eligibleAttention(e Event, since, now time.Time) bool {
	age := now.Sub(e.ObservedAt)
	return !e.Superseded && e.Delivery == "pending" && e.AcknowledgedAt == nil &&
		e.ObservedAt.After(since) && age >= 20*time.Second && age <= 10*time.Minute &&
		attentionReason(e.Observation) != ""
}

// ReadyAttention lets other sessions proceed past removed or unfinished sources.
func (s *State) ReadyAttention(since, now time.Time) (string, error) {
	if s.NextAttention(since, now) == "" {
		return "", nil
	}
	for _, e := range s.Events {
		if !eligibleAttention(e, since, now) {
			continue
		}
		err := s.attentionSourceCurrent(e)
		if errors.Is(err, ErrAttentionChanged) {
			continue
		}
		if err != nil {
			return "", err
		}
		return e.ID, nil
	}
	return "", nil
}

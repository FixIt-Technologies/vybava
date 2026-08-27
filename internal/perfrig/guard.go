package perfrig

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Probe is one neighbor-canary observation.
type Probe struct {
	At        time.Time     `json:"at"`
	Status    int           `json:"status"`
	Latency   time.Duration `json:"latency"`
	Err       string        `json:"err,omitempty"`
	Breaching bool          `json:"breaching"`
}

// Breach reports whether a single probe violates the guard's thresholds
// (wrong/failed status, or latency over the abort ceiling). It is pure so the
// abort policy is directly unit-testable without a live endpoint.
func (g Guard) Breach(p Probe) bool {
	if p.Err != "" {
		return true
	}
	if p.Status != g.ExpectCode {
		return true
	}
	if g.AbortP95Ms > 0 && p.Latency > time.Duration(g.AbortP95Ms)*time.Millisecond {
		return true
	}
	return false
}

// probe performs one HTTP GET against the guard target. EVERY return path
// computes Breaching via Breach — an unreachable canary is a breach, not a
// healthy neighbor (Watch counts p.Breaching, so missing this here would make
// the guard blind exactly when the neighbor is down hard).
func (g Guard) probe(ctx context.Context, client *http.Client) Probe {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.Probe, nil)
	if err != nil {
		p := Probe{At: start, Err: err.Error()}
		p.Breaching = g.Breach(p)
		return p
	}
	resp, err := client.Do(req)
	lat := time.Since(start)
	if err != nil {
		p := Probe{At: start, Latency: lat, Err: err.Error()}
		p.Breaching = g.Breach(p)
		return p
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	p := Probe{At: start, Status: resp.StatusCode, Latency: lat}
	p.Breaching = g.Breach(p)
	return p
}

// Watch polls the guard on its interval until ctx is cancelled, emitting every
// probe on the returned channel. When `Breaches` consecutive probes breach, it
// calls onAbort exactly once (the caller uses that to stop the ramp) and keeps
// reporting so the operator sees the neighbor's recovery.
func (g Guard) Watch(ctx context.Context, onAbort func(Probe)) <-chan Probe {
	out := make(chan Probe, 8)
	client := &http.Client{Timeout: 5 * time.Second}
	go func() {
		defer close(out)
		ticker := time.NewTicker(time.Duration(g.IntervalS) * time.Second)
		defer ticker.Stop()
		consecutive := 0
		aborted := false
		emit := func(p Probe) {
			if p.Breaching {
				consecutive++
			} else {
				consecutive = 0
			}
			if !aborted && consecutive >= g.Breaches {
				aborted = true
				if onAbort != nil {
					onAbort(p)
				}
			}
			select {
			case out <- p:
			case <-ctx.Done():
			}
		}
		emit(g.probe(ctx, client)) // probe immediately, don't wait a full interval
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				emit(g.probe(ctx, client))
			}
		}
	}()
	return out
}

// String renders the guard's abort policy for the run banner.
func (g Guard) String() string {
	lat := "off"
	if g.AbortP95Ms > 0 {
		lat = fmt.Sprintf("%dms", g.AbortP95Ms)
	}
	return fmt.Sprintf("guard %q: %s every %ds, abort after %d breach(es) [expect %d, lat<%s]",
		g.Name, g.Probe, g.IntervalS, g.Breaches, g.ExpectCode, lat)
}

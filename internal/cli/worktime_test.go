package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/henderson-tech/vybava/internal/worktime"
)

func TestWorktimeSampleExplainsUnavailableContext(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		var out, errOut bytes.Buffer
		rt := runtime{stdout: &out, stderr: &errOut, json: asJSON}
		cmd := rt.worktimeCommandWithSources("worktime", func(context.Context) (worktime.Sample, error) {
			return worktime.Sample{Application: "com.apple.Terminal"}, nil
		}, func(ctx context.Context, s worktime.Sample) worktime.Sample {
			return worktime.DarwinWindowContext(ctx, s, func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("permission denied") })
		})
		cmd.SetArgs([]string{"sample", "--window-context"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if asJSON {
			var s worktime.Sample
			if err := json.Unmarshal(out.Bytes(), &s); err != nil {
				t.Fatal(err)
			}
			if s.ContextError == "" || errOut.Len() != 0 {
				t.Fatalf("JSON warning contract: %+v stderr=%s", s, errOut.String())
			}
		} else if !strings.Contains(errOut.String(), "Warning:") || !strings.Contains(errOut.String(), "Accessibility") || !strings.Contains(out.String(), "com.apple.Terminal") {
			t.Fatalf("human warning missing: stdout=%s stderr=%s", out.String(), errOut.String())
		}
	}
}

func TestWorktimeWatchBoundsAndCancellation(t *testing.T) {
	for _, interval := range []string{"0s", "61s"} {
		var out bytes.Buffer
		rt := runtime{stdout: &out, stderr: &out}
		calls := 0
		cmd := rt.worktimeCommandWithSources("worktime", func(context.Context) (worktime.Sample, error) { calls++; return worktime.Sample{}, nil }, worktime.WindowContext)
		cmd.SetArgs([]string{"watch", "--interval", interval})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "between 1s and 1m") || calls != 0 {
			t.Fatalf("invalid interval sampled: %s calls=%d err=%v", interval, calls, err)
		}
	}
	var out bytes.Buffer
	rt := runtime{stdout: &out, stderr: &out}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	cmd := rt.worktimeCommandWithSources("worktime", func(context.Context) (worktime.Sample, error) { calls++; cancel(); return worktime.Sample{}, nil }, worktime.WindowContext)
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"watch", "--interval", "1s"})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	if err := cmd.Execute(); !errors.Is(err, context.Canceled) || calls != 1 || out.Len() != 0 {
		t.Fatalf("watch cancellation: calls=%d output=%q err=%v", calls, out.String(), err)
	}
}

package perfrig

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Options tune a run without touching the manifest.
type Options struct {
	Stdout    io.Writer
	Stderr    io.Writer
	DryRun    bool   // print the plan (stages + commands), run nothing
	MaxStage  int    // stop after this stage index (<=0 = whole ramp); for calibration
	ShellPath string // shell to invoke commands with (default "/bin/bash")
}

var (
	p95Re    = regexp.MustCompile(`p95_ms=([0-9.]+)`)
	errRe    = regexp.MustCompile(`error_rate=([0-9.]+)`)
	entrySub = "{entry}"
)

// Runner executes a drill from a manifest.
type Runner struct {
	M   Manifest
	Opt Options
}

func (r Runner) out() io.Writer {
	if r.Opt.Stdout != nil {
		return r.Opt.Stdout
	}
	return os.Stdout
}

func (r Runner) shell() string {
	if r.Opt.ShellPath != "" {
		return r.Opt.ShellPath
	}
	return "/bin/bash"
}

func (r Runner) sub(cmd string) string {
	return strings.ReplaceAll(cmd, entrySub, r.M.Target.Entry)
}

// Plan prints what the drill WOULD do — every resolved stage and the exact
// commands — without running anything. This is the safe first look, especially
// for a prod-direct drill.
func (r Runner) Plan() string {
	var b strings.Builder
	fmt.Fprintf(&b, "drill: %s (mode=%s) -> %s\n", r.M.Project, r.M.Mode, r.M.Target.Entry)
	if r.M.Guard != nil {
		fmt.Fprintf(&b, "  %s\n", r.M.Guard.String())
	} else {
		b.WriteString("  guard: none\n")
	}
	if r.M.Seed != nil {
		fmt.Fprintf(&b, "  seed: %s\n", r.sub(r.M.Seed.Cmd))
	}
	if r.M.Auth != nil {
		fmt.Fprintf(&b, "  auth: %s\n", r.sub(r.M.Auth.Cmd))
	}
	for _, s := range PlanRamp(r.M) {
		fmt.Fprintf(&b, "  stage %d (concurrency %d): %s, hold %ds\n",
			s.Index, s.Total(), s.Label(), r.M.Ramp.HoldS)
	}
	for _, g := range r.M.Generators {
		fmt.Fprintf(&b, "  generator %q: %s=<count> %s\n", g.ID, g.ScaleEnv, r.sub(g.Cmd))
	}
	return b.String()
}

// Run executes the full drill: seed → (rig up) → staged ramp under the guard →
// teardown, returning the Drill record for the report. It stops at the first
// failed stage when StopOnFirstFailure is set, and always stops if the guard
// aborts (the neighbor is more important than finishing the ramp).
func (r Runner) Run(ctx context.Context) (Drill, error) {
	d := Drill{Project: r.M.Project, Mode: r.M.Mode, Target: r.M.Target.Entry, StartedAt: time.Now()}

	if r.Opt.DryRun {
		fmt.Fprint(r.out(), r.Plan())
		return d, nil
	}

	// Arm the neighbor guard first, before any load exists.
	var abortReason string
	guardCtx, stopGuard := context.WithCancel(ctx)
	defer stopGuard()
	if r.M.Guard != nil {
		fmt.Fprintf(r.out(), "arming %s\n", r.M.Guard.String())
		probes := r.M.Guard.Watch(guardCtx, func(p Probe) {
			abortReason = fmt.Sprintf("guard %q breached (status=%d lat=%s)", r.M.Guard.Name, p.Status, p.Latency)
			stopGuard() // signal the ramp loop via ctx
		})
		go func() {
			for range probes { // drain; onAbort already captured the decision
			}
		}()
	}

	if r.M.Seed != nil {
		fmt.Fprintln(r.out(), "== seed ==")
		if err := r.exec(ctx, r.M.Seed.Cmd, r.M.Seed.Dir, nil); err != nil {
			return d, fmt.Errorf("seed: %w", err)
		}
	}
	if r.M.Mode == ModeIsolatedRig && r.M.Stack != nil {
		fmt.Fprintln(r.out(), "== rig up ==")
		if err := r.exec(ctx, r.M.Stack.Up, r.M.Stack.Dir, nil); err != nil {
			return d, fmt.Errorf("stack up: %w", err)
		}
		defer func() {
			fmt.Fprintln(r.out(), "== rig down ==")
			_ = r.exec(context.Background(), r.M.Stack.Down, r.M.Stack.Dir, nil)
		}()
		if r.M.Stack.Ready != "" {
			if err := r.exec(ctx, r.M.Stack.Ready, r.M.Stack.Dir, nil); err != nil {
				return d, fmt.Errorf("rig never became ready: %w", err)
			}
		}
	}
	if r.M.Auth != nil {
		fmt.Fprintln(r.out(), "== auth ==")
		if err := r.exec(ctx, r.M.Auth.Cmd, r.M.Auth.Dir, nil); err != nil {
			return d, fmt.Errorf("auth: %w", err)
		}
	}

	genByID := map[string]Generator{}
	for _, g := range r.M.Generators {
		genByID[g.ID] = g
	}

	for _, stage := range PlanRamp(r.M) {
		if r.Opt.MaxStage > 0 && stage.Index >= r.Opt.MaxStage {
			break
		}
		if guardCtx.Err() != nil {
			d.Aborted = true
			d.AbortNote = abortReason
			break
		}
		res := StageResult{Stage: stage, Concurrency: stage.Total(),
			P95Ms: map[string]float64{}, ErrorRate: map[string]float64{}}
		fmt.Fprintf(r.out(), "== stage %d: %s (concurrency %d) ==\n", stage.Index, stage.Label(), stage.Total())

		for id, count := range stage.Counts {
			if count <= 0 {
				continue
			}
			g := genByID[id]
			env := []string{fmt.Sprintf("%s=%d", g.ScaleEnv, count)}
			var sink stageSink
			err := r.exec(guardCtx, g.Cmd, g.Dir, &sink, env...)
			if sink.hasP95 {
				res.P95Ms[id] = sink.p95
			}
			if sink.hasErr {
				res.ErrorRate[id] = sink.errRate
			}
			if err != nil {
				res.Failed = true
				if guardCtx.Err() != nil {
					res.Reason = abortReason
				} else {
					res.Reason = fmt.Sprintf("generator %q exited: %v", id, err)
				}
			}
		}
		d.Stages = append(d.Stages, res)
		if res.Failed && r.M.Ramp.StopOnFirstFailure {
			break
		}
		if guardCtx.Err() != nil {
			d.Aborted = true
			d.AbortNote = abortReason
			break
		}
	}
	return d, nil
}

// stageSink scrapes a generator's stdout for the p95_ms=/error_rate= markers
// perfrig standardizes on, while streaming it through to the operator.
type stageSink struct {
	p95     float64
	errRate float64
	hasP95  bool
	hasErr  bool
}

// exec runs a shell command, streaming output; if sink is non-nil it also
// scrapes the perf markers. extraEnv is appended to the process environment.
func (r Runner) exec(ctx context.Context, cmd, dir string, sink *stageSink, extraEnv ...string) error {
	cmd = r.sub(cmd)
	c := exec.CommandContext(ctx, r.shell(), "-c", cmd)
	if dir != "" {
		c.Dir = dir
	}
	c.Env = append(os.Environ(), extraEnv...)
	stdout, err := c.StdoutPipe()
	if err != nil {
		return err
	}
	c.Stderr = r.out()
	if err := c.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintln(r.out(), line)
		if sink != nil {
			if mm := p95Re.FindStringSubmatch(line); mm != nil {
				if v, e := strconv.ParseFloat(mm[1], 64); e == nil {
					sink.p95, sink.hasP95 = v, true
				}
			}
			if mm := errRe.FindStringSubmatch(line); mm != nil {
				if v, e := strconv.ParseFloat(mm[1], 64); e == nil {
					sink.errRate, sink.hasErr = v, true
				}
			}
		}
	}
	return c.Wait()
}

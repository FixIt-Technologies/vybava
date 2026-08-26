package perfrig

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Options tune a run without touching the manifest.
type Options struct {
	Stdout    io.Writer
	Stderr    io.Writer
	MaxStage  int    // run only the first N stages (<=0 = whole ramp); for calibration
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

	// outMu serializes writes from concurrently running generators so their
	// interleaved output stays line-atomic. Set by Run.
	outMu *sync.Mutex
}

func (r Runner) out() io.Writer {
	if r.Opt.Stdout != nil {
		return r.Opt.Stdout
	}
	return os.Stdout
}

func (r Runner) errOut() io.Writer {
	if r.Opt.Stderr != nil {
		return r.Opt.Stderr
	}
	return os.Stderr
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

// printLine writes one generator/step output line, prefixed with its label
// when several generators run concurrently.
func (r Runner) printLine(label, line string) {
	if r.outMu != nil {
		r.outMu.Lock()
		defer r.outMu.Unlock()
	}
	if label != "" {
		fmt.Fprintf(r.out(), "[%s] %s\n", label, line)
	} else {
		fmt.Fprintln(r.out(), line)
	}
}

// manifestEnv renders the manifest-level env map as KEY=VALUE pairs in a
// stable order.
func (r Runner) manifestEnv() []string {
	if len(r.M.Env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(r.M.Env))
	for k := range r.M.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, k := range keys {
		env = append(env, fmt.Sprintf("%s=%s", k, r.M.Env[k]))
	}
	return env
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
	if rc := r.M.Target.ReachableCheck; rc != "" {
		fmt.Fprintf(&b, "  reachable check: GET %s\n", r.reachableURL())
	}
	if r.M.Seed != nil {
		fmt.Fprintf(&b, "  seed: %s\n", r.sub(r.M.Seed.Cmd))
	}
	if r.M.Auth != nil {
		fmt.Fprintf(&b, "  auth: %s\n", r.sub(r.M.Auth.Cmd))
	}
	for _, e := range r.manifestEnv() {
		fmt.Fprintf(&b, "  env: %s\n", e)
	}
	for _, s := range PlanRamp(r.M) {
		fmt.Fprintf(&b, "  stage %d (concurrency %d): %s, hold %ds (deadline %s)\n",
			s.Index, s.Total(), s.Label(), r.M.Ramp.HoldS, r.M.StageDeadline())
	}
	for _, g := range r.M.Generators {
		fmt.Fprintf(&b, "  generator %q: %s=<count> PERFRIG_HOLD_S=%d %s\n",
			g.ID, g.ScaleEnv, r.M.Ramp.HoldS, r.sub(g.Cmd))
	}
	return b.String()
}

func (r Runner) reachableURL() string {
	rc := r.M.Target.ReachableCheck
	if !strings.HasPrefix(rc, "/") {
		rc = "/" + rc
	}
	return strings.TrimRight(r.M.Target.Entry, "/") + rc
}

// checkReachable refuses to start the ramp against a target that isn't
// answering — a down target would render every stage as a bogus "failure".
func (r Runner) checkReachable(ctx context.Context) error {
	url := r.reachableURL()
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("reachable check: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("target not reachable (%s): %w", url, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("target not reachable (%s): status %d", url, resp.StatusCode)
	}
	fmt.Fprintf(r.out(), "target reachable: GET %s -> %d\n", url, resp.StatusCode)
	return nil
}

// genResult is one generator's outcome within a stage.
type genResult struct {
	id   string
	sink stageSink
	err  error
}

// Run executes the full drill: seed → (rig up) → reachable check → guarded
// ramp → teardown, returning the Drill record for the report. All generators
// of a stage run CONCURRENTLY (the whole point of combined load); the stage
// ends when every generator exits. The drill stops at the first failed stage
// when StopOnFirstFailure is set, and always stops if the guard aborts (the
// neighbor is more important than finishing the ramp).
func (r Runner) Run(ctx context.Context) (Drill, error) {
	d := Drill{Project: r.M.Project, Mode: r.M.Mode, Target: r.M.Target.Entry, StartedAt: time.Now()}
	r.outMu = &sync.Mutex{}

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
		if err := r.exec(ctx, r.M.Seed.Cmd, r.M.Seed.Dir, nil, ""); err != nil {
			return d, fmt.Errorf("seed: %w", err)
		}
	}
	if r.M.Mode == ModeIsolatedRig && r.M.Stack != nil {
		fmt.Fprintln(r.out(), "== rig up ==")
		if err := r.exec(ctx, r.M.Stack.Up, r.M.Stack.Dir, nil, ""); err != nil {
			return d, fmt.Errorf("stack up: %w", err)
		}
		defer func() {
			fmt.Fprintln(r.out(), "== rig down ==")
			_ = r.exec(context.Background(), r.M.Stack.Down, r.M.Stack.Dir, nil, "")
		}()
		if r.M.Stack.Ready != "" {
			if err := r.exec(ctx, r.M.Stack.Ready, r.M.Stack.Dir, nil, ""); err != nil {
				return d, fmt.Errorf("rig never became ready: %w", err)
			}
		}
	}
	if r.M.Target.ReachableCheck != "" {
		if err := r.checkReachable(ctx); err != nil {
			return d, err
		}
	}
	if r.M.Auth != nil {
		fmt.Fprintln(r.out(), "== auth ==")
		if err := r.exec(ctx, r.M.Auth.Cmd, r.M.Auth.Dir, nil, ""); err != nil {
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
			break
		}
		res := StageResult{Stage: stage, Concurrency: stage.Total(),
			P95Ms: map[string]float64{}, ErrorRate: map[string]float64{}}
		fmt.Fprintf(r.out(), "== stage %d: %s (concurrency %d, hold %ds) ==\n",
			stage.Index, stage.Label(), stage.Total(), r.M.Ramp.HoldS)

		// Every generator of the stage launches together; the hard deadline
		// keeps a hung generator from wedging the drill.
		stageCtx, cancelStage := context.WithTimeout(guardCtx, r.M.StageDeadline())
		var wg sync.WaitGroup
		resCh := make(chan genResult, len(stage.Counts))
		for id, count := range stage.Counts {
			if count <= 0 {
				continue
			}
			g := genByID[id]
			wg.Add(1)
			go func(g Generator, count int) {
				defer wg.Done()
				env := []string{
					fmt.Sprintf("%s=%d", g.ScaleEnv, count),
					fmt.Sprintf("PERFRIG_HOLD_S=%d", r.M.Ramp.HoldS),
				}
				var sink stageSink
				err := r.exec(stageCtx, g.Cmd, g.Dir, &sink, g.ID, env...)
				resCh <- genResult{id: g.ID, sink: sink, err: err}
			}(g, count)
		}
		wg.Wait()
		close(resCh)
		deadlineHit := errors.Is(stageCtx.Err(), context.DeadlineExceeded)
		cancelStage()

		results := make([]genResult, 0, len(stage.Counts))
		for gr := range resCh {
			results = append(results, gr)
		}
		sort.Slice(results, func(i, j int) bool { return results[i].id < results[j].id })
		for _, gr := range results {
			if gr.sink.hasP95 {
				res.P95Ms[gr.id] = gr.sink.p95
			}
			if gr.sink.hasErr {
				res.ErrorRate[gr.id] = gr.sink.errRate
			}
			if gr.err == nil {
				continue
			}
			switch {
			case guardCtx.Err() != nil:
				// The guard (or the operator) pulled the plug mid-stage; the
				// generator dying from that kill is not the target's failure.
				res.Aborted = true
			case deadlineHit:
				res.Failed = true
				res.Reason = fmt.Sprintf("stage deadline %s exceeded (hung generator killed)", r.M.StageDeadline())
			default:
				res.Failed = true
				if res.Reason == "" {
					res.Reason = fmt.Sprintf("generator %q exited: %v", gr.id, gr.err)
				}
			}
		}
		if res.Aborted {
			res.Failed = false
			res.Reason = ""
		}
		d.Stages = append(d.Stages, res)
		// Guard abort beats first-failure attribution: check it first.
		if guardCtx.Err() != nil {
			break
		}
		if res.Failed && r.M.Ramp.StopOnFirstFailure {
			break
		}
	}

	if guardCtx.Err() != nil {
		d.Aborted = true
		d.AbortNote = abortReason
		if d.AbortNote == "" {
			d.AbortNote = "run cancelled (interrupt)"
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

// exec runs a shell command, streaming output (prefixed with label when
// non-empty); if sink is non-nil it also scrapes the perf markers. extraEnv
// is appended after the manifest env and the process environment.
//
// The command runs in its own process group; on ctx cancel the WHOLE group
// gets SIGTERM (a plain kill of the shell would orphan its children and leave
// the load running after a guard abort), with a bounded WaitDelay so an
// ignoring process can't wedge us.
func (r Runner) exec(ctx context.Context, cmd, dir string, sink *stageSink, label string, extraEnv ...string) error {
	cmd = r.sub(cmd)
	c := exec.CommandContext(ctx, r.shell(), "-c", cmd)
	if dir != "" {
		c.Dir = dir
	}
	c.Env = append(append(os.Environ(), r.manifestEnv()...), extraEnv...)
	setProcGroup(c)
	c.WaitDelay = 10 * time.Second
	stdout, err := c.StdoutPipe()
	if err != nil {
		return err
	}
	c.Stderr = &lockedWriter{mu: r.outMu, w: r.errOut()}
	if err := c.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
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
		r.printLine(label, line)
	}
	if serr := scanner.Err(); serr != nil {
		// Keep draining so the child never blocks on a full pipe, but tell
		// the operator markers may have been missed.
		fmt.Fprintf(r.errOut(), "perfrig: stdout scan aborted (%v); draining raw\n", serr)
		_, _ = io.Copy(io.Discard, stdout)
	}
	return c.Wait()
}

// lockedWriter serializes writes from concurrent generators onto one stream.
type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	if l.mu != nil {
		l.mu.Lock()
		defer l.mu.Unlock()
	}
	return l.w.Write(p)
}

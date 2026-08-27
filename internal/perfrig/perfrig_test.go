package perfrig

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func validIsolatedManifest() Manifest {
	m := Manifest{
		Schema:  1,
		Project: "fixit",
		Mode:    ModeIsolatedRig,
		Target:  Target{Entry: "http://10.9.0.5:8080", ReachableCheck: "/api/v1/health"},
		Stack:   &Stack{Dir: ".", Up: "true", Down: "true"},
		Generators: []Generator{
			{ID: "http", Cmd: "true", ScaleEnv: "VUS"},
			{ID: "sockets", Cmd: "true", ScaleEnv: "SOCKETS"},
		},
		Ramp: Ramp{Stages: []map[string]int{{"http": 200, "sockets": 200}, {"http": 1000}}},
	}
	m.applyDefaults()
	return m
}

func TestValidateIsolatedOK(t *testing.T) {
	if err := validIsolatedManifest().Validate(); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestValidateProdDirectRequiresGuard(t *testing.T) {
	m := Manifest{
		Schema:     1,
		Project:    "vitrinka",
		Mode:       ModeProdDirect,
		Target:     Target{Entry: "https://vitrinka.ai"},
		Generators: []Generator{{ID: "sessions", Cmd: "true", ScaleEnv: "SESSIONS"}},
		Ramp:       Ramp{Stages: []map[string]int{{"sessions": 100}}},
	}
	err := m.Validate()
	if err == nil || !strings.Contains(err.Error(), "guard block") {
		t.Fatalf("prod-direct without guard must fail on guard, got %v", err)
	}
}

func TestValidateProdDirectRejectsStack(t *testing.T) {
	m := Manifest{
		Schema:     1,
		Project:    "vitrinka",
		Mode:       ModeProdDirect,
		Target:     Target{Entry: "https://vitrinka.ai"},
		Guard:      &Guard{Name: "fixit-prod", Probe: "https://api.fixit.app/api/v1/health"},
		Stack:      &Stack{Up: "true"},
		Generators: []Generator{{ID: "sessions", Cmd: "true", ScaleEnv: "SESSIONS"}},
		Ramp:       Ramp{Stages: []map[string]int{{"sessions": 100}}},
	}
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "must not declare a stack") {
		t.Fatalf("prod-direct with stack must fail, got %v", err)
	}
}

func TestValidateUnknownGeneratorInStage(t *testing.T) {
	m := validIsolatedManifest()
	m.Ramp.Stages = []map[string]int{{"nope": 10}}
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "unknown generator") {
		t.Fatalf("expected unknown-generator error, got %v", err)
	}
}

func TestPlanRampFillsMissingGeneratorsWithZero(t *testing.T) {
	m := validIsolatedManifest()
	plans := PlanRamp(m)
	if len(plans) != 2 {
		t.Fatalf("want 2 stages, got %d", len(plans))
	}
	// Stage 1 declares only http:1000 — sockets must be filled to 0.
	if plans[1].Counts["sockets"] != 0 {
		t.Fatalf("missing generator should default to 0, got %d", plans[1].Counts["sockets"])
	}
	if plans[1].Total() != 1000 {
		t.Fatalf("stage total wrong: %d", plans[1].Total())
	}
	if got := plans[0].Label(); got != "http=200,sockets=200" {
		t.Fatalf("label = %q", got)
	}
}

func TestGuardBreach(t *testing.T) {
	g := Guard{ExpectCode: 200, AbortP95Ms: 1500}
	cases := []struct {
		name string
		p    Probe
		want bool
	}{
		{"healthy", Probe{Status: 200, Latency: 100 * time.Millisecond}, false},
		{"wrong status", Probe{Status: 503, Latency: 10 * time.Millisecond}, true},
		{"too slow", Probe{Status: 200, Latency: 2 * time.Second}, true},
		{"errored", Probe{Err: "connection refused"}, true},
	}
	for _, c := range cases {
		if got := g.Breach(c.p); got != c.want {
			t.Errorf("%s: Breach=%v want %v", c.name, got, c.want)
		}
	}
}

func TestGuardBreachLatencyIgnoredWhenZero(t *testing.T) {
	g := Guard{ExpectCode: 200, AbortP95Ms: 0}
	if g.Breach(Probe{Status: 200, Latency: 10 * time.Second}) {
		t.Fatal("latency threshold of 0 must disable the latency check")
	}
}

func TestDefaultsApplied(t *testing.T) {
	m := Manifest{Guard: &Guard{}, Ramp: Ramp{}}
	m.applyDefaults()
	if m.Ramp.HoldS != 60 || m.Guard.IntervalS != 10 || m.Guard.ExpectCode != 200 || m.Guard.Breaches != 2 {
		t.Fatalf("defaults not applied: %+v / %+v", m.Ramp, m.Guard)
	}
}

func TestDrillReportFirstFailure(t *testing.T) {
	d := Drill{
		Project: "fixit", Mode: ModeIsolatedRig, Target: "http://x", StartedAt: time.Unix(0, 0).UTC(),
		Stages: []StageResult{
			{Stage: StagePlan{Index: 0, Counts: map[string]int{"http": 200}}, Concurrency: 200, P95Ms: map[string]float64{"http": 72}},
			{Stage: StagePlan{Index: 1, Counts: map[string]int{"http": 2000}}, Concurrency: 2000, Failed: true, Reason: "5xx spike", P95Ms: map[string]float64{"http": 1900}},
		},
	}
	ff := d.FirstFailure()
	if ff == nil || ff.Concurrency != 2000 {
		t.Fatalf("first failure detection wrong: %+v", ff)
	}
	md := d.Markdown()
	for _, want := range []string{"Perf drill: fixit", "concurrency 2000", "FAIL: 5xx spike", "First failure at concurrency 2000"} {
		if !strings.Contains(md, want) {
			t.Errorf("report missing %q\n%s", want, md)
		}
	}
}

func TestRunnerPlanNoExec(t *testing.T) {
	r := Runner{M: validIsolatedManifest()}
	plan := r.Plan()
	for _, want := range []string{"drill: fixit", "stage 0", "generator \"http\"", "VUS=<count>", "PERFRIG_HOLD_S=60"} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan missing %q\n%s", want, plan)
		}
	}
}

// runnable returns an isolated-rig manifest whose stack is a no-op and whose
// reachable check is disabled, so Run exercises only the ramp machinery.
func runnable(generators []Generator, stages []map[string]int) Manifest {
	m := Manifest{
		Schema:     1,
		Project:    "test",
		Mode:       ModeIsolatedRig,
		Target:     Target{Entry: "http://127.0.0.1:1"},
		Stack:      &Stack{Dir: ".", Up: "true", Down: "true"},
		Generators: generators,
		Ramp:       Ramp{Stages: stages, StopOnFirstFailure: true},
	}
	m.applyDefaults()
	return m
}

func runIt(t *testing.T, m Manifest, opt Options) Drill {
	t.Helper()
	var out, errOut bytes.Buffer
	if opt.Stdout == nil {
		opt.Stdout = &out
	}
	if opt.Stderr == nil {
		opt.Stderr = &errOut
	}
	d, err := (Runner{M: m, Opt: opt}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v\nstdout:\n%s\nstderr:\n%s", err, out.String(), errOut.String())
	}
	return d
}

func TestRunStageGeneratorsRunConcurrently(t *testing.T) {
	m := runnable([]Generator{
		{ID: "a", Cmd: "echo p95_ms=1; sleep 0.5", ScaleEnv: "A"},
		{ID: "b", Cmd: "echo p95_ms=2; sleep 0.5", ScaleEnv: "B"},
	}, []map[string]int{{"a": 1, "b": 1}})
	start := time.Now()
	d := runIt(t, m, Options{})
	elapsed := time.Since(start)
	// Sequential execution would take >=1.0s; concurrent ~0.5s.
	if elapsed >= 900*time.Millisecond {
		t.Fatalf("stage took %s — generators did not run concurrently", elapsed)
	}
	if len(d.Stages) != 1 || d.Stages[0].P95Ms["a"] != 1 || d.Stages[0].P95Ms["b"] != 2 {
		t.Fatalf("markers not collected from both generators: %+v", d.Stages)
	}
}

func TestRunPassesScaleHoldAndManifestEnv(t *testing.T) {
	m := runnable([]Generator{{
		ID:       "g",
		Cmd:      `[ "$PERF_BOARD" = boardx ] && echo p95_ms=$VUS error_rate=$PERFRIG_HOLD_S`,
		ScaleEnv: "VUS",
	}}, []map[string]int{{"g": 7}})
	m.Env = map[string]string{"PERF_BOARD": "boardx"}
	d := runIt(t, m, Options{})
	s := d.Stages[0]
	if s.Failed {
		t.Fatalf("stage failed — PERF_BOARD from manifest env not visible: %+v", s)
	}
	if s.P95Ms["g"] != 7 {
		t.Fatalf("scale env not passed: p95=%v", s.P95Ms["g"])
	}
	if s.ErrorRate["g"] != 60 {
		t.Fatalf("PERFRIG_HOLD_S not passed: %v", s.ErrorRate["g"])
	}
}

func TestGuardAbortMidStageIsNotAFailure(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(200) // healthy while the stage launches
			return
		}
		w.WriteHeader(503)
	}))
	defer srv.Close()
	m := runnable([]Generator{{ID: "g", Cmd: "sleep 30", ScaleEnv: "N"}},
		[]map[string]int{{"g": 1}})
	m.Guard = &Guard{Name: "neighbor", Probe: srv.URL, IntervalS: 1, ExpectCode: 200, Breaches: 1}
	start := time.Now()
	d := runIt(t, m, Options{})
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("guard abort did not kill the generator (took %s)", elapsed)
	}
	if !d.Aborted || d.AbortNote == "" {
		t.Fatalf("drill not marked aborted: %+v", d)
	}
	if d.FirstFailure() != nil {
		t.Fatalf("guard abort misattributed as target failure: %+v", d.FirstFailure())
	}
	if len(d.Stages) != 1 || !d.Stages[0].Aborted || d.Stages[0].Failed {
		t.Fatalf("stage attribution wrong: %+v", d.Stages)
	}
	md := d.Markdown()
	for _, want := range []string{"guard abort", "NOT the target's ceiling"} {
		if !strings.Contains(md, want) {
			t.Errorf("report missing %q\n%s", want, md)
		}
	}
}

func TestRunReachableCheckRefusesDeadTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()
	m := runnable([]Generator{{ID: "g", Cmd: "true", ScaleEnv: "N"}}, []map[string]int{{"g": 1}})
	m.Target = Target{Entry: srv.URL, ReachableCheck: "/health"}
	var out bytes.Buffer
	_, err := (Runner{M: m, Opt: Options{Stdout: &out, Stderr: &out}}).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("expected reachable-check refusal, got %v", err)
	}
}

func TestRunMaxStageRunsOnlyFirstN(t *testing.T) {
	m := runnable([]Generator{{ID: "g", Cmd: "echo p95_ms=1", ScaleEnv: "N"}},
		[]map[string]int{{"g": 1}, {"g": 2}})
	d := runIt(t, m, Options{MaxStage: 1})
	if len(d.Stages) != 1 {
		t.Fatalf("--max-stage 1 must run exactly the first stage, ran %d", len(d.Stages))
	}
}

func TestMarkdownEmptyStagesDoesNotPanic(t *testing.T) {
	d := Drill{Project: "x", Mode: ModeProdDirect, Target: "http://x", StartedAt: time.Unix(0, 0).UTC()}
	if md := d.Markdown(); !strings.Contains(md, "No stages ran") {
		t.Fatalf("empty drill report wrong:\n%s", md)
	}
}

func TestGuardWatchAbortsOnUnreachableProbe(t *testing.T) {
	// An unreachable canary is a breach — a guard that treats connection
	// errors as healthy is blind exactly when the neighbor is down hard.
	g := Guard{Name: "n", Probe: "http://127.0.0.1:1/health", IntervalS: 1, ExpectCode: 200, Breaches: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	aborted := make(chan struct{})
	probes := g.Watch(ctx, func(Probe) { close(aborted) })
	go func() {
		for range probes {
		}
	}()
	select {
	case <-aborted:
	case <-time.After(4 * time.Second):
		t.Fatal("guard never aborted on an unreachable probe target")
	}
}

func TestStageDeadlineKillsHungGenerator(t *testing.T) {
	m := runnable([]Generator{{ID: "g", Cmd: "sleep 30", ScaleEnv: "N"}},
		[]map[string]int{{"g": 1}})
	start := time.Now()
	d := runIt(t, m, Options{StageTimeout: 500 * time.Millisecond})
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("hung generator not killed at the stage deadline (took %s)", elapsed)
	}
	s := d.Stages[0]
	if !s.Failed || !strings.Contains(s.Reason, "deadline") {
		t.Fatalf("deadline kill must FAIL the stage with a deadline reason: %+v", s)
	}
	if s.Aborted || d.Aborted {
		t.Fatalf("a deadline kill is a target failure, not a guard abort: %+v", s)
	}
}

func TestValidateRejectsNegativeGuardAndEmptyStackCommands(t *testing.T) {
	m := validIsolatedManifest()
	m.Guard = &Guard{Name: "n", Probe: "http://x/h", IntervalS: -1}
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "negative") {
		t.Fatalf("negative interval must be rejected, got %v", err)
	}
	m = validIsolatedManifest()
	m.Guard = &Guard{Name: "n"}
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "guard.probe") {
		t.Fatalf("blank guard probe must be rejected, got %v", err)
	}
	m = validIsolatedManifest()
	m.Stack = &Stack{Dir: ".", Up: " ", Down: "true"}
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "stack.up") {
		t.Fatalf("blank stack.up must be rejected, got %v", err)
	}
}

func TestLoadManifestRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "perf.manifest.yml")
	manifest := `schema: 1
project: p
mode: prod-direct
target: { entry: "https://x" }
guard: { name: n, probe: "https://x/h" }
generators: [{ id: g, cmd: "true", scale_env: N }]
ramp: { stages: [{ g: 1 }] }
metrics: { host: h }
`
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadManifest(path)
	if err == nil || !strings.Contains(err.Error(), "metrics") {
		t.Fatalf("unknown field must be rejected, got %v", err)
	}
}

package perfrig

import (
	"strings"
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
	for _, want := range []string{"drill: fixit", "stage 0", "generator \"http\"", "VUS=<count>"} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan missing %q\n%s", want, plan)
		}
	}
}

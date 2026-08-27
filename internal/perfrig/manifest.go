// Package perfrig is the reusable performance-drill orchestrator behind the
// `testing/<project>/perf/` convention. It is deliberately protocol-agnostic:
// each project declares HOW to seed, authenticate, and generate load as shell
// commands in a manifest, and perfrig owns only the parts that are generic and
// safety-critical across every project — the neighbor guard (abort canary),
// the ramp driver (staged concurrency, push-to-first-failure), and the report
// (latency percentiles vs concurrency).
//
// Two projects motivated the shape: FixIt (an isolated throwaway rig, k6 HTTP
// + socket.io swarm, offline-minted JWTs) and vitrinka (production directly,
// Go SSE/WS actors, API bench-seed). Anything that differs between those two
// is a manifest field, never a perfrig assumption.
package perfrig

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Mode selects how perfrig treats the target system.
type Mode string

const (
	// ModeIsolatedRig owns a throwaway stack: perfrig brings it up and tears
	// it down around the run (FixIt). Blast radius is the rig itself.
	ModeIsolatedRig Mode = "isolated-rig"
	// ModeProdDirect points generators at a live production system perfrig
	// does NOT manage (vitrinka). The neighbor guard is the only safety net,
	// so it is mandatory in this mode.
	ModeProdDirect Mode = "prod-direct"
)

// Manifest is the per-project source of truth: one perf.manifest.yml renders a
// whole drill. Adding a project is this file plus its seed/auth/generator
// commands — no perfrig code change. Parsing is strict: an unknown field is an
// error, never a silently ignored no-op, so the manifest can be trusted as the
// contract of what actually runs.
type Manifest struct {
	Schema  int    `yaml:"schema" json:"schema"`
	Project string `yaml:"project" json:"project"`
	Mode    Mode   `yaml:"mode" json:"mode"`

	Target     Target            `yaml:"target" json:"target"`
	Guard      *Guard            `yaml:"guard,omitempty" json:"guard,omitempty"`
	Stack      *Stack            `yaml:"stack,omitempty" json:"stack,omitempty"`
	Seed       *Step             `yaml:"seed,omitempty" json:"seed,omitempty"`
	Auth       *Step             `yaml:"auth,omitempty" json:"auth,omitempty"`
	Env        map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Generators []Generator       `yaml:"generators" json:"generators"`
	Ramp       Ramp              `yaml:"ramp" json:"ramp"`
	Report     Report            `yaml:"report" json:"report"`
}

// Target is what generators hit and how perfrig confirms it is live.
type Target struct {
	Entry string `yaml:"entry" json:"entry"` // base URL generators aim at
	// ReachableCheck is a path appended to Entry; before the ramp starts (and
	// after an isolated rig is up) perfrig GETs it and refuses to run unless
	// it answers with a 2xx/3xx.
	ReachableCheck string `yaml:"reachable_check" json:"reachable_check"`
}

// Guard is the neighbor canary. In prod-direct mode it protects the OTHER
// tenants on a shared host (vitrinka's drill guards FixIt-prod, same box); in
// isolated-rig mode it is optional insurance for disk/IO contention.
type Guard struct {
	Name       string `yaml:"name" json:"name"`
	Probe      string `yaml:"probe" json:"probe"`               // absolute URL to poll
	IntervalS  int    `yaml:"interval_s" json:"interval_s"`     // seconds between probes (default 10)
	ExpectCode int    `yaml:"expect_code" json:"expect_code"`   // required HTTP status (default 200)
	AbortP95Ms int    `yaml:"abort_p95_ms" json:"abort_p95_ms"` // abort if a probe's latency exceeds this (0 = ignore)
	Breaches   int    `yaml:"breaches" json:"breaches"`         // consecutive breaches before abort (default 2)
}

// Stack is how perfrig brings an isolated rig up and down (isolated-rig only).
type Stack struct {
	Dir   string `yaml:"dir" json:"dir"`     // working dir for the commands (compose project)
	Up    string `yaml:"up" json:"up"`       // e.g. "docker compose up -d"
	Down  string `yaml:"down" json:"down"`   // e.g. "docker compose down"
	Ready string `yaml:"ready" json:"ready"` // shell command that exits 0 when the rig serves
}

// Step is a one-shot shell command (seed / auth). {entry} is substituted.
type Step struct {
	Cmd string `yaml:"cmd" json:"cmd"`
	Dir string `yaml:"dir,omitempty" json:"dir,omitempty"`
}

// Generator is one actor type perfrig scales through the ramp. All generators
// named by a stage are launched CONCURRENTLY and the stage lasts until every
// one of them exits. Each invocation gets ScaleEnv set to the stage's count
// plus PERFRIG_HOLD_S — the generator is expected to sustain its load for
// about that many seconds and then exit (k6 duration, actor -duration flag…).
//
// Kill caveat: on a guard abort perfrig SIGTERMs the generator's local
// process group, but a command that tunnels through ssh must arrange for the
// remote side to die with the connection (ssh -tt, a remote timeout, or a
// bounded duration) — the client dying does not reliably kill a detached
// remote process (e.g. a docker container the remote CLI started).
type Generator struct {
	ID       string `yaml:"id" json:"id"`   // e.g. "http", "sockets", "movers"
	Cmd      string `yaml:"cmd" json:"cmd"` // shell command; {entry} substituted
	Dir      string `yaml:"dir,omitempty" json:"dir,omitempty"`
	ScaleEnv string `yaml:"scale_env" json:"scale_env"` // env var carrying the per-stage count (e.g. VUS)
}

// Ramp is the staged concurrency profile. Each stage maps generator-id -> count.
type Ramp struct {
	Stages []map[string]int `yaml:"stages" json:"stages"`
	// HoldS is how long each stage should sustain load. It is handed to every
	// generator as PERFRIG_HOLD_S; perfrig additionally enforces a hard stage
	// deadline of 2×hold+60s after which the stage is killed and marked
	// failed, so a hung generator can never wedge the drill.
	HoldS              int  `yaml:"hold_s" json:"hold_s"`
	StopOnFirstFailure bool `yaml:"stop_on_first_failure" json:"stop_on_first_failure"`
}

// Report controls where the drill artifact lands. Out is a directory (~ is
// expanded); each run writes a timestamped <project>-<start>.md into it, in
// addition to the report always going to stdout.
type Report struct {
	Out string `yaml:"out" json:"out"`
}

// LoadManifest reads and validates a perf.manifest.yml.
func LoadManifest(path string) (Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("parse %s: %w", path, err)
	}
	m.applyDefaults()
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func (m *Manifest) applyDefaults() {
	if m.Ramp.HoldS == 0 {
		m.Ramp.HoldS = 60
	}
	if m.Guard != nil {
		if m.Guard.IntervalS == 0 {
			m.Guard.IntervalS = 10
		}
		if m.Guard.ExpectCode == 0 {
			m.Guard.ExpectCode = 200
		}
		if m.Guard.Breaches == 0 {
			m.Guard.Breaches = 2
		}
	}
}

// Validate rejects manifests that would run an unsafe or meaningless drill.
func (m Manifest) Validate() error {
	var problems []string
	if m.Schema != 1 {
		problems = append(problems, fmt.Sprintf("schema must be 1, got %d", m.Schema))
	}
	if strings.TrimSpace(m.Project) == "" {
		problems = append(problems, "project is required")
	}
	if m.Guard != nil {
		if strings.TrimSpace(m.Guard.Probe) == "" {
			problems = append(problems, "guard.probe is required")
		}
		// Zero values get defaults; negatives would survive them and e.g. panic
		// time.NewTicker, so reject outright.
		if m.Guard.IntervalS < 0 || m.Guard.Breaches < 0 || m.Guard.AbortP95Ms < 0 {
			problems = append(problems, "guard interval_s/breaches/abort_p95_ms must not be negative")
		}
	}
	switch m.Mode {
	case ModeIsolatedRig:
		if m.Stack == nil {
			problems = append(problems, "isolated-rig mode requires a stack block")
		} else {
			// An empty command would "succeed" via bash -c "" and fake a rig
			// lifecycle that never happened.
			if strings.TrimSpace(m.Stack.Up) == "" {
				problems = append(problems, "stack.up is required")
			}
			if strings.TrimSpace(m.Stack.Down) == "" {
				problems = append(problems, "stack.down is required")
			}
		}
	case ModeProdDirect:
		// The whole point of prod-direct is that perfrig doesn't own the
		// target, so the neighbor guard is the only thing standing between a
		// runaway ramp and the other tenants. Refuse to run without it.
		if m.Guard == nil {
			problems = append(problems, "prod-direct mode requires a guard block (neighbor safety is mandatory)")
		}
		if m.Stack != nil {
			problems = append(problems, "prod-direct mode must not declare a stack (perfrig never manages production)")
		}
	default:
		problems = append(problems, fmt.Sprintf("mode must be %q or %q", ModeIsolatedRig, ModeProdDirect))
	}
	if strings.TrimSpace(m.Target.Entry) == "" {
		problems = append(problems, "target.entry is required")
	}
	if len(m.Generators) == 0 {
		problems = append(problems, "at least one generator is required")
	}
	genIDs := map[string]bool{}
	for i, g := range m.Generators {
		if g.ID == "" {
			problems = append(problems, fmt.Sprintf("generators[%d].id is required", i))
			continue
		}
		if genIDs[g.ID] {
			problems = append(problems, fmt.Sprintf("duplicate generator id %q", g.ID))
		}
		genIDs[g.ID] = true
		if strings.TrimSpace(g.Cmd) == "" {
			problems = append(problems, fmt.Sprintf("generator %q needs a cmd", g.ID))
		}
		if strings.TrimSpace(g.ScaleEnv) == "" {
			problems = append(problems, fmt.Sprintf("generator %q needs a scale_env", g.ID))
		}
	}
	if len(m.Ramp.Stages) == 0 {
		problems = append(problems, "ramp.stages must have at least one stage")
	}
	for i, stage := range m.Ramp.Stages {
		for id := range stage {
			if !genIDs[id] {
				problems = append(problems, fmt.Sprintf("ramp stage %d references unknown generator %q", i, id))
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid manifest:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// HoldDuration is the per-stage hold as a time.Duration.
func (m Manifest) HoldDuration() time.Duration {
	return time.Duration(m.Ramp.HoldS) * time.Second
}

// StageDeadline is the hard per-stage timeout: generous slack over the hold
// (generator ramp-up, summary flush) but bounded, so a hung generator fails
// the stage instead of wedging the drill forever.
func (m Manifest) StageDeadline() time.Duration {
	return 2*m.HoldDuration() + 60*time.Second
}

// Package repolicy converges GitHub repository settings to a declared policy.
//
// GitHub has no organization-level default for repository settings — only the
// default branch NAME is inherited, so a knob like "automatically delete head
// branches" is set per repository and every new repository starts off-policy.
// This package reads the owner's repositories through `gh`, reports the ones
// that drift from the declared policy, and converges them.
package repolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Runner executes a process; tests substitute a scripted fake.
type Runner interface {
	// Run executes name with args in dir and returns trimmed stdout.
	Run(dir, name string, args ...string) (string, error)
}

// knob is one settable repository boolean: the policy key an operator writes,
// the `gh repo list --json` field that reads it, and the REST field that
// writes it. The three names differ per knob — GraphQL and REST disagree.
type knob struct {
	Key  string
	List string
	REST string
}

// Vocabulary is the complete set of settings a policy may declare, in report
// order. Extending it is one line plus its doc row — but only with a knob
// `gh repo list --json` actually serves: auto-merge, for one, is writable over
// REST yet absent from the list fields, and a sweep that needs one extra API
// call per repository is not a sweep.
var Vocabulary = []knob{
	{"deleteBranchOnMerge", "deleteBranchOnMerge", "delete_branch_on_merge"},
	{"allowMergeCommit", "mergeCommitAllowed", "allow_merge_commit"},
	{"allowSquashMerge", "squashMergeAllowed", "allow_squash_merge"},
	{"allowRebaseMerge", "rebaseMergeAllowed", "allow_rebase_merge"},
}

// Policy is the declared desired state. Only settings it names are compared or
// written — anything a policy does not mention is left exactly as it is.
type Policy struct {
	Owners   []string        `yaml:"owners" json:"owners,omitempty"`
	Exclude  []string        `yaml:"exclude" json:"exclude,omitempty"`
	Settings map[string]bool `yaml:"settings" json:"settings"`
}

// DefaultPolicy is what ships when no policy file is given: merged branches
// disappear, everything else stays the owner's business.
func DefaultPolicy() Policy {
	return Policy{Settings: map[string]bool{"deleteBranchOnMerge": true}}
}

// LoadPolicy reads a YAML policy file and validates its vocabulary.
//
// Decoding is strict: a mistyped top-level key (`excludes:` for `exclude:`)
// would otherwise load cleanly, leave the field empty, and let apply write to
// the very repositories the operator wrote the file to protect.
func LoadPolicy(path string) (Policy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Policy{}, fmt.Errorf("read policy: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var p Policy
	if err := decoder.Decode(&p); err != nil && !errors.Is(err, io.EOF) {
		return Policy{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return p, p.Validate()
}

// Validate refuses a policy that declares nothing or names a setting outside
// the vocabulary — a typo must never pass as "no drift".
func (p Policy) Validate() error {
	if len(p.Settings) == 0 {
		return fmt.Errorf("policy declares no settings — one of: %s", strings.Join(keys(), ", "))
	}
	for key := range p.Settings {
		if find(key) == nil {
			return fmt.Errorf("unknown setting %q — one of: %s", key, strings.Join(keys(), ", "))
		}
	}
	for _, repo := range p.Exclude {
		if strings.Count(repo, "/") != 1 {
			return fmt.Errorf("exclude %q must be owner/repo", repo)
		}
	}
	return nil
}

// declared returns the policy's knobs in vocabulary order.
func (p Policy) declared() []knob {
	var out []knob
	for _, k := range Vocabulary {
		if _, ok := p.Settings[k.Key]; ok {
			out = append(out, k)
		}
	}
	return out
}

// Options tune the sweep itself, never the desired state.
type Options struct {
	Limit           int
	IncludeArchived bool
}

// Drift is one repository setting that does not match the policy.
type Drift struct {
	Repo    string `json:"repo"`
	Setting string `json:"setting"`
	Want    bool   `json:"want"`
	Got     bool   `json:"got"`
}

// Change is the outcome of converging one repository.
type Change struct {
	Repo     string   `json:"repo"`
	Settings []string `json:"settings"`
	OK       bool     `json:"ok"`
	Error    string   `json:"error,omitempty"`
}

// Report is the stable envelope both verbs emit.
type Report struct {
	Owners   []string        `json:"owners"`
	Settings map[string]bool `json:"settings"`
	Checked  int             `json:"checked"`
	Skipped  []string        `json:"skipped,omitempty"`
	Drift    []Drift         `json:"drift"`
	Applied  []Change        `json:"applied,omitempty"`
	Warnings []string        `json:"warnings,omitempty"`
}

// Failures lists the repositories Apply could not converge.
func (r Report) Failures() []Change {
	var out []Change
	for _, c := range r.Applied {
		if !c.OK {
			out = append(out, c)
		}
	}
	return out
}

// Audit reads every owner's repositories and reports the drift. It never
// writes.
func Audit(r Runner, p Policy, owners []string, opts Options) (Report, error) {
	if err := p.Validate(); err != nil {
		return Report{}, err
	}
	if len(owners) == 0 {
		owners = p.Owners
	}
	if len(owners) == 0 {
		return Report{}, fmt.Errorf("no owners — pass them as arguments or declare owners: in the policy")
	}
	if opts.Limit <= 0 {
		opts.Limit = 500
	}
	excluded := index(p.Exclude)
	report := Report{Owners: owners, Settings: p.Settings}

	for _, owner := range owners {
		repos, err := list(r, owner, p, opts)
		if err != nil {
			return Report{}, err
		}
		if len(repos) == opts.Limit {
			report.Warnings = append(report.Warnings,
				fmt.Sprintf("%s returned exactly --limit %d repositories — raise it, the tail was not read", owner, opts.Limit))
		}
		for _, repo := range repos {
			name, _ := repo["nameWithOwner"].(string)
			if name == "" {
				continue
			}
			if excluded[name] {
				report.Skipped = append(report.Skipped, name)
				continue
			}
			report.Checked++
			for _, k := range p.declared() {
				want := p.Settings[k.Key]
				got, ok := repo[k.List].(bool)
				if !ok {
					report.Warnings = append(report.Warnings,
						fmt.Sprintf("%s: gh did not report %s — setting skipped", name, k.List))
					continue
				}
				if got != want {
					report.Drift = append(report.Drift, Drift{Repo: name, Setting: k.Key, Want: want, Got: got})
				}
			}
		}
	}
	sort.SliceStable(report.Drift, func(i, j int) bool { return report.Drift[i].Repo < report.Drift[j].Repo })
	return report, nil
}

// Apply audits, then converges each drifting repository with ONE PATCH
// carrying all of its off-policy settings. A repository the token cannot
// administer is reported, never fatal — the rest of the sweep still lands.
func Apply(r Runner, p Policy, owners []string, opts Options) (Report, error) {
	report, err := Audit(r, p, owners, opts)
	if err != nil {
		return report, err
	}
	for _, repo := range order(report.Drift) {
		args := []string{"api", "-X", "PATCH", "repos/" + repo, "--silent"}
		var names []string
		for _, d := range report.Drift {
			if d.Repo != repo {
				continue
			}
			names = append(names, d.Setting)
			args = append(args, "-F", find(d.Setting).REST+"="+strconv.FormatBool(d.Want))
		}
		change := Change{Repo: repo, Settings: names, OK: true}
		if _, err := r.Run("", "gh", args...); err != nil {
			change.OK, change.Error = false, err.Error()
		}
		report.Applied = append(report.Applied, change)
	}
	return report, nil
}

// list asks gh for exactly the fields the policy declares — never the whole
// repository object, and never a field an older gh would reject.
func list(r Runner, owner string, p Policy, opts Options) ([]map[string]any, error) {
	fields := []string{"nameWithOwner"}
	for _, k := range p.declared() {
		fields = append(fields, k.List)
	}
	args := []string{"repo", "list", owner, "--limit", strconv.Itoa(opts.Limit), "--json", strings.Join(fields, ",")}
	if !opts.IncludeArchived {
		args = append(args, "--no-archived")
	}
	out, err := r.Run("", "gh", args...)
	if err != nil {
		return nil, fmt.Errorf("gh repo list %s: %w", owner, err)
	}
	var repos []map[string]any
	if err := json.Unmarshal([]byte(out), &repos); err != nil {
		return nil, fmt.Errorf("gh repo list %s: unreadable output: %w", owner, err)
	}
	return repos, nil
}

// order returns the drifting repositories once each, in report order.
func order(drift []Drift) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range drift {
		if !seen[d.Repo] {
			seen[d.Repo], out = true, append(out, d.Repo)
		}
	}
	return out
}

func index(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, v := range values {
		out[v] = true
	}
	return out
}

func find(key string) *knob {
	for i, k := range Vocabulary {
		if k.Key == key {
			return &Vocabulary[i]
		}
	}
	return nil
}

func keys() []string {
	out := make([]string, 0, len(Vocabulary))
	for _, k := range Vocabulary {
		out = append(out, k.Key)
	}
	return out
}

// Package hotfix drives one release-lineage hotfix end to end: a branch cut
// from the tag production runs, an isolated worktree, a PR against the
// default branch (the forward-port), a production deploy dispatched ON the
// hotfix branch, and the merge that lands the fix — and its tag — on main.
// Every verb is idempotent and re-derives state from git and gh; nothing is
// cached on disk.
package hotfix

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ConfigFile is the repo-root manifest a project ships to opt into the lane.
const ConfigFile = "hotfix.yaml"

// Config is the project manifest. Templates expand {slug} (the hotfix slug),
// {name} (worktree name), {branch}, {from} (git ref), {path} (worktree path)
// and {root} (primary checkout).
type Config struct {
	V             int            `yaml:"v"`
	DefaultBranch string         `yaml:"default_branch"`
	TagGlob       string         `yaml:"tag_glob"`
	BranchPrefix  string         `yaml:"branch_prefix"`
	Worktree      WorktreeConfig `yaml:"worktree"`
	Deploy        DeployConfig   `yaml:"deploy"`
	PR            PRConfig       `yaml:"pr"`
}

type WorktreeConfig struct {
	Name    string `yaml:"name"`
	Create  string `yaml:"create"`
	Path    string `yaml:"path"`
	Cleanup string `yaml:"cleanup"`
}

type DeployConfig struct {
	Workflow string            `yaml:"workflow"`
	Inputs   map[string]string `yaml:"inputs"`
}

type PRConfig struct {
	Labels     []string `yaml:"labels"`
	MergeFlags []string `yaml:"merge_flags"`
}

// DefaultConfig is what `hotfix init` writes and what unset keys fall back to.
func DefaultConfig() Config {
	return Config{
		V:             1,
		DefaultBranch: "main",
		TagGlob:       "v[0-9]*",
		BranchPrefix:  "hotfix/",
		Worktree: WorktreeConfig{
			Name:    "hotfix-{slug}",
			Create:  "git worktree add -b {branch} {path} {from}",
			Path:    ".worktrees/{name}",
			Cleanup: "git worktree remove {path}",
		},
		Deploy: DeployConfig{
			Workflow: "deploy-production.yml",
			Inputs:   map[string]string{"release_type": "patch"},
		},
		PR: PRConfig{Labels: []string{"hotfix"}, MergeFlags: []string{"--merge"}},
	}
}

// ErrConfigMissing means the primary checkout has no hotfix.yaml.
var ErrConfigMissing = errors.New("hotfix.yaml not found")

// LoadConfig reads <root>/hotfix.yaml, layering it over the defaults.
func LoadConfig(root string) (Config, error) {
	cfg := DefaultConfig()
	raw, err := os.ReadFile(filepath.Join(root, ConfigFile))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, ErrConfigMissing
	}
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", ConfigFile, err)
	}
	if cfg.V != 1 {
		return cfg, fmt.Errorf("%s: unsupported v=%d (want 1)", ConfigFile, cfg.V)
	}
	if !strings.HasSuffix(cfg.BranchPrefix, "/") {
		return cfg, fmt.Errorf("%s: branch_prefix must end with '/'", ConfigFile)
	}
	return cfg, nil
}

// WriteDefaultConfig materialises the defaults with explanatory comments.
// It never overwrites an existing manifest.
func WriteDefaultConfig(root string) (string, error) {
	path := filepath.Join(root, ConfigFile)
	if _, err := os.Stat(path); err == nil {
		return path, os.ErrExist
	}
	return path, os.WriteFile(path, []byte(defaultConfigText), 0o644)
}

const defaultConfigText = `# hotfix.yaml — release-lineage hotfix lane (Výbava ` + "`hotfix`" + ` applet).
# Templates expand {slug} {name} {branch} {from} {path} {root}.
v: 1
default_branch: main
tag_glob: "v[0-9]*"          # stable release tags; pre-releases (with '-') are ignored
branch_prefix: hotfix/       # hotfix branches are <prefix><slug>
worktree:
  name: "hotfix-{slug}"
  path: ".worktrees/{name}"
  # Runs in the primary checkout. Must check out {branch} (creating it from
  # {from} when it does not exist yet) at {path}.
  create: "git worktree add -b {branch} {path} {from}"
  # Printed as the last ` + "`next`" + ` step after finish; never run by the applet.
  cleanup: "git worktree remove {path}"
deploy:
  workflow: deploy-production.yml   # workflow_dispatch, run with --ref {branch}
  inputs:
    release_type: patch
pr:
  labels: [hotfix]
  merge_flags: ["--merge"]   # a merge commit keeps the release tag reachable from main
`

// Vars is the template vocabulary for one hotfix.
type Vars struct {
	Slug, Name, Branch, From, Path, Root string
}

// Expand substitutes every {key} of Vars into template.
func Expand(template string, v Vars) string {
	r := strings.NewReplacer(
		"{slug}", v.Slug, "{name}", v.Name, "{branch}", v.Branch,
		"{from}", v.From, "{path}", v.Path, "{root}", v.Root,
	)
	return r.Replace(template)
}

// VarsFor derives the deterministic names for a slug.
func (c Config) VarsFor(root, slug, from string) Vars {
	v := Vars{Slug: slug, Root: root, From: from, Branch: c.BranchPrefix + slug}
	v.Name = Expand(c.Worktree.Name, v)
	p := Expand(c.Worktree.Path, v)
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	v.Path = p
	return v
}

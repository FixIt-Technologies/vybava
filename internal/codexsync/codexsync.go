// Package codexsync renders the Claude Code personal home (skills and
// commands) into the structure Codex actually discovers.
//
// Claude command files are converted to skills: directories containing
// SKILL.md with `name` and `description`. The documented
// user scope is $HOME/.agents/skills. So a Claude skill becomes a skill copied
// verbatim (nesting preserved — Codex treats every directory holding a SKILL.md
// as its own skill), and a Claude command becomes a generated
// source-command-<slug> skill wrapping the command body.
//
// The render is deterministic: same inputs, byte-identical outputs, so drift is
// checkable in CI and a re-run is a no-op.
package codexsync

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ManifestName is the record of what a previous run generated. Pruning only
// ever removes paths this file claims, so an unmanaged skill dropped into
// ~/.agents/skills by hand survives every run.
const ManifestName = ".codexsync.json"

const (
	managedBegin = "# >>> codexsync managed — do not edit by hand"
	managedEnd   = "# <<< codexsync managed"
)

// Config locates the homes involved. Every field is absolute.
type Config struct {
	ClaudeHome string // ~/.claude — the source of truth
	AgentsHome string // ~/.agents — the Codex user scope we render into
	CodexHome  string // ~/.codex  — config.toml and the legacy prompts/skills trees
	BackupRoot string // ~/Backups — where displaced files go before we touch them
}

// Entry is one rendered skill directory.
type Entry struct {
	// Slug is the path under <AgentsHome>/skills, using forward slashes.
	Slug string `json:"slug"`
	// Kind is "skill" for a copied Claude skill, "command" for a generated
	// source-command-* wrapper.
	Kind string `json:"kind"`
	// Source is the Claude path this was rendered from.
	Source string `json:"source"`
	// Files maps a path relative to the entry directory to its content hash.
	Files map[string]string `json:"files"`
}

// Plan is the full desired state, independent of what is currently on disk.
type Plan struct {
	Entries []Entry `json:"entries"`
	// Suppress lists SKILL.md paths outside AgentsHome that would be
	// discovered as duplicates of a rendered entry.
	Suppress []string `json:"suppress"`
	// StalePrompts lists ~/.codex/prompts paths that Codex cannot read.
	StalePrompts []string `json:"stalePrompts"`

	files map[string][]byte // slug/relpath -> rendered content
	modes map[string]fs.FileMode
}

// manifest is the on-disk record of the previous run.
type manifest struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Report describes what an Apply actually changed.
type Report struct {
	Written   []string `json:"written"`
	Removed   []string `json:"removed"`
	Backup    string   `json:"backup,omitempty"`
	Config    string   `json:"config,omitempty"`
	Manifest  bool     `json:"manifest"`
	Unchanged int      `json:"unchanged"`
}

// BuildPlan reads the Claude home and renders the desired Codex-side tree.
// It touches nothing.
func BuildPlan(cfg Config) (*Plan, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	plan := &Plan{files: map[string][]byte{}, modes: map[string]fs.FileMode{}}

	skills, err := planSkills(cfg, plan)
	if err != nil {
		return nil, err
	}
	commands, err := planCommands(cfg, plan)
	if err != nil {
		return nil, err
	}
	plan.Entries = append(skills, commands...)
	sort.Slice(plan.Entries, func(i, j int) bool { return plan.Entries[i].Slug < plan.Entries[j].Slug })
	for i := 1; i < len(plan.Entries); i++ {
		if plan.Entries[i-1].Slug == plan.Entries[i].Slug {
			return nil, fmt.Errorf("duplicate destination %q: %s and %s", plan.Entries[i].Slug, plan.Entries[i-1].Source, plan.Entries[i].Source)
		}
	}

	plan.Suppress, err = collectSuppressions(cfg, plan)
	if err != nil {
		return nil, err
	}
	plan.StalePrompts, err = collectStalePrompts(cfg)
	if err != nil {
		return nil, err
	}
	return plan, nil
}

// planSkills copies each top-level directory under <ClaudeHome>/skills that
// contains at least one SKILL.md. The whole subtree comes along: references/,
// scripts/, and nested skill directories keep working because Codex discovers
// each SKILL.md in the tree on its own.
func planSkills(cfg Config, plan *Plan) ([]Entry, error) {
	root := filepath.Join(cfg.ClaudeHome, "skills")
	dirs, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read claude skills: %w", err)
	}

	var entries []Entry
	for _, dir := range dirs {
		if strings.HasPrefix(dir.Name(), ".") {
			continue
		}
		source := filepath.Join(root, dir.Name())
		info, err := os.Stat(source)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			continue
		}
		hasSkill, err := containsSkillFile(source)
		if err != nil {
			return nil, err
		}
		if !hasSkill {
			continue
		}

		entry := Entry{Slug: dir.Name(), Kind: "skill", Source: source, Files: map[string]string{}}
		err = walkFiles(source, func(rel string, body []byte, mode fs.FileMode) error {
			entry.Files[rel] = hashOf(body)
			plan.files[path.Join(entry.Slug, rel)] = body
			plan.modes[path.Join(entry.Slug, rel)] = mode.Perm()
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", source, err)
		}
		// Merge policy only after all source files have been copied, so a
		// source sidecar cannot undo the Claude opt-out during traversal.
		for rel := range entry.Files {
			if path.Base(rel) != "SKILL.md" {
				continue
			}
			meta, _, err := parseFrontmatter(plan.files[path.Join(entry.Slug, rel)])
			if err != nil {
				return nil, fmt.Errorf("%s/%s: %w", source, rel, err)
			}
			if !meta.DisableModelInvocation {
				continue
			}
			sidecar := path.Join(path.Dir(rel), "agents", "openai.yaml")
			full := path.Join(entry.Slug, sidecar)
			content, err := disableImplicitInvocation(plan.files[full])
			if err != nil {
				return nil, fmt.Errorf("%s/%s: %w", source, sidecar, err)
			}
			entry.Files[sidecar] = hashOf(content)
			plan.files[full] = content
			if plan.modes[full] == 0 {
				plan.modes[full] = 0o644
			}
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// planCommands renders every <ClaudeHome>/commands/**/*.md as its own skill.
// Nested commands flatten into the slug (me/timesheet/backfill.md becomes
// source-command-me-timesheet-backfill) because Codex has no command namespace
// to mirror and a flat, greppable name is what the user types.
func planCommands(cfg Config, plan *Plan) ([]Entry, error) {
	root := filepath.Join(cfg.ClaudeHome, "commands")
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	var entries []Entry
	err := walkFiles(root, func(rel string, body []byte, _ fs.FileMode) error {
		if path.Ext(rel) != ".md" {
			return nil
		}
		p := filepath.Join(root, filepath.FromSlash(rel))

		command := strings.TrimSuffix(filepath.ToSlash(rel), ".md")
		slug := "source-command-" + strings.ReplaceAll(command, "/", "-")
		meta, rest, err := parseFrontmatter(body)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		description := meta.Description
		if description == "" {
			description = deriveDescription(rest)
		}

		skill := renderCommandSkill(slug, command, description, rest)
		entry := Entry{Slug: slug, Kind: "command", Source: p, Files: map[string]string{
			"SKILL.md": hashOf(skill),
		}}
		plan.files[path.Join(slug, "SKILL.md")] = skill
		plan.modes[path.Join(slug, "SKILL.md")] = 0o644

		if meta.DisableModelInvocation {
			content := []byte("policy:\n  allow_implicit_invocation: false\n")
			entry.Files["agents/openai.yaml"] = hashOf(content)
			plan.files[path.Join(slug, "agents", "openai.yaml")] = content
			plan.modes[path.Join(slug, "agents", "openai.yaml")] = 0o644
		}
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk commands: %w", err)
	}
	return entries, nil
}

func renderCommandSkill(slug, command, description string, body []byte) []byte {
	var out bytes.Buffer
	out.WriteString("---\n")
	fmt.Fprintf(&out, "name: %s\n", slug)
	fmt.Fprintf(&out, "description: %s\n", yamlScalar(description))
	out.WriteString("---\n\n")
	fmt.Fprintf(&out, "# /%s\n\n", command)
	fmt.Fprintf(&out, "Generated by `codexsync` from `~/.claude/commands/%s.md`. Edit the command, re-run `codexsync`; edits here are overwritten.\n\n", command)
	fmt.Fprintf(&out, "Follow the instructions below when the user asks for `/%s`. Arguments the user passed follow the command name.\n\n", command)
	out.WriteString("---\n\n")
	out.Write(bytes.TrimRight(body, "\n"))
	out.WriteString("\n")
	return out.Bytes()
}

// collectSuppressions finds SKILL.md files Codex would discover outside the
// AgentsHome that duplicate something we rendered, so config.toml can silence
// them. Without this the same skill shows up several times in Codex's picker.
func collectSuppressions(cfg Config, plan *Plan) ([]string, error) {
	rendered := map[string]bool{}
	for _, entry := range plan.Entries {
		for rel := range entry.Files {
			if filepath.Base(rel) == "SKILL.md" {
				rendered[path.Join(entry.Slug, rel)] = true
			}
		}
	}

	var out []string
	for _, root := range []string{
		filepath.Join(cfg.ClaudeHome, "skills"),
		filepath.Join(cfg.CodexHome, "skills"),
	} {
		if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		err := walkFiles(root, func(rel string, body []byte, _ fs.FileMode) error {
			if path.Base(rel) == "SKILL.md" && rendered[rel] {
				// Same relative path alone does not establish a duplicate: a
				// Codex-only variant may carry different instructions.
				if root == filepath.Join(cfg.ClaudeHome, "skills") || bytes.Equal(body, plan.files[rel]) {
					out = append(out, filepath.Join(root, filepath.FromSlash(rel)))
				}
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scan duplicates in %s: %w", root, err)
		}
	}
	sort.Strings(out)
	return out, nil
}

// collectStalePrompts reports ~/.codex/prompts entries Codex cannot use: it
// reads flat <name>.md files there, so a directory or a dangling symlink is
// dead weight.
func collectStalePrompts(cfg Config) ([]string, error) {
	root := filepath.Join(cfg.CodexHome, "prompts")
	items, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, item := range items {
		if strings.HasPrefix(item.Name(), ".") {
			continue
		}
		full := filepath.Join(root, item.Name())
		if item.IsDir() {
			out = append(out, full)
			continue
		}
		info, err := os.Stat(full)
		if errors.Is(err, fs.ErrNotExist) && item.Type()&os.ModeSymlink != 0 {
			out = append(out, full) // dangling symlink
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.IsDir() || filepath.Ext(item.Name()) != ".md" {
			out = append(out, full)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Check reports whether the rendered tree on disk already matches the plan.
type DriftError struct {
	Paths []string
}

func (e *DriftError) Error() string {
	return fmt.Sprintf("codexsync drift (%d):\n  %s", len(e.Paths), strings.Join(e.Paths, "\n  "))
}

func (*DriftError) ExitCode() int { return 1 }

func Check(cfg Config, plan *Plan) error {
	report, err := Apply(cfg, plan, true)
	if err != nil {
		return err
	}
	var drift []string
	for _, rel := range report.Written {
		drift = append(drift, "missing or changed "+rel)
	}
	for _, stale := range report.Removed {
		drift = append(drift, "orphaned "+stale)
	}
	if report.Config != "" {
		drift = append(drift, "changed config.toml")
	}
	if report.Manifest {
		drift = append(drift, "changed "+ManifestName)
	}
	if len(drift) == 0 {
		return nil
	}
	sort.Strings(drift)
	return &DriftError{Paths: drift}
}

// Apply writes the plan. Anything it is about to displace is copied under
// BackupRoot first, because these homes hold hand-written history.
func Apply(cfg Config, plan *Plan, dryRun bool) (*Report, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	if plan == nil || plan.files == nil {
		return nil, errors.New("apply requires a plan from BuildPlan")
	}
	report := &Report{Written: []string{}, Removed: []string{}}
	root := filepath.Join(cfg.AgentsHome, "skills")
	previous, err := readManifest(cfg)
	if err != nil {
		return nil, err
	}
	owned := map[string]bool{}
	for _, entry := range previous.Entries {
		for rel := range entry.Files {
			owned[path.Join(entry.Slug, rel)] = true
		}
	}
	stale := staleFromManifest(previous, plan)
	var preserve []string
	type pendingWrite struct {
		path string
		body []byte
		mode fs.FileMode
	}
	var writes []pendingWrite

	rels := make([]string, 0, len(plan.files))
	for rel := range plan.files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	for _, rel := range rels {
		if err := validateTarget(root, rel); err != nil {
			return nil, err
		}
		target := filepath.Join(root, filepath.FromSlash(rel))
		existing, mode, exists, err := readRegular(target)
		if err != nil {
			return nil, err
		}
		if exists && !owned[rel] {
			return nil, fmt.Errorf("refusing to overwrite unmanaged file %s; move it aside before applying", target)
		}
		if exists && bytes.Equal(existing, plan.files[rel]) && mode == plan.modes[rel] {
			report.Unchanged++
			continue
		}
		report.Written = append(report.Written, rel)
		writes = append(writes, pendingWrite{target, plan.files[rel], plan.modes[rel]})
		if exists {
			preserve = append(preserve, target)
		}
	}

	for _, orphan := range stale {
		if err := validateTarget(root, orphan); err != nil {
			return nil, err
		}
		target := filepath.Join(root, filepath.FromSlash(orphan))
		if _, _, exists, err := readRegular(target); err != nil {
			return nil, err
		} else if !exists {
			continue
		}
		report.Removed = append(report.Removed, orphan)
		preserve = append(preserve, target)
	}
	for _, orphan := range plan.StalePrompts {
		if filepath.Dir(orphan) != filepath.Join(cfg.CodexHome, "prompts") {
			return nil, fmt.Errorf("unexpected prompt path %s", orphan)
		}
		if err := validateTarget(cfg.CodexHome, "prompts"); err != nil {
			return nil, err
		}
		report.Removed = append(report.Removed, orphan)
		preserve = append(preserve, orphan)
	}

	manifestBody, err := json.MarshalIndent(manifest{Version: 1, Entries: plan.Entries}, "", "  ")
	if err != nil {
		return nil, err
	}
	manifestBody = append(manifestBody, '\n')
	configBody, err := renderConfigBlock(cfg, plan)
	if err != nil {
		return nil, err
	}
	for _, state := range []pendingWrite{
		{filepath.Join(root, ManifestName), manifestBody, 0o644},
		{filepath.Join(cfg.CodexHome, "config.toml"), configBody, 0o600},
	} {
		if err := validateTarget(filepath.Dir(state.path), filepath.Base(state.path)); err != nil {
			return nil, err
		}
		existing, mode, exists, err := readRegular(state.path)
		if err != nil {
			return nil, err
		}
		if exists && bytes.Equal(existing, state.body) {
			continue
		}
		if exists {
			preserve = append(preserve, state.path)
			state.mode = mode
		}
		writes = append(writes, state)
		if filepath.Base(state.path) == ManifestName {
			report.Manifest = true
		} else {
			report.Config = state.path
		}
	}
	if dryRun {
		return report, nil
	}
	// Every read and ownership/config check completes before the first write.
	// A failed backup must stop the operation before any original is changed.
	report.Backup, err = backup(cfg, preserve)
	if err != nil {
		return nil, err
	}
	for _, orphan := range report.Removed {
		if filepath.IsAbs(orphan) {
			if err := os.RemoveAll(orphan); err != nil {
				return nil, err
			}
			continue
		}
		target := filepath.Join(root, filepath.FromSlash(orphan))
		if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if err := removeEmptyParents(root, filepath.Dir(target)); err != nil {
			return nil, err
		}
	}
	for _, write := range writes {
		if err := atomicWrite(write.path, write.body, write.mode); err != nil {
			return nil, err
		}
	}
	return report, nil
}

// staleFromManifest returns entry-relative paths the previous run generated
// that the current plan no longer wants.
func staleFromManifest(previous *manifest, plan *Plan) []string {
	wanted := map[string]bool{}
	for rel := range plan.files {
		wanted[rel] = true
	}
	var out []string
	for _, entry := range previous.Entries {
		for rel := range entry.Files {
			full := path.Join(entry.Slug, rel)
			if !wanted[full] {
				out = append(out, full)
			}
		}
	}
	sort.Strings(out)
	return out
}

func readManifest(cfg Config) (*manifest, error) {
	if err := validateTarget(filepath.Join(cfg.AgentsHome, "skills"), ManifestName); err != nil {
		return nil, err
	}
	body, err := os.ReadFile(filepath.Join(cfg.AgentsHome, "skills", ManifestName))
	if errors.Is(err, fs.ErrNotExist) {
		return &manifest{Version: 1}, nil
	}
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	if m.Version != 1 {
		return nil, fmt.Errorf("unsupported codexsync manifest version %d", m.Version)
	}
	seen := map[string]bool{}
	for _, entry := range m.Entries {
		if !validRelative(entry.Slug) || strings.Contains(entry.Slug, "/") || entry.Slug == ManifestName || seen[entry.Slug] {
			return nil, fmt.Errorf("invalid or duplicate manifest slug %q", entry.Slug)
		}
		seen[entry.Slug] = true
		for rel := range entry.Files {
			if !validRelative(rel) {
				return nil, fmt.Errorf("invalid manifest file %q", rel)
			}
		}
	}
	return &m, nil
}

// renderConfigBlock replaces the managed region of config.toml with the current
// suppression set, leaving every hand-written setting around it untouched.
func renderConfigBlock(cfg Config, plan *Plan) ([]byte, error) {
	target := filepath.Join(cfg.CodexHome, "config.toml")
	existing, err := os.ReadFile(target)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	var block bytes.Buffer
	block.WriteString(managedBegin + "\n")
	block.WriteString("# Duplicate discovery of skills rendered into ~/.agents/skills.\n")
	block.WriteString("# Regenerate with: codexsync apply\n")
	for _, p := range plan.Suppress {
		block.WriteString("\n[[skills.config]]\n")
		quoted, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&block, "path = %s\n", quoted)
		block.WriteString("enabled = false\n")
	}
	block.WriteString(managedEnd + "\n")

	return replaceManagedBlock(existing, block.Bytes())
}

func replaceManagedBlock(existing, block []byte) ([]byte, error) {
	starts, ends := bytes.Count(existing, []byte(managedBegin)), bytes.Count(existing, []byte(managedEnd))
	if starts != ends || starts > 1 {
		return nil, errors.New("config.toml has malformed codexsync markers; repair the managed block before applying")
	}
	start := bytes.Index(existing, []byte(managedBegin))
	if start < 0 {
		if len(existing) == 0 {
			return block, nil
		}
		return append(append(append([]byte{}, existing...), []byte("\n\n")...), block...), nil
	}
	end := bytes.Index(existing[start:], []byte(managedEnd))
	if end < 0 {
		return nil, errors.New("config.toml codexsync end marker precedes its begin marker")
	}
	end += start
	for markerStart, marker := range map[int]string{start: managedBegin, end: managedEnd} {
		after := markerStart + len(marker)
		if (markerStart > 0 && existing[markerStart-1] != '\n') || (after < len(existing) && existing[after] != '\n') {
			return nil, errors.New("config.toml codexsync markers must occupy complete lines")
		}
	}
	end += len(managedEnd)
	if end < len(existing) && existing[end] == '\n' {
		end++
	}
	out := append([]byte{}, existing[:start]...)
	out = append(out, block...)
	return append(out, existing[end:]...), nil
}

// backup copies paths under BackupRoot before they are removed. Backups live
// outside git by design — they are insurance, not history.
func backup(cfg Config, paths []string) (string, error) {
	if len(paths) == 0 {
		return "", nil
	}
	backupRoot := filepath.Join(cfg.BackupRoot, "codexsync")
	if err := os.MkdirAll(backupRoot, 0o700); err != nil {
		return "", err
	}
	dest, err := os.MkdirTemp(backupRoot, "backup-")
	if err != nil {
		return "", err
	}
	skillsRoot := filepath.Join(cfg.AgentsHome, "skills")

	for _, p := range paths {
		source := p
		if !filepath.IsAbs(source) {
			source = filepath.Join(skillsRoot, filepath.FromSlash(p))
		}
		rel, err := filepath.Rel(filepath.Dir(cfg.AgentsHome), source)
		if err != nil || strings.HasPrefix(rel, "..") {
			rel = strings.TrimPrefix(source, string(filepath.Separator))
		}
		if err := copyTree(source, filepath.Join(dest, rel)); err != nil {
			return "", err
		}
	}
	return dest, nil
}

func copyTree(source, dest string) error {
	info, err := os.Lstat(source)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // nothing to preserve
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(source)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return os.Symlink(target, dest)
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("cannot back up non-regular file %s", source)
		}
		body, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dest, body, info.Mode().Perm())
	}
	if err := os.MkdirAll(dest, info.Mode().Perm()); err != nil {
		return err
	}
	children, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := copyTree(filepath.Join(source, child.Name()), filepath.Join(dest, child.Name())); err != nil {
			return err
		}
	}
	return nil
}

func containsSkillFile(root string) (bool, error) {
	found := false
	err := walkFiles(root, func(rel string, _ []byte, _ fs.FileMode) error {
		if path.Base(rel) == "SKILL.md" {
			found = true
		}
		return nil
	})
	return found, err
}

// walkFiles visits every regular file under root, following symlinked
// directories — a skill home is full of them, and Codex follows them too, so a
// copy that stopped at the link would ship an empty directory. Cycles are cut
// by tracking resolved directories already visited.
func walkFiles(root string, visit func(rel string, body []byte, mode fs.FileMode) error) error {
	seen := map[string]bool{}

	var walk func(dir, prefix string) error
	walk = func(dir, prefix string) error {
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return err
		}
		if seen[resolved] {
			return nil
		}
		seen[resolved] = true
		defer delete(seen, resolved) // repeated aliases are not cycles

		items, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		names := make([]string, 0, len(items))
		for _, item := range items {
			names = append(names, item.Name())
		}
		sort.Strings(names) // deterministic order regardless of filesystem

		for _, name := range names {
			if name == ".git" || ignoredFile(name) {
				continue
			}
			full := filepath.Join(dir, name)
			info, err := os.Stat(full) // Stat, not Lstat: resolve the link
			if err != nil {
				return err
			}
			rel := path.Join(prefix, name)
			if info.IsDir() {
				if err := walk(full, rel); err != nil {
					return err
				}
				continue
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("unsupported source file %s (%s)", full, info.Mode())
			}
			body, err := os.ReadFile(full)
			if err != nil {
				return err
			}
			if err := visit(rel, body, info.Mode()); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(root, "")
}

func ignoredFile(name string) bool {
	return name == ".DS_Store" || strings.HasSuffix(name, ".swp")
}

func hashOf(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

type frontmatter struct {
	Description            string `yaml:"description"`
	DisableModelInvocation bool   `yaml:"disable-model-invocation"`
}

func parseFrontmatter(body []byte) (frontmatter, []byte, error) {
	var meta frontmatter
	normalized := bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	if !bytes.HasPrefix(normalized, []byte("---\n")) {
		return meta, body, nil
	}
	offset := 4
	for _, line := range bytes.SplitAfter(normalized[4:], []byte("\n")) {
		if bytes.Equal(bytes.TrimSpace(line), []byte("---")) {
			err := yaml.Unmarshal(normalized[4:offset], &meta)
			return meta, bytes.TrimLeft(normalized[offset+len(line):], "\n"), err
		}
		offset += len(line)
	}
	return meta, nil, errors.New("unterminated YAML frontmatter")
}

func disableImplicitInvocation(body []byte) ([]byte, error) {
	var doc yaml.Node
	if len(body) == 0 {
		body = []byte("{}\n")
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("openai.yaml must be a mapping")
	}
	root := doc.Content[0]
	policy := mappingValue(root, "policy")
	if policy == nil {
		policy = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "policy"}, policy)
	}
	if policy.Kind != yaml.MappingNode {
		return nil, errors.New("openai.yaml policy must be a mapping")
	}
	value := mappingValue(policy, "allow_implicit_invocation")
	if value == nil {
		value = &yaml.Node{Kind: yaml.ScalarNode}
		policy.Content = append(policy.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "allow_implicit_invocation"}, value)
	}
	value.Kind, value.Tag, value.Value = yaml.ScalarNode, "!!bool", "false"
	return yaml.Marshal(&doc)
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// deriveDescription falls back to the first prose line when a command carries
// no description, so every generated skill still tells Codex when to fire.
func deriveDescription(body []byte) string {
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "---") {
			continue
		}
		if runes := []rune(trimmed); len(runes) > 300 {
			trimmed = string(runes[:300])
		}
		return trimmed
	}
	return "Migrated Claude Code command."
}

// yamlScalar quotes a description so a colon or a leading dash cannot break the
// frontmatter Codex parses.
func yamlScalar(value string) string {
	quoted, _ := json.Marshal(strings.Join(strings.Fields(value), " "))
	return string(quoted)
}

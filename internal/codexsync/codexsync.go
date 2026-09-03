// Package codexsync renders the Claude Code personal home (skills and
// commands) into the structure Codex actually discovers.
//
// Codex has no notion of "commands", and its only extension surface is a skill:
// a directory containing SKILL.md with `name` and `description`. The documented
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
	"time"
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
	Unchanged int      `json:"unchanged"`
}

// BuildPlan reads the Claude home and renders the desired Codex-side tree.
// It touches nothing.
func BuildPlan(cfg Config) (*Plan, error) {
	plan := &Plan{files: map[string][]byte{}}

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

	plan.Suppress = collectSuppressions(cfg, plan.Entries)
	plan.StalePrompts = collectStalePrompts(cfg)
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
		if !dir.IsDir() || strings.HasPrefix(dir.Name(), ".") {
			continue
		}
		source := filepath.Join(root, dir.Name())
		hasSkill, err := containsSkillFile(source)
		if err != nil {
			return nil, err
		}
		if !hasSkill {
			continue
		}

		entry := Entry{Slug: dir.Name(), Kind: "skill", Source: source, Files: map[string]string{}}
		err = walkFiles(source, func(rel string, body []byte) error {
			entry.Files[rel] = hashOf(body)
			plan.files[path.Join(entry.Slug, rel)] = body

			// A Claude skill opting out of model invocation must opt out on the
			// Codex side too, via the sidecar Codex reads for that policy.
			if filepath.Base(rel) == "SKILL.md" && frontmatterBool(body, "disable-model-invocation") {
				sidecar := path.Join(path.Dir(rel), "agents", "openai.yaml")
				sidecar = strings.TrimPrefix(sidecar, "./")
				content := []byte("policy:\n  allow_implicit_invocation: false\n")
				entry.Files[sidecar] = hashOf(content)
				plan.files[path.Join(entry.Slug, sidecar)] = content
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", source, err)
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
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(d.Name()) != ".md" || ignoredFile(d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}

		command := strings.TrimSuffix(filepath.ToSlash(rel), ".md")
		slug := "source-command-" + strings.ReplaceAll(command, "/", "-")
		front, rest := splitFrontmatter(body)
		description := frontmatterString(front, "description")
		if description == "" {
			description = deriveDescription(rest)
		}

		skill := renderCommandSkill(slug, command, description, rest)
		entry := Entry{Slug: slug, Kind: "command", Source: p, Files: map[string]string{
			"SKILL.md": hashOf(skill),
		}}
		plan.files[path.Join(slug, "SKILL.md")] = skill

		if frontmatterBool(body, "disable-model-invocation") {
			content := []byte("policy:\n  allow_implicit_invocation: false\n")
			entry.Files["agents/openai.yaml"] = hashOf(content)
			plan.files[path.Join(slug, "agents", "openai.yaml")] = content
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
func collectSuppressions(cfg Config, entries []Entry) []string {
	rendered := map[string]bool{}
	for _, entry := range entries {
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
		// An unreadable tree simply suppresses nothing.
		_ = walkFiles(root, func(rel string, _ []byte) error {
			if path.Base(rel) == "SKILL.md" && rendered[rel] {
				out = append(out, filepath.Join(root, filepath.FromSlash(rel)))
			}
			return nil
		})
	}
	sort.Strings(out)
	return out
}

// collectStalePrompts reports ~/.codex/prompts entries Codex cannot use: it
// reads flat <name>.md files there, so a directory or a dangling symlink is
// dead weight.
func collectStalePrompts(cfg Config) []string {
	root := filepath.Join(cfg.CodexHome, "prompts")
	items, err := os.ReadDir(root)
	if err != nil {
		return nil
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
		if _, err := os.Stat(full); err != nil {
			out = append(out, full) // dangling symlink
			continue
		}
		if filepath.Ext(item.Name()) != ".md" {
			out = append(out, full)
		}
	}
	sort.Strings(out)
	return out
}

// Check reports whether the rendered tree on disk already matches the plan.
func Check(cfg Config, plan *Plan) error {
	root := filepath.Join(cfg.AgentsHome, "skills")
	var drift []string

	for rel, want := range plan.files {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			drift = append(drift, "missing "+rel)
			continue
		}
		if !bytes.Equal(got, want) {
			drift = append(drift, "changed "+rel)
		}
	}
	for _, stale := range staleFromManifest(cfg, plan) {
		drift = append(drift, "orphaned "+stale)
	}
	if len(drift) == 0 {
		return nil
	}
	sort.Strings(drift)
	return fmt.Errorf("codexsync drift (%d):\n  %s", len(drift), strings.Join(drift, "\n  "))
}

// Apply writes the plan. Anything it is about to displace is copied under
// BackupRoot first, because these homes hold hand-written history.
func Apply(cfg Config, plan *Plan, dryRun bool) (*Report, error) {
	report := &Report{}
	root := filepath.Join(cfg.AgentsHome, "skills")

	stale := staleFromManifest(cfg, plan)
	needsBackup := len(stale) > 0 || len(plan.StalePrompts) > 0

	if needsBackup && !dryRun {
		stamp, err := backup(cfg, append(append([]string{}, stale...), plan.StalePrompts...))
		if err != nil {
			return nil, err
		}
		report.Backup = stamp
	}

	rels := make([]string, 0, len(plan.files))
	for rel := range plan.files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	for _, rel := range rels {
		target := filepath.Join(root, filepath.FromSlash(rel))
		if existing, err := os.ReadFile(target); err == nil && bytes.Equal(existing, plan.files[rel]) {
			report.Unchanged++
			continue
		}
		report.Written = append(report.Written, rel)
		if dryRun {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(target, plan.files[rel], 0o644); err != nil {
			return nil, err
		}
	}

	for _, orphan := range stale {
		report.Removed = append(report.Removed, orphan)
		if dryRun {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, filepath.FromSlash(orphan))); err != nil {
			return nil, err
		}
	}
	for _, orphan := range plan.StalePrompts {
		report.Removed = append(report.Removed, orphan)
		if dryRun {
			continue
		}
		if err := os.RemoveAll(orphan); err != nil {
			return nil, err
		}
	}

	if !dryRun {
		if err := writeManifest(cfg, plan); err != nil {
			return nil, err
		}
		changed, err := writeConfigBlock(cfg, plan)
		if err != nil {
			return nil, err
		}
		if changed {
			report.Config = filepath.Join(cfg.CodexHome, "config.toml")
		}
	}
	return report, nil
}

// staleFromManifest returns entry-relative paths the previous run generated
// that the current plan no longer wants.
func staleFromManifest(cfg Config, plan *Plan) []string {
	previous, err := readManifest(cfg)
	if err != nil {
		return nil
	}
	wanted := map[string]bool{}
	for rel := range plan.files {
		wanted[rel] = true
	}
	slugs := map[string]bool{}
	for _, entry := range plan.Entries {
		slugs[entry.Slug] = true
	}

	var out []string
	for _, entry := range previous.Entries {
		if !slugs[entry.Slug] {
			out = append(out, entry.Slug) // whole entry retired
			continue
		}
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
	body, err := os.ReadFile(filepath.Join(cfg.AgentsHome, "skills", ManifestName))
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func writeManifest(cfg Config, plan *Plan) error {
	body, err := json.MarshalIndent(manifest{Version: 1, Entries: plan.Entries}, "", "  ")
	if err != nil {
		return err
	}
	target := filepath.Join(cfg.AgentsHome, "skills", ManifestName)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, append(body, '\n'), 0o644)
}

// writeConfigBlock replaces the managed region of config.toml with the current
// suppression set, leaving every hand-written setting around it untouched.
func writeConfigBlock(cfg Config, plan *Plan) (bool, error) {
	target := filepath.Join(cfg.CodexHome, "config.toml")
	existing, err := os.ReadFile(target)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}

	var block bytes.Buffer
	block.WriteString(managedBegin + "\n")
	block.WriteString("# Duplicate discovery of skills rendered into ~/.agents/skills.\n")
	block.WriteString("# Regenerate with: codexsync apply\n")
	for _, p := range plan.Suppress {
		block.WriteString("\n[[skills.config]]\n")
		fmt.Fprintf(&block, "path = %q\n", p)
		block.WriteString("enabled = false\n")
	}
	block.WriteString(managedEnd + "\n")

	updated := replaceManagedBlock(existing, block.Bytes())
	if bytes.Equal(existing, updated) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(target, updated, 0o644)
}

func replaceManagedBlock(existing, block []byte) []byte {
	start := bytes.Index(existing, []byte(managedBegin))
	if start < 0 {
		trimmed := bytes.TrimRight(existing, "\n")
		if len(trimmed) == 0 {
			return block
		}
		return append(append(trimmed, []byte("\n\n")...), block...)
	}
	end := bytes.Index(existing[start:], []byte(managedEnd))
	if end < 0 {
		return append(append([]byte{}, existing[:start]...), block...)
	}
	end += start + len(managedEnd)
	for end < len(existing) && existing[end] == '\n' {
		end++
	}
	out := append([]byte{}, existing[:start]...)
	out = append(out, block...)
	return append(out, existing[end:]...)
}

// backup copies paths under BackupRoot before they are removed. Backups live
// outside git by design — they are insurance, not history.
func backup(cfg Config, paths []string) (string, error) {
	if len(paths) == 0 {
		return "", nil
	}
	dest := filepath.Join(cfg.BackupRoot, "codexsync", stampName())
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
	if err != nil {
		return nil // nothing to preserve
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(source)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dest+".symlink", []byte(target+"\n"), 0o644)
	}
	if !info.IsDir() {
		body, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dest, body, 0o644)
	}
	return filepath.WalkDir(source, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // best-effort insurance copy
		}
		rel, err := filepath.Rel(source, p)
		if err != nil {
			return err
		}
		return copyTree(p, filepath.Join(dest, rel))
	})
}

func containsSkillFile(root string) (bool, error) {
	found := false
	err := walkFiles(root, func(rel string, _ []byte) error {
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
func walkFiles(root string, visit func(rel string, body []byte) error) error {
	seen := map[string]bool{}

	var walk func(dir, prefix string) error
	walk = func(dir, prefix string) error {
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return nil // a dangling link contributes nothing
		}
		if seen[resolved] {
			return nil
		}
		seen[resolved] = true

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
				continue
			}
			rel := path.Join(prefix, name)
			if info.IsDir() {
				if err := walk(full, rel); err != nil {
					return err
				}
				continue
			}
			body, err := os.ReadFile(full)
			if err != nil {
				return err
			}
			if err := visit(rel, body); err != nil {
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

// splitFrontmatter returns the YAML frontmatter block and the body after it.
func splitFrontmatter(body []byte) (front, rest []byte) {
	if !bytes.HasPrefix(body, []byte("---\n")) {
		return nil, body
	}
	end := bytes.Index(body[4:], []byte("\n---"))
	if end < 0 {
		return nil, body
	}
	front = body[4 : 4+end]
	rest = body[4+end:]
	if idx := bytes.IndexByte(rest, '\n'); idx >= 0 {
		rest = rest[idx+1:]
	}
	return front, bytes.TrimLeft(rest, "\n")
}

func frontmatterString(front []byte, key string) string {
	for _, line := range strings.Split(string(front), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, key+":") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, key+":"))
		return strings.Trim(value, `"'`)
	}
	return ""
}

func frontmatterBool(body []byte, key string) bool {
	front, _ := splitFrontmatter(body)
	return frontmatterString(front, key) == "true"
}

// deriveDescription falls back to the first prose line when a command carries
// no description, so every generated skill still tells Codex when to fire.
func deriveDescription(body []byte) string {
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "---") {
			continue
		}
		if len(trimmed) > 300 {
			trimmed = trimmed[:300]
		}
		return trimmed
	}
	return "Migrated Claude Code command."
}

// yamlScalar quotes a description so a colon or a leading dash cannot break the
// frontmatter Codex parses.
func yamlScalar(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	return `"` + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`) + `"`
}

func stampName() string {
	return time.Now().UTC().Format("20060102-150405")
}

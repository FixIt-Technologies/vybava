package codexsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newHome(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	cfg := Config{
		ClaudeHome: filepath.Join(root, ".claude"),
		AgentsHome: filepath.Join(root, ".agents"),
		CodexHome:  filepath.Join(root, ".codex"),
		BackupRoot: filepath.Join(root, "Backups"),
	}
	for _, dir := range []string{
		filepath.Join(cfg.ClaudeHome, "skills"),
		filepath.Join(cfg.ClaudeHome, "commands"),
		filepath.Join(cfg.CodexHome),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return cfg
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func applied(t *testing.T, cfg Config) *Report {
	t.Helper()
	plan, err := BuildPlan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Apply(cfg, plan, false)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestNestedSkillsCopyWholeTree(t *testing.T) {
	cfg := newHome(t)
	write(t, filepath.Join(cfg.ClaudeHome, "skills", "git", "commit", "SKILL.md"), "---\nname: commit\ndescription: Commit\n---\nbody\n")
	write(t, filepath.Join(cfg.ClaudeHome, "skills", "git", "references", "notes.md"), "notes\n")

	applied(t, cfg)

	// Codex reads every directory holding a SKILL.md, so nesting must survive
	// verbatim rather than being flattened.
	if got := read(t, filepath.Join(cfg.AgentsHome, "skills", "git", "commit", "SKILL.md")); !strings.Contains(got, "name: commit") {
		t.Fatalf("nested skill not copied: %q", got)
	}
	if read(t, filepath.Join(cfg.AgentsHome, "skills", "git", "references", "notes.md")) != "notes\n" {
		t.Fatal("sibling reference not copied")
	}
}

func TestDirectoryWithoutSkillFileIsSkipped(t *testing.T) {
	cfg := newHome(t)
	write(t, filepath.Join(cfg.ClaudeHome, "skills", "scratch", "README.md"), "not a skill\n")

	applied(t, cfg)

	if _, err := os.Stat(filepath.Join(cfg.AgentsHome, "skills", "scratch")); !os.IsNotExist(err) {
		t.Fatal("copied a directory that holds no SKILL.md")
	}
}

func TestCommandBecomesSourceCommandSkill(t *testing.T) {
	cfg := newHome(t)
	write(t, filepath.Join(cfg.ClaudeHome, "commands", "me", "timesheet", "backfill.md"),
		"---\ndescription: Backfill the timesheet\n---\nDo the thing.\n")

	applied(t, cfg)

	got := read(t, filepath.Join(cfg.AgentsHome, "skills", "source-command-me-timesheet-backfill", "SKILL.md"))
	for _, want := range []string{
		"name: source-command-me-timesheet-backfill",
		`description: "Backfill the timesheet"`,
		"# /me/timesheet/backfill",
		"Do the thing.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

func TestCommandWithoutDescriptionDerivesOne(t *testing.T) {
	cfg := newHome(t)
	write(t, filepath.Join(cfg.ClaudeHome, "commands", "tidy.md"), "# Tidy\n\nClean the working tree.\n")

	applied(t, cfg)

	got := read(t, filepath.Join(cfg.AgentsHome, "skills", "source-command-tidy", "SKILL.md"))
	if !strings.Contains(got, `description: "Clean the working tree."`) {
		t.Fatalf("description not derived from body:\n%s", got)
	}
}

func TestDisableModelInvocationBecomesCodexPolicy(t *testing.T) {
	cfg := newHome(t)
	write(t, filepath.Join(cfg.ClaudeHome, "skills", "eve", "SKILL.md"),
		"---\nname: eve\ndescription: Ask eve\ndisable-model-invocation: true\n---\nbody\n")
	write(t, filepath.Join(cfg.ClaudeHome, "commands", "handoff.md"),
		"---\ndisable-model-invocation: true\n---\nHand off.\n")

	applied(t, cfg)

	// The Claude opt-out has to survive the crossing, or a skill the user
	// deliberately silenced starts auto-firing in Codex instead.
	for _, slug := range []string{"eve", "source-command-handoff"} {
		got := read(t, filepath.Join(cfg.AgentsHome, "skills", slug, "agents", "openai.yaml"))
		if !strings.Contains(got, "allow_implicit_invocation: false") {
			t.Fatalf("%s: opt-out lost: %q", slug, got)
		}
	}
}

func TestModelInvocableSkillGetsNoPolicySidecar(t *testing.T) {
	cfg := newHome(t)
	write(t, filepath.Join(cfg.ClaudeHome, "skills", "sync", "SKILL.md"), "---\nname: sync\ndescription: Sync\n---\nbody\n")

	applied(t, cfg)

	if _, err := os.Stat(filepath.Join(cfg.AgentsHome, "skills", "sync", "agents", "openai.yaml")); !os.IsNotExist(err) {
		t.Fatal("wrote a policy sidecar for a skill that never opted out")
	}
}

func TestApplyIsIdempotentAndDeterministic(t *testing.T) {
	cfg := newHome(t)
	write(t, filepath.Join(cfg.ClaudeHome, "skills", "sync", "SKILL.md"), "---\nname: sync\ndescription: Sync\n---\nbody\n")
	write(t, filepath.Join(cfg.ClaudeHome, "commands", "tidy.md"), "---\ndescription: Tidy\n---\nBody.\n")

	first := applied(t, cfg)
	if len(first.Written) == 0 {
		t.Fatal("first run wrote nothing")
	}

	second := applied(t, cfg)
	if len(second.Written) != 0 || len(second.Removed) != 0 {
		t.Fatalf("second run was not a no-op: %+v", second)
	}

	plan, err := BuildPlan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := Check(cfg, plan); err != nil {
		t.Fatalf("check reported drift right after apply: %v", err)
	}
}

func TestCheckDetectsHandEditAndOrphan(t *testing.T) {
	cfg := newHome(t)
	write(t, filepath.Join(cfg.ClaudeHome, "skills", "sync", "SKILL.md"), "---\nname: sync\ndescription: Sync\n---\nbody\n")
	applied(t, cfg)

	write(t, filepath.Join(cfg.AgentsHome, "skills", "sync", "SKILL.md"), "tampered\n")
	plan, err := BuildPlan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := Check(cfg, plan); err == nil {
		t.Fatal("hand edit not reported as drift")
	}
}

func TestRetiredSkillIsPrunedAndBackedUp(t *testing.T) {
	cfg := newHome(t)
	source := filepath.Join(cfg.ClaudeHome, "skills", "doomed", "SKILL.md")
	write(t, source, "---\nname: doomed\ndescription: Doomed\n---\nbody\n")
	applied(t, cfg)

	if err := os.RemoveAll(filepath.Dir(source)); err != nil {
		t.Fatal(err)
	}
	report := applied(t, cfg)

	if _, err := os.Stat(filepath.Join(cfg.AgentsHome, "skills", "doomed")); !os.IsNotExist(err) {
		t.Fatal("retired skill survived the prune")
	}
	if report.Backup == "" {
		t.Fatal("pruned without taking a backup")
	}
	// Insurance must be real: the removed body has to be readable afterwards.
	var found bool
	_ = filepath.Walk(report.Backup, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() && strings.Contains(read(t, p), "name: doomed") {
			found = true
		}
		return nil
	})
	if !found {
		t.Fatal("backup does not contain the removed skill")
	}
}

func TestUnmanagedSkillIsNeverTouched(t *testing.T) {
	cfg := newHome(t)
	write(t, filepath.Join(cfg.ClaudeHome, "skills", "sync", "SKILL.md"), "---\nname: sync\ndescription: Sync\n---\nbody\n")
	handmade := filepath.Join(cfg.AgentsHome, "skills", "handmade", "SKILL.md")
	write(t, handmade, "---\nname: handmade\ndescription: Mine\n---\nkeep me\n")

	applied(t, cfg)
	applied(t, cfg)

	if read(t, handmade) != "---\nname: handmade\ndescription: Mine\n---\nkeep me\n" {
		t.Fatal("clobbered a skill codexsync never generated")
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	cfg := newHome(t)
	write(t, filepath.Join(cfg.ClaudeHome, "skills", "sync", "SKILL.md"), "---\nname: sync\ndescription: Sync\n---\nbody\n")

	plan, err := BuildPlan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Apply(cfg, plan, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Written) == 0 {
		t.Fatal("dry run reported no intended writes")
	}
	if _, err := os.Stat(filepath.Join(cfg.AgentsHome, "skills", "sync")); !os.IsNotExist(err) {
		t.Fatal("dry run touched the disk")
	}
}

func TestConfigBlockReplacedInPlace(t *testing.T) {
	cfg := newHome(t)
	config := filepath.Join(cfg.CodexHome, "config.toml")
	write(t, config, "model = \"gpt-5\"\n")
	write(t, filepath.Join(cfg.ClaudeHome, "skills", "sync", "SKILL.md"), "---\nname: sync\ndescription: Sync\n---\nbody\n")
	// The same skill is also discoverable on the Claude path; that duplicate
	// is exactly what the managed block exists to silence.
	applied(t, cfg)

	got := read(t, config)
	if !strings.Contains(got, `model = "gpt-5"`) {
		t.Fatal("hand-written config setting was lost")
	}
	if !strings.Contains(got, managedBegin) || !strings.Contains(got, managedEnd) {
		t.Fatalf("managed block missing:\n%s", got)
	}
	if !strings.Contains(got, filepath.Join(cfg.ClaudeHome, "skills", "sync", "SKILL.md")) {
		t.Fatalf("duplicate claude-side path not suppressed:\n%s", got)
	}

	// A second render must replace the block, never stack a second copy.
	applied(t, cfg)
	if n := strings.Count(read(t, config), managedBegin); n != 1 {
		t.Fatalf("managed block written %d times", n)
	}
}

func TestStalePromptsArePrunedAndBackedUp(t *testing.T) {
	cfg := newHome(t)
	prompts := filepath.Join(cfg.CodexHome, "prompts")
	// The legacy layout: a directory holding a symlink. Codex reads flat
	// <name>.md files here, so this has never been discoverable.
	write(t, filepath.Join(prompts, "claude-my-handoff", "handoff.md"), "stale\n")
	write(t, filepath.Join(prompts, "keeper.md"), "real prompt\n")

	report := applied(t, cfg)

	if _, err := os.Stat(filepath.Join(prompts, "claude-my-handoff")); !os.IsNotExist(err) {
		t.Fatal("undiscoverable prompt directory survived")
	}
	if read(t, filepath.Join(prompts, "keeper.md")) != "real prompt\n" {
		t.Fatal("pruned a prompt Codex can actually read")
	}
	if report.Backup == "" {
		t.Fatal("pruned prompts without a backup")
	}
}

func TestDanglingPromptSymlinkIsPruned(t *testing.T) {
	cfg := newHome(t)
	prompts := filepath.Join(cfg.CodexHome, "prompts")
	if err := os.MkdirAll(prompts, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(prompts, "gone.md")
	if err := os.Symlink(filepath.Join(cfg.ClaudeHome, "commands", "nope.md"), link); err != nil {
		t.Fatal(err)
	}

	applied(t, cfg)

	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatal("dangling prompt symlink survived")
	}
}

func TestDescriptionWithColonStaysValidFrontmatter(t *testing.T) {
	cfg := newHome(t)
	write(t, filepath.Join(cfg.ClaudeHome, "commands", "pr.md"), "# PR\n\nOpen a PR: create, review, merge.\n")

	applied(t, cfg)

	got := read(t, filepath.Join(cfg.AgentsHome, "skills", "source-command-pr", "SKILL.md"))
	if !strings.Contains(got, `description: "Open a PR: create, review, merge."`) {
		t.Fatalf("colon in description not quoted:\n%s", got)
	}
}

func TestSymlinkedSkillDirectoryIsMaterialized(t *testing.T) {
	cfg := newHome(t)
	// Skill homes are full of symlinked bundles. WalkDir does not follow them,
	// so a naive copy ships an empty directory — or dies trying to read one.
	external := filepath.Join(t.TempDir(), "shared")
	write(t, filepath.Join(external, "SKILL.md"), "---\nname: shared\ndescription: Shared\n---\nbody\n")
	write(t, filepath.Join(cfg.ClaudeHome, "skills", "bundle", "SKILL.md"), "---\nname: bundle\ndescription: Bundle\n---\nroot\n")
	if err := os.Symlink(external, filepath.Join(cfg.ClaudeHome, "skills", "bundle", "shared")); err != nil {
		t.Fatal(err)
	}

	applied(t, cfg)

	got := read(t, filepath.Join(cfg.AgentsHome, "skills", "bundle", "shared", "SKILL.md"))
	if !strings.Contains(got, "name: shared") {
		t.Fatalf("symlinked skill not materialized: %q", got)
	}
}

func TestSymlinkCycleTerminates(t *testing.T) {
	cfg := newHome(t)
	skill := filepath.Join(cfg.ClaudeHome, "skills", "loop")
	write(t, filepath.Join(skill, "SKILL.md"), "---\nname: loop\ndescription: Loop\n---\nbody\n")
	if err := os.Symlink(skill, filepath.Join(skill, "self")); err != nil {
		t.Fatal(err)
	}

	applied(t, cfg) // must return rather than recurse forever

	if !strings.Contains(read(t, filepath.Join(cfg.AgentsHome, "skills", "loop", "SKILL.md")), "name: loop") {
		t.Fatal("skill missing after cycle walk")
	}
}

package codexsync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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

func TestTopLevelSkillLinksAndRepeatedAliases(t *testing.T) {
	cfg := newHome(t)
	source := filepath.Join(t.TempDir(), "shared")
	write(t, filepath.Join(source, "SKILL.md"), "---\nname: shared\ndescription: Shared\n---\nbody\n")
	for _, name := range []string{"first", "second"} {
		if err := os.Symlink(source, filepath.Join(cfg.ClaudeHome, "skills", name)); err != nil {
			t.Fatal(err)
		}
	}
	report := applied(t, cfg)
	if len(report.Written) != 2 {
		t.Fatalf("linked skills skipped: %+v", report)
	}
	plan, err := BuildPlan(cfg)
	if err != nil || len(plan.Suppress) != 2 {
		t.Fatalf("aliases missing from suppressions: %+v, %v", plan, err)
	}
}

func TestExecutableModesSurviveAndDrift(t *testing.T) {
	cfg := newHome(t)
	write(t, filepath.Join(cfg.ClaudeHome, "skills", "runner", "SKILL.md"), "Run the script.\n")
	script := filepath.Join(cfg.ClaudeHome, "skills", "runner", "scripts", "run.sh")
	write(t, script, "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(script, 0o750); err != nil {
		t.Fatal(err)
	}
	applied(t, cfg)
	target := filepath.Join(cfg.AgentsHome, "skills", "runner", "scripts", "run.sh")
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("executable mode lost: %v, %v", info, err)
	}
	if err := os.Chmod(target, 0o640); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := Check(cfg, plan); err == nil {
		t.Fatal("mode-only drift passed")
	}
	if report := applied(t, cfg); len(report.Written) != 1 || report.Backup == "" {
		t.Fatalf("mode drift not repaired with backup: %+v", report)
	}
}

func TestInvocationPolicyMergesExistingMetadata(t *testing.T) {
	cfg := newHome(t)
	dir := filepath.Join(cfg.ClaudeHome, "skills", "manual")
	write(t, filepath.Join(dir, "SKILL.md"), "---\ndisable-model-invocation: true # explicit only\n---\nInstructions.\n")
	write(t, filepath.Join(dir, "agents", "openai.yaml"), "interface:\n  display_name: Manual\npolicy:\n  allow_implicit_invocation: true\ndependencies:\n  tools: []\n")
	applied(t, cfg)
	var sidecar struct {
		Interface struct {
			DisplayName string `yaml:"display_name"`
		}
		Policy struct {
			Allow bool `yaml:"allow_implicit_invocation"`
		}
		Dependencies yaml.Node
	}
	got := read(t, filepath.Join(cfg.AgentsHome, "skills", "manual", "agents", "openai.yaml"))
	if err := yaml.Unmarshal([]byte(got), &sidecar); err != nil {
		t.Fatal(err)
	}
	if sidecar.Policy.Allow || sidecar.Interface.DisplayName != "Manual" || sidecar.Dependencies.Kind != yaml.MappingNode {
		t.Fatalf("policy or metadata lost: %s", got)
	}
}

func TestCommandFrontmatterUsesYAMLAndConsumesDelimiter(t *testing.T) {
	for _, description := range []string{"description: >\n  Open a PR:\n  review it", "description: 'Open a PR: review it' # comment"} {
		t.Run(description, func(t *testing.T) {
			cfg := newHome(t)
			body := "---\n" + description + "\ndisable-model-invocation: true\n---\nCommand body.\n"
			write(t, filepath.Join(cfg.ClaudeHome, "commands", "pr.md"), strings.ReplaceAll(body, "\n", "\r\n"))
			applied(t, cfg)
			got := read(t, filepath.Join(cfg.AgentsHome, "skills", "source-command-pr", "SKILL.md"))
			if !strings.Contains(got, `description: "Open a PR: review it"`) || !strings.HasSuffix(got, "---\n\nCommand body.\n") {
				t.Fatalf("frontmatter parsed incorrectly: %s", got)
			}
		})
	}
}

func TestDestinationCollisionsFailBeforeApply(t *testing.T) {
	for _, skillCollision := range []bool{false, true} {
		cfg := newHome(t)
		write(t, filepath.Join(cfg.ClaudeHome, "commands", "a", "b.md"), "Nested command.\n")
		if skillCollision {
			write(t, filepath.Join(cfg.ClaudeHome, "skills", "source-command-a-b", "SKILL.md"), "Existing skill.\n")
		} else {
			write(t, filepath.Join(cfg.ClaudeHome, "commands", "a-b.md"), "Flat command.\n")
		}
		if _, err := BuildPlan(cfg); err == nil || !strings.Contains(err.Error(), "duplicate destination") {
			t.Fatalf("collision accepted: %v", err)
		}
	}
}

func TestUnmanagedCollisionIsPreserved(t *testing.T) {
	cfg := newHome(t)
	write(t, filepath.Join(cfg.ClaudeHome, "skills", "sync", "SKILL.md"), "Generated.\n")
	target := filepath.Join(cfg.AgentsHome, "skills", "sync", "SKILL.md")
	write(t, target, "Handwritten.\n")
	plan, err := BuildPlan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(cfg, plan, false); err == nil || !strings.Contains(err.Error(), "unmanaged") {
		t.Fatalf("unmanaged collision not refused: %v", err)
	}
	if read(t, target) != "Handwritten.\n" {
		t.Fatal("handwritten skill overwritten")
	}
}

func TestRetirementPreservesUnmanagedFilesInsideEntry(t *testing.T) {
	cfg := newHome(t)
	source := filepath.Join(cfg.ClaudeHome, "skills", "retired", "SKILL.md")
	write(t, source, "Generated.\n")
	applied(t, cfg)
	manual := filepath.Join(cfg.AgentsHome, "skills", "retired", "notes.md")
	write(t, manual, "My notes.\n")
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	applied(t, cfg)
	if read(t, manual) != "My notes.\n" {
		t.Fatal("retirement removed unmanaged contents")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(manual), "SKILL.md")); !os.IsNotExist(err) {
		t.Fatal("retired managed file survived")
	}
}

func TestOverwrittenFilesHaveDistinctRecoverableBackups(t *testing.T) {
	cfg := newHome(t)
	source := filepath.Join(cfg.ClaudeHome, "skills", "sync", "SKILL.md")
	write(t, source, "Original.\n")
	applied(t, cfg)
	write(t, source, "Second.\n")
	first := applied(t, cfg)
	write(t, source, "Third.\n")
	second := applied(t, cfg)
	if first.Backup == "" || first.Backup == second.Backup {
		t.Fatalf("backups reused: %q %q", first.Backup, second.Backup)
	}
	for dir, want := range map[string]string{first.Backup: "Original.\n", second.Backup: "Second.\n"} {
		if got := read(t, filepath.Join(dir, ".agents", "skills", "sync", "SKILL.md")); got != want {
			t.Fatalf("backup contains %q, want %q", got, want)
		}
	}
}

func TestManagedStateDriftAndDryRun(t *testing.T) {
	for _, which := range []string{"config", "manifest", "prompts"} {
		t.Run(which, func(t *testing.T) {
			cfg := newHome(t)
			applied(t, cfg)
			switch which {
			case "config":
				write(t, filepath.Join(cfg.CodexHome, "config.toml"), "model = \"custom\"\n")
			case "manifest":
				if err := os.Remove(filepath.Join(cfg.AgentsHome, "skills", ManifestName)); err != nil {
					t.Fatal(err)
				}
			case "prompts":
				write(t, filepath.Join(cfg.CodexHome, "prompts", "nested", "old.md"), "Legacy.\n")
			}
			plan, err := BuildPlan(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := Check(cfg, plan); err == nil {
				t.Fatal("managed state drift passed")
			}
			dry, err := Apply(cfg, plan, true)
			if err != nil {
				t.Fatal(err)
			}
			actual := applied(t, cfg)
			if dry.Config != actual.Config || dry.Manifest != actual.Manifest || len(dry.Removed) != len(actual.Removed) {
				t.Fatalf("dry run differs: %+v, %+v", dry, actual)
			}
		})
	}
}

func TestMalformedStateFailsWithoutMutating(t *testing.T) {
	for _, which := range []string{"manifest", "config"} {
		t.Run(which, func(t *testing.T) {
			cfg := newHome(t)
			source := filepath.Join(cfg.ClaudeHome, "skills", "sync", "SKILL.md")
			write(t, source, "Original.\n")
			applied(t, cfg)
			write(t, source, "Changed.\n")
			if which == "manifest" {
				write(t, filepath.Join(cfg.AgentsHome, "skills", ManifestName), "{invalid")
			} else {
				write(t, filepath.Join(cfg.CodexHome, "config.toml"), managedBegin+"\nmodel = \"keep\"\n")
			}
			plan, err := BuildPlan(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Apply(cfg, plan, false); err == nil {
				t.Fatal("malformed managed state accepted")
			}
			if got := read(t, filepath.Join(cfg.AgentsHome, "skills", "sync", "SKILL.md")); got != "Original.\n" {
				t.Fatal("files changed before state was validated")
			}
		})
	}
}

func TestManifestPathsAreValidated(t *testing.T) {
	for _, rel := range []string{".", "../other", "/absolute", "a/../b", "a\\b"} {
		if validRelative(rel) {
			t.Fatalf("accepted invalid relative path %q", rel)
		}
	}
	cfg := newHome(t)
	body, err := json.Marshal(manifest{Version: 2})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(cfg.AgentsHome, "skills", ManifestName), string(body))
	if _, err := readManifest(cfg); err == nil {
		t.Fatal("unsupported manifest version accepted")
	}
}

func TestDifferentCodexVariantIsNotSuppressed(t *testing.T) {
	cfg := newHome(t)
	write(t, filepath.Join(cfg.ClaudeHome, "skills", "sync", "SKILL.md"), "Claude variant.\n")
	write(t, filepath.Join(cfg.CodexHome, "skills", "sync", "SKILL.md"), "Codex variant.\n")
	plan, err := BuildPlan(cfg)
	if err != nil || len(plan.Suppress) != 1 || !strings.HasPrefix(plan.Suppress[0], cfg.ClaudeHome) {
		t.Fatalf("different variant suppressed: %+v, %v", plan, err)
	}
}

func TestBackupFailureLeavesOriginalsUntouched(t *testing.T) {
	cfg := newHome(t)
	source := filepath.Join(cfg.ClaudeHome, "skills", "sync", "SKILL.md")
	write(t, source, "Original.\n")
	applied(t, cfg)
	write(t, source, "Changed.\n")
	write(t, cfg.BackupRoot, "A file blocks the backup directory.\n")
	plan, err := BuildPlan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(cfg, plan, false); err == nil {
		t.Fatal("apply succeeded without its backup")
	}
	if got := read(t, filepath.Join(cfg.AgentsHome, "skills", "sync", "SKILL.md")); got != "Original.\n" {
		t.Fatal("original changed despite failed backup")
	}
}

func TestDestinationSymlinkIsRefused(t *testing.T) {
	cfg := newHome(t)
	source := filepath.Join(cfg.ClaudeHome, "skills", "sync", "SKILL.md")
	write(t, source, "Source.\n")
	root := filepath.Join(cfg.AgentsHome, "skills")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(source), filepath.Join(root, "sync")); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(cfg, plan, false); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("destination link accepted: %v", err)
	}
}

func TestHomesCannotOverlapOrDisappear(t *testing.T) {
	cfg := newHome(t)
	cfg.AgentsHome = cfg.ClaudeHome
	if _, err := BuildPlan(cfg); err == nil {
		t.Fatal("overlapping homes accepted")
	}
	cfg = newHome(t)
	cfg.ClaudeHome = filepath.Join(cfg.ClaudeHome, "missing")
	if _, err := BuildPlan(cfg); err == nil {
		t.Fatal("missing source treated as an empty render")
	}
}

func TestPromptBackupPreservesLinksWithoutNameCollisions(t *testing.T) {
	cfg := newHome(t)
	dir := filepath.Join(cfg.CodexHome, "prompts", "legacy")
	write(t, filepath.Join(dir, "note.symlink"), "Keep the literal file.\n")
	if err := os.Symlink("absent.md", filepath.Join(dir, "note")); err != nil {
		t.Fatal(err)
	}
	report := applied(t, cfg)
	dest := filepath.Join(report.Backup, ".codex", "prompts", "legacy")
	if got, err := os.Readlink(filepath.Join(dest, "note")); err != nil || got != "absent.md" {
		t.Fatalf("link not preserved: %q, %v", got, err)
	}
	if got := read(t, filepath.Join(dest, "note.symlink")); got != "Keep the literal file.\n" {
		t.Fatal("symlink backup overwrote its neighbor")
	}
}

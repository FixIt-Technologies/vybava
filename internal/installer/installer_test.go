package installer_test

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/FixIt-Technologies/vybava/internal/catalog"
	"github.com/FixIt-Technologies/vybava/internal/installer"
	"github.com/FixIt-Technologies/vybava/internal/state"
)

func TestPlanExpandsAllAgentTargets(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	i := installer.Installer{}
	operations, err := i.Plan([]catalog.Item{{ID: "prm", Kind: catalog.KindSkill}}, installer.Options{
		Agent: installer.AgentAll, Scope: installer.ScopeProject, RootDir: root,
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if got, want := len(operations), 2; got != want {
		t.Fatalf("Plan() operations = %d, want %d", got, want)
	}
}

func TestApplyInstallsSkillAndRefusesUnmanagedReplacement(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := state.Store{Path: filepath.Join(root, "state.json")}
	i := installer.Installer{
		Payload: fstest.MapFS{
			"skills/demo/SKILL.md": &fstest.MapFile{Data: []byte("---\nname: demo\ndescription: Demo.\n---\n")},
		},
		Store: store,
	}
	operations, err := i.Plan([]catalog.Item{{ID: "demo", Kind: catalog.KindSkill}}, installer.Options{
		Agent: installer.AgentCodex, Scope: installer.ScopeProject, RootDir: root,
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if err := i.Apply(operations, false); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".codex", "skills", "demo", "SKILL.md")); err != nil {
		t.Fatalf("installed skill: %v", err)
	}

	unmanaged := filepath.Join(root, ".codex", "skills", "other")
	if err := os.MkdirAll(unmanaged, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := []installer.Operation{{ItemID: "other", Kind: string(catalog.KindSkill), Agent: "codex", Scope: "project", Destination: unmanaged}}
	if err := i.Apply(bad, false); err == nil {
		t.Fatal("Apply() replaced an unmanaged skill")
	}
}

package reconcile

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func mustT(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func contains1(s, sub string) bool { return strings.Contains(s, sub) }

func tempRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	mustT(t, err)
	t.Setenv("HOME", filepath.Join(root, "home"))
	mustT(t, os.MkdirAll(filepath.Join(root, "home"), 0o755))
	return root
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// seedRepo builds origin.git + seed + repo under root with the given files
// committed on main; returns the seed clone for further commits.
func seedRepo(t *testing.T, root string, files map[string]string) string {
	t.Helper()
	origin := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	repo := filepath.Join(root, "repo")
	gitT(t, root, "init", "-q", "--bare", "--initial-branch=main", origin)
	gitT(t, root, "clone", "-q", origin, seed)
	gitT(t, seed, "config", "user.email", "t@t")
	gitT(t, seed, "config", "user.name", "t")
	commitFiles(t, seed, "seed", files)
	gitT(t, root, "clone", "-q", origin, repo)
	return seed
}

func commitFiles(t *testing.T, seed, msg string, files map[string]string) string {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(seed, p)
		mustT(t, os.MkdirAll(filepath.Dir(full), 0o755))
		mustT(t, os.WriteFile(full, []byte(c), 0o644))
	}
	gitT(t, seed, "add", "-A")
	gitT(t, seed, "commit", "-qm", msg)
	gitT(t, seed, "push", "-q", "origin", "main")
	return gitT(t, seed, "rev-parse", "HEAD")
}

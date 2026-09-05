package reconcile

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

type gitRepo struct{ dir string }

func (g gitRepo) run(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", g.dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

func (g gitRepo) revParse(ref string) (string, error) { return g.run("rev-parse", ref) }

func (g gitRepo) fetchMain() error {
	_, err := g.run("fetch", "-q", "origin", "main")
	return err
}

func (g gitRepo) resetHard(ref string) error {
	_, err := g.run("reset", "--hard", "-q", ref)
	return err
}

func (g gitRepo) lsFiles() ([]string, error) {
	out, err := g.run("ls-files")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

func (g gitRepo) tracks(rp string) bool {
	_, err := g.run("ls-files", "--error-unmatch", "--", rp)
	return err == nil
}

// subject returns "<short> <subject>" for a commit, "" when unknown.
func (g gitRepo) subject(sha string) string {
	out, err := g.run("log", "-1", "--format=%h %s", sha)
	if err != nil {
		return ""
	}
	return out
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

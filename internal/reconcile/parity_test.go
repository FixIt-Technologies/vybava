//go:build !windows

package reconcile_test

// The bash engine's executable contract (webulinka-infra
// scripts/infra-reconcile/test-reconcile.sh, cases 0–21 incl. 13b) mirrored
// 1:1. Every path (repo, live root, state, mode) is a fixture; the nginx hooks
// are fake binaries switched by marker files, exactly as the bash suite does.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/FixIt-Technologies/vybava/internal/cli"
)

type fixture struct {
	t    *testing.T
	T    string // canonical temp root
	seed string
	repo string
	man  string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, T: root, seed: filepath.Join(root, "seed"), repo: filepath.Join(root, "repo")}
	t.Setenv("HOME", filepath.Join(root, "home")) // keeps alert lookups inert
	must(t, os.MkdirAll(filepath.Join(root, "home"), 0o755))

	f.git("", "init", "-q", "--bare", "--initial-branch=main", filepath.Join(root, "origin.git"))
	f.git("", "clone", "-q", filepath.Join(root, "origin.git"), f.seed)
	f.git(f.seed, "config", "user.email", "t@t")
	f.git(f.seed, "config", "user.name", "t")
	must(t, os.MkdirAll(filepath.Join(f.seed, "scripts"), 0o755))
	must(t, os.MkdirAll(filepath.Join(f.seed, "nginx"), 0o755))
	must(t, os.WriteFile(filepath.Join(f.seed, "scripts/one.sh"), []byte("#!/bin/sh\necho one\n"), 0o755))
	must(t, os.WriteFile(filepath.Join(f.seed, "nginx/site.conf"), []byte("server { listen 80; }\n"), 0o644))
	f.git(f.seed, "add", "-A")
	f.git(f.seed, "commit", "-qm", "seed")
	f.git(f.seed, "branch", "-qM", "main")
	f.git(f.seed, "push", "-q", "origin", "main")
	f.git("", "clone", "-q", filepath.Join(root, "origin.git"), f.repo)

	bin := filepath.Join(root, "bin")
	must(t, os.MkdirAll(bin, 0o755))
	must(t, os.WriteFile(filepath.Join(bin, "nginx-test"), []byte("#!/bin/sh\n[ ! -f "+root+"/nginx-test-fails ]\n"), 0o755))
	must(t, os.WriteFile(filepath.Join(bin, "nginx-reload"), []byte("#!/bin/sh\n[ ! -f "+root+"/nginx-reload-fails ] || exit 1\ndate +%s%N >> "+root+"/nginx-reloads\n"), 0o755))
	must(t, os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\necho \"$PWD $*\" >> "+root+"/docker-calls\n"), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	f.man = filepath.Join(root, "reconcile.yaml")
	must(t, os.WriteFile(f.man, []byte(fmt.Sprintf(`schema_version: 1
repo: fixture
host_label: fixture
clone: %[1]s/repo
state_dir: %[1]s/state
mode_file: %[1]s/mode
lock_file: %[1]s/lock
apps_root: %[1]s/opt/apps
metrics_file: %[1]s/metrics/infra-reconcile.prom
skip: ["*.md"]
mappings:
  - match: ["scripts/*"]
    strip: scripts/
    dest: %[1]s/opt/scripts/{rest}
  - match: ["nginx/*.conf"]
    strip: nginx/
    dest: %[1]s/opt/conf.d/{rest}
    hook: nginx
  - match: ["apps/*/*"]
    strip: apps/
    dest: %[1]s/opt/apps/{rest}
    hook: compose
hooks:
  nginx:
    test: [nginx-test]
    reload: [nginx-reload]
`, root)), 0o644))
	return f
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) git(dir string, args ...string) {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		f.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// commit writes files into the seed clone and pushes main.
func (f *fixture) commit(msg string, files map[string]string) {
	f.t.Helper()
	for p, content := range files {
		full := filepath.Join(f.seed, p)
		must(f.t, os.MkdirAll(filepath.Dir(full), 0o755))
		must(f.t, os.WriteFile(full, []byte(content), 0o644))
	}
	f.git(f.seed, "add", "-A")
	f.git(f.seed, "commit", "-qm", msg)
	f.git(f.seed, "push", "-q", "origin", "main")
}

// run drives the real CLI (`vybava reconcile …`) against the fixture.
func (f *fixture) run(args ...string) (string, error) {
	f.t.Helper()
	var out bytes.Buffer
	command, err := (cli.App{Stdout: &out, Stderr: &out}).Command("reconcile")
	if err != nil {
		f.t.Fatal(err)
	}
	command.SetArgs(append([]string{"--manifest", f.man}, args...))
	err = command.Execute()
	return out.String(), err
}

func (f *fixture) mode(m string) {
	must(f.t, os.WriteFile(filepath.Join(f.T, "mode"), []byte(m+"\n"), 0o644))
}
func (f *fixture) touch(name string) {
	must(f.t, os.WriteFile(filepath.Join(f.T, name), nil, 0o644))
}
func (f *fixture) rm(name string) { _ = os.Remove(filepath.Join(f.T, name)) }
func (f *fixture) live(rel string) string {
	b, _ := os.ReadFile(filepath.Join(f.T, "opt", rel))
	return string(b)
}
func (f *fixture) writeLive(rel, content string) {
	full := filepath.Join(f.T, "opt", rel)
	must(f.t, os.MkdirAll(filepath.Dir(full), 0o755))
	must(f.t, os.WriteFile(full, []byte(content), 0o644))
}
func (f *fixture) reloads() int {
	b, _ := os.ReadFile(filepath.Join(f.T, "nginx-reloads"))
	return strings.Count(string(b), "\n")
}
func (f *fixture) state(name string) string {
	b, _ := os.ReadFile(filepath.Join(f.T, "state", name))
	return string(b)
}
func (f *fixture) applied(rp string) string {
	last := ""
	for _, l := range strings.Split(f.state("applied.tsv"), "\n") {
		p, sha, _ := strings.Cut(l, "\t")
		if p == rp {
			last = sha
		}
	}
	return last
}
func (f *fixture) exists(rel string) bool {
	_, err := os.Lstat(filepath.Join(f.T, rel))
	return err == nil
}
func (f *fixture) pendingHooksHas(h string) bool {
	for _, l := range strings.Split(f.state("pending-hooks"), "\n") {
		if l == h {
			return true
		}
	}
	return false
}
func shaOf(path string) string {
	b, _ := os.ReadFile(path)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// step runs one contract case; the suite is sequential and stateful like the
// bash one, so a failed case stops the rest instead of cascading noise.
func (f *fixture) step(name string, fn func(t *testing.T)) {
	if f.t.Failed() {
		return
	}
	f.t.Run(name, fn)
}

func TestBashParity(t *testing.T) {
	f := newFixture(t)

	f.step("00_status_creates_no_state", func(t *testing.T) {
		f.mode("report")
		if _, err := f.run("status"); err != nil {
			t.Fatal(err)
		}
		if f.exists("state") {
			t.Fatal("status created the state directory")
		}
	})

	f.step("01_report_mode_writes_nothing", func(t *testing.T) {
		f.mode("report")
		out, _ := f.run("run")
		if !strings.Contains(out, "pending converge") {
			t.Fatalf("report mode did not list pending files:\n%s", out)
		}
		if f.exists("opt/scripts/one.sh") {
			t.Fatal("report mode wrote a live file")
		}
		if !f.exists("metrics/infra-reconcile.prom") {
			t.Fatal("run did not write the textfile metrics")
		}
	})

	f.step("02_converge_lands_files_exec_bit_hook_once", func(t *testing.T) {
		f.mode("converge")
		if _, err := f.run("run"); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(filepath.Join(f.T, "opt/scripts/one.sh"))
		if err != nil || fi.Mode()&0o111 == 0 {
			t.Fatal("converge did not install scripts/one.sh with exec bit")
		}
		if !f.exists("opt/conf.d/site.conf") {
			t.Fatal("converge did not install the nginx conf")
		}
		if n := f.reloads(); n != 1 {
			t.Fatalf("nginx hook did not run exactly once (got %d)", n)
		}
		if !strings.Contains(f.state("applied.tsv"), "scripts/one.sh") {
			t.Fatal("applied.tsv missing scripts/one.sh")
		}
		if strings.TrimSpace(f.state("last-good")) == "" {
			t.Fatal("full converge did not record last-good")
		}
	})

	f.step("03_steady_state_no_reloads", func(t *testing.T) {
		if _, err := f.run("run"); err != nil {
			t.Fatal(err)
		}
		if n := f.reloads(); n != 1 {
			t.Fatalf("steady-state tick re-ran the nginx hook (%d)", n)
		}
	})

	f.step("04_held_never_overwritten", func(t *testing.T) {
		f.writeLive("conf.d/site.conf", "hand-hotfix\n")
		f.commit("edit", map[string]string{"nginx/site.conf": "server { listen 81; }\n"})
		out, _ := f.run("run")
		if !strings.Contains(out, "HELD") {
			t.Fatalf("hand-edited file was not HELD:\n%s", out)
		}
		if !strings.Contains(f.live("conf.d/site.conf"), "hand-hotfix") {
			t.Fatal("HELD file was overwritten")
		}
	})

	f.step("05_force_rejects_traversal_and_untracked", func(t *testing.T) {
		for _, p := range []string{"scripts/../nginx/site.conf", "/etc/passwd", "scripts/ghost.sh"} {
			if _, err := f.run("force", p); err == nil {
				t.Fatalf("force accepted %q", p)
			}
		}
	})

	f.step("06_force_failing_nginx_test_restores", func(t *testing.T) {
		f.touch("nginx-test-fails")
		if _, err := f.run("force", "nginx/site.conf"); err == nil {
			t.Fatal("force succeeded despite failing nginx test")
		}
		if !strings.Contains(f.live("conf.d/site.conf"), "hand-hotfix") {
			t.Fatal("failed force did not restore the live file")
		}
		if f.applied("nginx/site.conf") == shaOf(filepath.Join(f.repo, "nginx/site.conf")) {
			t.Fatal("failed force recorded the repo sha as applied")
		}
	})

	f.step("07_force_applies_reloads_backs_up", func(t *testing.T) {
		f.rm("nginx-test-fails")
		if _, err := f.run("force", "nginx/site.conf"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(f.live("conf.d/site.conf"), "listen 81") {
			t.Fatal("force did not apply the repo version")
		}
		if n := f.reloads(); n != 2 {
			t.Fatalf("force did not reload nginx (got %d)", n)
		}
		found := false
		entries, _ := os.ReadDir(filepath.Join(f.T, "state/backups"))
		for _, e := range entries {
			b, _ := os.ReadFile(filepath.Join(f.T, "state/backups", e.Name()))
			if strings.Contains(string(b), "hand-hotfix") {
				found = true
			}
		}
		if !found {
			t.Fatal("force did not back up the hotfixed live file")
		}
	})

	f.step("08_force_backups_never_collide", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			if _, err := f.run("force", "nginx/site.conf"); err != nil {
				t.Fatal(err)
			}
		}
		entries, _ := os.ReadDir(filepath.Join(f.T, "state/backups"))
		if len(entries) < 3 {
			t.Fatalf("same-second force backups collided (%d retained)", len(entries))
		}
	})

	f.step("09_converge_nginx_test_failure_rolls_back_then_retries", func(t *testing.T) {
		f.commit("edit2", map[string]string{"nginx/site.conf": "server { listen 82; }\n"})
		prev := f.live("conf.d/site.conf")
		f.touch("nginx-test-fails")
		_, _ = f.run("run")
		if f.live("conf.d/site.conf") != prev {
			t.Fatal("failed -t did not roll the conf back")
		}
		if n := f.reloads(); n != 4 {
			t.Fatalf("nginx reloaded despite failing test (got %d)", n)
		}
		f.rm("nginx-test-fails")
		if _, err := f.run("run"); err != nil { // drift is still pending — the converge retries now
			t.Fatal(err)
		}
		if !strings.Contains(f.live("conf.d/site.conf"), "listen 82") {
			t.Fatal("converge did not retry after rollback")
		}
		if n := f.reloads(); n != 5 {
			t.Fatalf("retried converge did not reload nginx (got %d)", n)
		}
		if strings.TrimSpace(f.state("pending-hooks")) != "" {
			t.Fatal("pending-hooks left populated after success")
		}
	})

	f.step("10_status_is_read_only", func(t *testing.T) {
		f.commit("edit3", map[string]string{"nginx/site.conf": "server { listen 83; }\n"})
		headBefore, _ := exec.Command("git", "-C", f.repo, "rev-parse", "HEAD").Output()
		stateBefore := shaOf(filepath.Join(f.T, "state/applied.tsv"))
		if _, err := f.run("status"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.run("status", "--json"); err != nil {
			t.Fatal(err)
		}
		headAfter, _ := exec.Command("git", "-C", f.repo, "rev-parse", "HEAD").Output()
		if string(headBefore) != string(headAfter) {
			t.Fatal("status moved the checkout")
		}
		if shaOf(filepath.Join(f.T, "state/applied.tsv")) != stateBefore {
			t.Fatal("status mutated applied.tsv")
		}
		matches, _ := filepath.Glob(filepath.Join(f.T, "state/last-alert.*"))
		if len(matches) > 0 {
			t.Fatal("status wrote alert state")
		}
	})

	f.step("11_converge_rollback_restores_previous_live_state", func(t *testing.T) {
		f.commit("bad", map[string]string{"nginx/site.conf": "server { broken\n"})
		prev := f.live("conf.d/site.conf")
		f.touch("nginx-test-fails")
		out, _ := f.run("run")
		if !strings.Contains(out, "rolled back") {
			t.Fatalf("failed converge did not report a rollback:\n%s", out)
		}
		if f.live("conf.d/site.conf") != prev {
			t.Fatal("bad nginx conf left live after failed -t")
		}
		if f.applied("nginx/site.conf") != shaOf(filepath.Join(f.T, "opt/conf.d/site.conf")) {
			t.Fatal("rollback did not re-point applied.tsv at the restored content")
		}
		before := f.reloads()
		f.rm("nginx-test-fails")
		f.commit("fixed", map[string]string{"nginx/site.conf": "server { listen 84; }\n"})
		if _, err := f.run("run"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(f.live("conf.d/site.conf"), "listen 84") {
			t.Fatal("fixed conf did not converge after rollback")
		}
		if f.reloads() != before+1 {
			t.Fatal("fixed conf did not reload nginx after rollback")
		}
	})

	f.step("12_force_refuses_absent_app_dir", func(t *testing.T) {
		f.commit("app", map[string]string{"apps/ghost/docker-compose.yml": "services: {}\n"})
		f.git(f.repo, "pull", "-q")
		if _, err := f.run("force", "apps/ghost/docker-compose.yml"); err == nil {
			t.Fatal("force materialized an absent app dir")
		}
		if f.exists("opt/apps/ghost") {
			t.Fatal("force created the ghost app dir")
		}
	})

	f.step("13_force_reload_failure_queues_retry", func(t *testing.T) {
		f.commit("edit85", map[string]string{"nginx/site.conf": "server { listen 85; }\n"})
		f.writeLive("conf.d/site.conf", "hotfix-85\n")
		_, _ = f.run("run") // HELD
		f.touch("nginx-reload-fails")
		if _, err := f.run("force", "nginx/site.conf"); err == nil {
			t.Fatal("force succeeded despite failing reload")
		}
		if !strings.Contains(f.live("conf.d/site.conf"), "listen 85") {
			t.Fatal("valid conf was not left installed on reload failure")
		}
		if !f.pendingHooksHas("nginx") {
			t.Fatal("failed reload was not queued for retry")
		}
		f.rm("nginx-reload-fails")
		f.touch("nginx-test-fails") // retry tick whose -t fails must keep the queue
		_, _ = f.run("run")
		if !f.pendingHooksHas("nginx") {
			t.Fatal("failing retry test dropped the queued reload")
		}
		f.rm("nginx-test-fails")
		before := f.reloads()
		if _, err := f.run("run"); err != nil {
			t.Fatal(err)
		}
		if f.reloads() != before+1 {
			t.Fatal("queued reload did not retry on the next tick")
		}
		if strings.TrimSpace(f.state("pending-hooks")) != "" {
			t.Fatal("pending-hooks left populated after retried reload")
		}
	})

	f.step("13b_preloaded_reload_queue_survives_rollback", func(t *testing.T) {
		f.commit("edit86", map[string]string{"nginx/site.conf": "server { listen 86; }\n"})
		f.writeLive("conf.d/site.conf", "hotfix-86\n")
		_, _ = f.run("run") // HELD
		f.touch("nginx-reload-fails")
		_, _ = f.run("force", "nginx/site.conf") // queues the reload
		f.rm("nginx-reload-fails")
		if !f.pendingHooksHas("nginx") {
			t.Fatal("fixture failed to queue a reload")
		}
		f.commit("bad86", map[string]string{"nginx/site.conf": "server { broken86\n"})
		f.touch("nginx-test-fails")
		_, _ = f.run("run") // rollback tick — must NOT drop the queue
		if !f.pendingHooksHas("nginx") {
			t.Fatal("rollback tick dropped the preloaded reload queue")
		}
		f.rm("nginx-test-fails")
		f.commit("fix87", map[string]string{"nginx/site.conf": "server { listen 87; }\n"})
		if _, err := f.run("run"); err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(f.state("pending-hooks")) != "" {
			t.Fatal("queue not drained after healthy tick")
		}
		if !strings.Contains(f.live("conf.d/site.conf"), "listen 87") {
			t.Fatal("healthy tick did not converge after queue drain")
		}
	})

	f.step("14_applied_state_rewrites_are_field_anchored", func(t *testing.T) {
		fh, err := os.OpenFile(filepath.Join(f.T, "state/applied.tsv"), os.O_APPEND|os.O_WRONLY, 0o644)
		must(t, err)
		_, _ = fh.WriteString("apps/a/scripts/one.sh\tdeadbeef\n")
		fh.Close()
		f.commit("bump", map[string]string{"scripts/one.sh": "#!/bin/sh\necho one\necho two\n"})
		if _, err := f.run("run"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(f.state("applied.tsv"), "apps/a/scripts/one.sh") {
			t.Fatal("recording scripts/one.sh removed the sibling apps/a/scripts/one.sh record")
		}
	})

	f.step("15_force_waits_on_shared_lock", func(t *testing.T) {
		lockFile, err := os.OpenFile(filepath.Join(f.T, "lock"), os.O_CREATE|os.O_RDWR, 0o644)
		must(t, err)
		must(t, syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX))
		released := make(chan struct{})
		go func() {
			time.Sleep(3 * time.Second)
			_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
			lockFile.Close()
			close(released)
		}()
		time.Sleep(300 * time.Millisecond)
		start := time.Now()
		_, _ = f.run("force", "nginx/site.conf")
		if time.Since(start) < 2*time.Second {
			t.Fatal("force did not wait for the reconcile lock")
		}
		<-released
	})

	f.step("16_unknown_commands_rejected", func(t *testing.T) {
		stateBefore := shaOf(filepath.Join(f.T, "state/applied.tsv"))
		if _, err := f.run("frobnicate"); err == nil {
			t.Fatal("unknown command was accepted")
		}
		if shaOf(filepath.Join(f.T, "state/applied.tsv")) != stateBefore {
			t.Fatal("unknown command touched state")
		}
	})

	f.step("17_committed_symlinks_never_dereferenced", func(t *testing.T) {
		must(t, os.Symlink("/etc/hostname", filepath.Join(f.seed, "scripts/evil.sh")))
		f.git(f.seed, "add", "-A")
		f.git(f.seed, "commit", "-qm", "sym")
		f.git(f.seed, "push", "-q", "origin", "main")
		out, _ := f.run("run")
		if !strings.Contains(out, "symlink") {
			t.Fatalf("symlinked source was not reported:\n%s", out)
		}
		if f.exists("opt/scripts/evil.sh") {
			t.Fatal("symlinked source was installed")
		}
		if _, err := f.run("force", "scripts/evil.sh"); err == nil {
			t.Fatal("force accepted a symlinked source")
		}
	})

	f.step("18_live_destination_symlinks_never_redirect", func(t *testing.T) {
		must(t, os.MkdirAll(filepath.Join(f.T, "opt/apps/legit"), 0o755))
		must(t, os.MkdirAll(filepath.Join(f.T, "outside"), 0o755))
		must(t, os.Symlink(filepath.Join(f.T, "outside"), filepath.Join(f.T, "opt/apps/legit/conf")))
		f.commit("legit", map[string]string{"apps/legit/conf/app.yml": "redirected\n"})
		out, _ := f.run("run")
		if !strings.Contains(out, "escapes") && !strings.Contains(out, "resolves elsewhere") {
			t.Fatalf("symlinked live app subdir was not refused:\n%s", out)
		}
		if f.exists("outside/app.yml") {
			t.Fatal("sweep wrote through a live symlink outside the app dir")
		}
	})

	// case 19 (the real path maps hold their contract) lives in manifests_test.go

	f.step("20_symlinked_scripts_destination_component_refused", func(t *testing.T) {
		must(t, os.MkdirAll(filepath.Join(f.T, "outside2"), 0o755))
		must(t, os.Symlink(filepath.Join(f.T, "outside2"), filepath.Join(f.T, "opt/scripts/sub")))
		f.commit("deep", map[string]string{"scripts/sub/deep.sh": "echo deep\n"})
		out, _ := f.run("run")
		if !strings.Contains(out, "resolves elsewhere") {
			t.Fatalf("symlinked scripts component was not refused:\n%s", out)
		}
		if f.exists("outside2/deep.sh") {
			t.Fatal("sweep wrote through a symlinked scripts dir")
		}
		must(t, os.Remove(filepath.Join(f.T, "opt/scripts/sub")))
		f.git(f.seed, "rm", "-q", "-r", "scripts/sub")
		f.git(f.seed, "commit", "-qm", "rmdeep")
		f.git(f.seed, "push", "-q", "origin", "main")
		_, _ = f.run("run")
	})

	f.step("21_failed_nginx_copy_rolls_transaction_back", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		f.commit("two", map[string]string{
			"nginx/site.conf":   "server { listen 90; }\n",
			"nginx/second.conf": "server { listen 91; }\n",
		})
		prev := f.live("conf.d/site.conf")
		confd := filepath.Join(f.T, "opt/conf.d")
		must(t, os.Chmod(confd, 0o555)) // second.conf (new file) cannot be created
		before := f.reloads()
		out, _ := f.run("run")
		must(t, os.Chmod(confd, 0o755))
		if f.live("conf.d/site.conf") != prev {
			t.Fatal("partial nginx converge was not rolled back on copy failure")
		}
		if f.reloads() != before {
			t.Fatal("nginx reloaded despite an incomplete converge")
		}
		if !strings.Contains(out, "permission denied") {
			t.Fatalf("copy failure was not classified as a permission error:\n%s", out)
		}
		// dir writable again — full converge succeeds (the exit status is still
		// non-zero: cases 17/18 left a committed symlink and a live symlink in
		// the fixture, and the Go engine reports errors through its exit code)
		_, _ = f.run("run")
		if !strings.Contains(f.live("conf.d/site.conf"), "listen 90") {
			t.Fatal("converge did not complete after copy failure cleared")
		}
		if !f.exists("opt/conf.d/second.conf") {
			t.Fatal("second conf missing after retry")
		}
		if f.reloads() != before+1 {
			t.Fatal("healthy retry did not reload once")
		}
	})
}

package reconcile

// Case 19 of the bash suite ("the real path map holds its contract") for each
// of the three box manifests: every mapping the bash map-paths.sh produced,
// every skip it enforced.

import (
	"os"
	"path/filepath"
	"testing"
)

type mapCase struct {
	path string
	dest string // "" = must be skipped
	hook Hook
	app  string
}

func loadFixtureManifest(t *testing.T, name string) Manifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "manifests", name+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(raw)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return m
}

func checkMap(t *testing.T, m Manifest, cases []mapCase) {
	t.Helper()
	for _, c := range cases {
		got, ok := m.MapPath(c.path)
		if c.dest == "" {
			if ok {
				t.Errorf("%s: must be skipped, mapped to %s (%s)", c.path, got.Dest, got.Hook)
			}
			continue
		}
		if !ok {
			t.Errorf("%s: skipped, want %s (%s)", c.path, c.dest, c.hook)
			continue
		}
		if got.Dest != c.dest || got.Hook != c.hook || got.App != c.app {
			t.Errorf("%s: got %s/%s/%q, want %s/%s/%q", c.path, got.Dest, got.Hook, got.App, c.dest, c.hook, c.app)
		}
		if got.Hook == HookCompose && !got.RequireLiveDir {
			t.Errorf("%s: compose mapping must require the live app dir", c.path)
		}
	}
}

func TestProdulinkaMapContract(t *testing.T) {
	m := loadFixtureManifest(t, "produlinka")
	if m.Repo != "lovinka-infra" || m.HostLabel != "lovinka-vps" {
		t.Fatalf("identity drifted: %s / %s", m.Repo, m.HostLabel)
	}
	checkMap(t, m, []mapCase{
		{"nginx/nginx.conf", "/opt/nginx-proxy/nginx.conf", HookNginx, ""},
		{"nginx/api.fixit.app.conf", "/opt/nginx-proxy/conf.d/api.fixit.app.conf", HookNginx, ""},
		{"nginx/00-rate-limits.conf", "/opt/nginx-proxy/conf.d/00-rate-limits.conf", HookNginx, ""},
		{"scripts/deploy-fixit-prod.sh", "/opt/scripts/deploy-fixit-prod.sh", HookNone, ""},
		{"scripts/lib/telegram-notify.sh", "/opt/scripts/lib/telegram-notify.sh", HookNone, ""},
		{"apps/qtta/docker-compose.yml", "/opt/apps/qtta/docker-compose.yml", HookCompose, "qtta"},
		{"apps/fixit-prod/.env.example", "", "", ""},
		// the reservine env CONTRACT converges, the live .env* files never do
		{"apps/reservine-devlp/.env.example", "/opt/apps/reservine-devlp/.env.example", HookCompose, "reservine-devlp"},
		{"apps/reservine-devlp/.env.fe.example", "/opt/apps/reservine-devlp/.env.fe.example", HookCompose, "reservine-devlp"},
		{"apps/reservine-devlp/.env.compose.example", "/opt/apps/reservine-devlp/.env.compose.example", HookCompose, "reservine-devlp"},
		{"apps/reservine-devlp/.env", "", "", ""},
		{"apps/reservine-devlp/.env.fe", "", "", ""},
		{"apps/fixit-dev/docker-compose.yml", "", "", ""},
		{"apps/eve-fixit-prod/docker-compose.yml", "", "", ""},
		{"apps/_shared/daemon.json", "", "", ""},
		{"nginx/deployik-foo.conf", "", "", ""},
		{"scripts/infra-reconcile/reconcile.sh", "", "", ""},
		{"README.md", "", "", ""},
		{"docs/plan.md", "", "", ""},
		{"apps/qtta/secrets.age", "", "", ""},
		{".env", "", "", ""},
		{"systemd/some.service", "", "", ""},
		{"html/index.html", "", "", ""},
		{"vpn/create-profile.sh", "", "", ""},
	})
}

func TestDevulinkaMapContract(t *testing.T) {
	m := loadFixtureManifest(t, "devulinka")
	if m.Repo != "devulinka-infra" || m.HostLabel != "devops-vps" {
		t.Fatalf("identity drifted: %s / %s", m.Repo, m.HostLabel)
	}
	checkMap(t, m, []mapCase{
		{"nginx/eve.dev.lovinka.com.conf", "/opt/nginx-proxy/conf.d/eve.dev.lovinka.com.conf", HookNginx, ""},
		{"nginx/eve.dev.lovinka.com-initial.conf", "", "", ""},
		{"nginx-proxy/00-default.conf", "/opt/nginx-proxy/conf.d/00-default.conf", HookNginx, ""},
		{"nginx-proxy/docker-compose.yml", "/opt/nginx-proxy/docker-compose.yml", HookNone, ""},
		{"nginx-proxy/Dockerfile", "", "", ""},
		{"scripts/deploy-monitoring.sh", "/opt/scripts/deploy-monitoring.sh", HookNone, ""},
		{"apps/gh-runner/docker-compose.yml", "/opt/apps/gh-runner/docker-compose.yml", HookCompose, "gh-runner"},
		{"apps/eve-ai-layer/docker-compose.yml", "", "", ""},
		{"apps/eve-ai-layer/reconcile.sh", "", "", ""},
		{"apps/_shared/fail2ban.conf", "", "", ""},
		{"apps/gh-runner/.env", "", "", ""},
		{"apps/gh-runner/.env.example", "", "", ""},
		{"apps/x/secret.age", "", "", ""},
		{"scripts/infra-reconcile/map-paths.sh", "", "", ""},
		{"README.md", "", "", ""},
		{"systemd/x.timer", "", "", ""},
		{"cloud-init/user-data", "", "", ""},
		{"bin/tool", "", "", ""},
	})
	if len(m.AutoRollApps) != 0 {
		t.Fatal("gh-runner must never be auto-rolled — auto_roll_apps must stay empty")
	}
	for _, mp := range m.Mappings {
		if mp.Hook == HookCompose && mp.Owner != "root" {
			t.Errorf("devulinka apps mapping must carry the root owner hint (deploy user gap)")
		}
	}
	if len(m.Serve.Hosts) != 3 {
		t.Fatalf("hub hosts = %d, want the three boxes", len(m.Serve.Hosts))
	}
}

func TestWebulinkaMapContract(t *testing.T) {
	m := loadFixtureManifest(t, "webulinka")
	if m.Repo != "webulinka-infra" || m.HostLabel != "lovinka-vps" {
		t.Fatalf("identity drifted: %s / %s", m.Repo, m.HostLabel)
	}
	checkMap(t, m, []mapCase{
		{"host/nginx-proxy/conf.d/foo.conf", "/opt/nginx-proxy/conf.d/foo.conf", HookNginx, ""},
		{"host/nginx-proxy/conf.d/foo.inc", "/opt/nginx-proxy/conf.d/foo.inc", HookNginx, ""},
		{"host/nginx-proxy/nginx.conf", "/opt/nginx-proxy/nginx.conf", HookNginx, ""},
		{"scripts/deploy-tang.sh", "/opt/scripts/deploy-tang.sh", HookNone, ""},
		{"apps/deployik/docker-compose.yml", "/opt/apps/deployik/docker-compose.yml", HookCompose, "deployik"},
		{"scripts/infra-reconcile/reconcile.sh", "", "", ""},
		{"README.md", "", "", ""},
		{"apps/deployik/.env", "", "", ""},
		{"host/docker/daemon.json", "", "", ""},
		{"host/netplan/01.yaml", "", "", ""},
		{"systemd/some.service", "", "", ""},
		{"wireguard/wg0.conf", "", "", ""},
		{"apps/_shared/x", "", "", ""},
	})
}

// The devulinka pilot runs as `deploy`: a root-owned destination must surface
// as a classified permission error, never a crash, and must never block the
// rest of the sweep.
func TestDevulinkaRootOwnedDestClassifies(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := tempRoot(t)
	m := loadFixtureManifest(t, "devulinka")
	m.Clone = root + "/repo"
	m.StateDir = root + "/state"
	m.ModeFile = root + "/mode"
	m.LockFile = root + "/lock"
	m.AppsRoot = root + "/opt/apps"
	m.Alerts = nil
	// point the two root-owned trees at fixtures we can lock down
	for i := range m.Mappings {
		mp := &m.Mappings[i]
		switch mp.Dest {
		case "/opt/apps/{rest}":
			mp.Dest = root + "/opt/apps/{rest}"
		case "/opt/scripts/{rest}":
			mp.Dest = root + "/opt/scripts/{rest}"
		}
	}
	seedRepo(t, root, map[string]string{
		"apps/gh-runner/docker-compose.yml": "services: {}\n",
		"scripts/hello.sh":                  "echo hi\n",
	})
	mustT(t, os.MkdirAll(root+"/opt/apps/gh-runner", 0o755))
	mustT(t, os.Chmod(root+"/opt/apps/gh-runner", 0o555)) // "root-owned"
	t.Cleanup(func() { _ = os.Chmod(root+"/opt/apps/gh-runner", 0o755) })
	mustT(t, os.WriteFile(m.ModeFile, []byte("converge\n"), 0o644))

	e := &Engine{M: m}
	res, err := e.Run()
	if err == nil {
		t.Fatal("run with a refused write must exit non-zero")
	}
	perm := 0
	for _, is := range res.Errors {
		if is.Kind == "permission" && is.Path == "apps/gh-runner/docker-compose.yml" {
			perm++
			for _, want := range []string{"permission denied", "manifest owner hint: root", "running as"} {
				if !contains1(is.Message, want) {
					t.Errorf("permission issue lacks %q: %s", want, is.Message)
				}
			}
		}
	}
	if perm != 1 {
		t.Fatalf("want exactly one classified permission error, got %d in %+v", perm, res.Errors)
	}
	if _, err := os.Stat(root + "/opt/scripts/hello.sh"); err != nil {
		t.Fatal("the refused write blocked the rest of the sweep")
	}
}

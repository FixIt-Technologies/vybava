package claudeguards

import (
	"strings"
	"testing"
)

func TestBadFilePatterns(t *testing.T) {
	block := []string{
		"id_rsa", "keys/id_ed25519.bak", "cert.pem", "server.key", "app.p12",
		"deploy.pfx", "site.crt", "ca.cer", "x.der", "release.jks", "app.keystore",
		"putty.ppk", "cluster.kubeconfig", ".env", ".env.local", "api/.netrc",
		"ssh/known_hosts", "ssh/authorized_keys",
	}
	pass := []string{".env.example", "src/main.go", "docs/keys.md", "monkey.ts", "envelope.env.example"}
	for _, f := range block {
		if !reBadFile.MatchString(f) || reEnvExample.MatchString(f) {
			t.Errorf("should block staged file %q", f)
		}
	}
	for _, f := range pass {
		if reBadFile.MatchString(f) && !reEnvExample.MatchString(f) {
			t.Errorf("should pass staged file %q", f)
		}
	}
}

func TestSecretPatterns(t *testing.T) {
	block := []string{
		"+-----BEGIN RSA PRIVATE KEY-----",
		"+aws_key = AKIAIOSFODNN7EXAMPLE",
		"+token: ghp_abcdefghijklmnopqrstuv123456",
		"+github_pat_11ABCDEFG0123456789abcdef",
		"+slack: xoxb-1234567890-abcdef",
		"+key = sk-ant-api03-abcdefghijklmnopqrst",
		"+g = AIzaSyA-abcdefghijklmnopqrstuvwxyz0123456",
		"+jwt eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIx",
	}
	pass := []string{"+const skill = 'sk-illful'", "+// mention AKIA keys in docs", "+x = 1"}
	for _, l := range block {
		if !reSecret.MatchString(l) {
			t.Errorf("should flag %q", l)
		}
	}
	for _, l := range pass {
		if reSecret.MatchString(l) {
			t.Errorf("should pass %q", l)
		}
	}
}

func TestPasswordAssignments(t *testing.T) {
	if !rePassword.MatchString(`+password = "hunter2hunter2"`) {
		t.Error("literal password assignment should flag")
	}
	for _, l := range []string{
		`+password = "${DB_PASSWORD}"`,
		`+api_key = "process.env.KEY"`,
		`+secret: "changeme-please"`,
		`+token = "<your-token-here>"`,
	} {
		if rePassword.MatchString(l) && !rePasswordSkip.MatchString(l) {
			t.Errorf("placeholder/env form should pass: %q", l)
		}
	}
}

func TestCdPrefix(t *testing.T) {
	for cmd, want := range map[string]string{
		`cd /x/y && git commit -m m`:     "/x/y",
		`(cd "/a b" && git commit -m m)`: "/a b",
		`git commit -m m`:                "",
	} {
		got := ""
		if m := reCdPrefix.FindStringSubmatch(cmd); m != nil {
			got = strings.TrimSpace(m[1])
		}
		if got != want {
			t.Errorf("cd prefix of %q: got %q, want %q", cmd, got, want)
		}
	}
}

func TestPrivateIP(t *testing.T) {
	private := []string{"10.0.0.1", "127.0.0.1", "172.16.0.1", "172.31.9.9", "192.168.1.1", "169.254.0.1", "0.0.0.0"}
	public := []string{"95.216.27.220", "8.8.8.8", "172.32.0.1"}
	for _, ip := range private {
		if !rePrivateIP.MatchString(ip) {
			t.Errorf("%s should be private", ip)
		}
	}
	for _, ip := range public {
		if rePrivateIP.MatchString(ip) {
			t.Errorf("%s should be public", ip)
		}
	}
}

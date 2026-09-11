package claudeguards

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGuardConfigPerCall(t *testing.T) {
	root, _, big, _ := fixture(t)
	write := func(raw string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "vybava.config.json"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"guards":{"noRead":["**/big.ts"],"maxDumpLines":50}}`)
	if d := contextBashMatch("sed -n '1,10p' "+big, root); d == nil || d.Rule != "context:no-read" {
		t.Fatalf("noRead: %v", d)
	}
	if d := contextReadMatch(big, 10, root); d == nil || d.Rule != "context:no-read" {
		t.Fatalf("Read noRead: %v", d)
	}
	if d := contextBashMatch("rg -n x "+big, root); d != nil {
		t.Fatal(d)
	}
	write(`{"guards":{"maxDumpLines":75}}`)
	if d := contextBashMatch("sed -n '1,70p' "+big, root); d != nil {
		t.Fatal(d)
	}
	if d := contextBashMatch("sed -n '1,80p' "+big, root); d == nil {
		t.Fatal("configured budget ignored")
	}
	if d := contextReadMatch(big, 80, root); d == nil {
		t.Fatal("Read limit bypassed configured budget")
	}
	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"**/translation-keys.d.ts", "translation-keys.d.ts", true},
		{"packages/generated/**", "packages/generated/a/b.ts", true},
		{"*.lock", "nested/bun.lock", false},
	} {
		if got := MatchNoRead(tc.pattern, tc.name); got != tc.want {
			t.Fatalf("%s %s: %v", tc.pattern, tc.name, got)
		}
	}
}

func TestUnboundedOutput(t *testing.T) {
	for _, tc := range []struct{ deny, allow string }{
		{"docker logs app", "docker logs --tail 200 app"},
		{"gh run view 12 --log", "gh run view 12 --log | tail -100"},
		{"gh run view 12 --log-failed", "gh run view 12"},
		{"git log --oneline", "git log --oneline -20"},
		{"git log", "git log --max-count=10"},
		{"git diff", "git diff --stat"},
		{"git show HEAD", "git show HEAD -- file.go"},
		{"bun test", "bun test file.test.ts"},
		{"go test ./...", "go test ./... -run TestBudget"},
		{"bunx jest", "bunx jest > /tmp/jest.log"},
	} {
		t.Run(tc.deny, func(t *testing.T) {
			if d := contextBashMatch(tc.deny, t.TempDir()); d == nil || d.Rule != "context:unbounded-output" {
				t.Fatalf("deny: %v", d)
			}
			if d := contextBashMatch(tc.allow, t.TempDir()); d != nil {
				t.Fatalf("allow: %v", d)
			}
		})
	}
}

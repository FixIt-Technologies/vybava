package claudeguards

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture writes an n-line file under a non-throwaway root and points the
// transcript root at the same tree so every rule is exercisable offline.
func fixture(t *testing.T) (root string, small, big, transcript string) {
	t.Helper()
	root = t.TempDir()
	saveRoots, saveTranscript := throwawayRoots, transcriptRoot
	throwawayRoots, transcriptRoot = nil, filepath.Join(root, "projects")
	t.Cleanup(func() { throwawayRoots, transcriptRoot = saveRoots, saveTranscript })
	small = filepath.Join(root, "small.ts")
	big = filepath.Join(root, "big.ts")
	transcript = filepath.Join(root, "projects", "slug", "abc.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o755); err != nil {
		t.Fatal(err)
	}
	for p, n := range map[string]int{small: 50, big: 995, transcript: 3} {
		if err := os.WriteFile(p, []byte(strings.Repeat("line\n", n)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, small, big, transcript
}

func TestContextBashMatch(t *testing.T) {
	root, small, big, transcript := fixture(t)
	cases := []struct {
		name string
		cmd  string
		want string // rule, "" = pass
	}{
		// --- inline-script-write ---
		{"python heredoc patch", "python3 - <<'EOF'\np='a.ts'\ns=open(p).read()\nopen(p,'w').write(s.replace('a','b'))\nEOF", "context:inline-script-write"},
		{"python -c write_text", `python3 -c "from pathlib import Path; Path('x').write_text('y')"`, "context:inline-script-write"},
		{"node -e writeFileSync", `node -e "require('fs').writeFileSync('x.json', JSON.stringify({}))"`, "context:inline-script-write"},
		{"python heredoc read-only ok", "python3 - <<'EOF'\nimport json\nprint(len(json.load(open('cs.json'))))\nEOF", ""},
		{"node -e read-only ok", `node -e "console.log(require('./package.json').version)"`, ""},
		{"python script by path ok", "python3 /tmp/patch.py", ""},
		{"escape hatch", "CLAUDE_ALLOW_SHELL_EDIT=1 python3 - <<'EOF'\nopen('x','w').write('y')\nEOF", ""},

		// --- heredoc-overwrite ---
		{"cat > existing", "cat > " + small + " <<'EOF'\nnew\nEOF", "context:heredoc-overwrite"},
		{"cat heredoc then redirect existing", "cat <<'EOF' > " + small + "\nnew\nEOF", "context:heredoc-overwrite"},
		{"tee existing", "echo x | tee " + small, "context:heredoc-overwrite"},
		{"tee -a ok", "echo x | tee -a " + small, ""},
		{"append ok", "cat >> " + small + " <<'EOF'\nmore\nEOF", ""},
		{"new file ok", "cat > " + filepath.Join(root, "fresh.ts") + " <<'EOF'\nnew\nEOF", ""},
		{"tmp target ok", "cat > /tmp/scratch.js <<'EOF'\nnew\nEOF", ""},
		{"escape hatch", "CLAUDE_ALLOW_SHELL_EDIT=1 cat > " + small + " <<'EOF'\nnew\nEOF", ""},

		// --- whole-file-dump ---
		{"cat big", "cat " + big, "context:whole-file-dump"},
		{"cat small ok", "cat " + small, ""},
		{"cat several small over budget", "cat " + small + " " + small + " " + small + " " + small + " " + small, "context:whole-file-dump"},
		{"cat big piped ok", "cat " + big + " | grep foo", ""},
		{"cat big redirected ok", "cat " + big + " > /tmp/copy.ts", ""},
		{"cat big then chained", "cat " + big + " && echo done", "context:whole-file-dump"},
		{"sed range in budget ok", "sed -n '120,180p' " + big, ""},
		{"sed range over budget", "sed -n '1,400p' " + big, "context:whole-file-dump"},
		{"sed to end", "sed -n '500,$p' " + big, "context:whole-file-dump"},
		{"sed several ranges ok", "sed -n '1,60p;400,460p' " + big, ""},
		{"sed -e ranges", "sed -n -e '1,150p' -e '300,400p' " + big, "context:whole-file-dump"},
		{"sed substitute prints nothing counted", "sed -n 's/a/b/p' " + big, ""},
		{"sed -i ok", "sed -i '' 's/a/b/' " + big, ""},
		{"head default ok", "head " + big, ""},
		{"head -n 300", "head -n 300 " + big, "context:whole-file-dump"},
		{"head -300", "head -300 " + big, "context:whole-file-dump"},
		{"head -n 100 ok", "head -n 100 " + big, ""},
		{"tail -n 500", "tail -n 500 " + big, "context:whole-file-dump"},
		{"missing file ok", "cat " + filepath.Join(root, "nope.ts"), ""},
		{"escape hatch", "CLAUDE_ALLOW_CONTEXT_DUMP=1 cat " + big, ""},
		{"cat in quoted heredoc body ok", "cat > /tmp/x.sh <<'EOF'\ncat " + big + "\nEOF", ""},
		{"echo mentions cat ok", "echo cat " + big, ""},

		// --- transcript-dump ---
		{"cat transcript", "cat " + transcript, "context:transcript-dump"},
		{"sed transcript", "sed -n '1,5p' " + transcript, "context:transcript-dump"},
		{"transcript piped into jq ok", "cat " + transcript + " | jq -c .type", ""},
		{"node over transcript ok", "node /tmp/agg.js " + transcript, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ""
			if d := contextBashMatch(c.cmd, root); d != nil {
				got = d.Rule
			}
			if got != c.want {
				t.Fatalf("cmd %q: got %q want %q", c.cmd, got, c.want)
			}
		})
	}
}

func TestContextReadMatch(t *testing.T) {
	root, small, big, transcript := fixture(t)
	cases := []struct {
		name  string
		path  string
		limit int
		want  string
	}{
		{"big without limit", big, 0, "context:whole-file-dump"},
		{"big with limit", big, 120, ""},
		{"small", small, 0, ""},
		{"relative path", "big.ts", 0, "context:whole-file-dump"},
		{"missing", filepath.Join(root, "nope.ts"), 0, ""},
		{"png ignored", filepath.Join(root, "shot.png"), 0, ""},
		{"transcript", transcript, 0, "context:transcript-dump"},
		{"transcript even with limit", transcript, 10, "context:transcript-dump"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ""
			if d := contextReadMatch(c.path, c.limit, root); d != nil {
				got = d.Rule
			}
			if got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestDenialText(t *testing.T) {
	d := deny("x:y", "why", "")
	if !strings.HasPrefix(d.Text(), "🚨 BLOCKED by claude-guards (x:y)") || strings.Contains(d.Text(), "\n\n\n") {
		t.Fatalf("unexpected text %q", d.Text())
	}
}

func TestLokCatalogRule(t *testing.T) {
	root, _, _, _ := fixture(t)
	cat := filepath.Join(root, "locales", "cs.json")
	if err := os.MkdirAll(filepath.Dir(cat), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cat, []byte("{\n  \"a\": \"b\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "vybava.config.json"), []byte(`{"lok":{"catalogs":{"m":{"style":"english-as-key","files":"locales/{locale}.json","locales":["cs"]}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := contextBashMatch("cat "+cat, root); d == nil || d.Rule != "context:locale-catalog" {
		t.Fatalf("cat of a 3-line catalog must still block, got %v", d)
	}
	if d := contextBashMatch("sed -n '1,2p' "+cat, root); d == nil || d.Rule != "context:locale-catalog" {
		t.Fatalf("ranged read must block, got %v", d)
	}
	if d := contextReadMatch(cat, 50, root); d == nil || d.Rule != "context:locale-catalog" {
		t.Fatalf("Read with limit must block, got %v", d)
	}
	if d := contextBashMatch("cat "+cat+" | jq keys", root); d != nil {
		t.Fatalf("piped read stays allowed, got %v", d)
	}
	if d := contextBashMatch("CLAUDE_ALLOW_CONTEXT_DUMP=1 cat "+cat, root); d != nil {
		t.Fatalf("escape hatch, got %v", d)
	}
}

// Files past the 4 MiB read cap report a sentinel line count. A sentinel near
// maxInt summed to a negative total once three of them appeared on one command
// line, so the guard allowed exactly the dump it exists to stop.
func TestDumpBudgetSumsUnmeasuredFilesWithoutWrapping(t *testing.T) {
	root := t.TempDir()
	var paths []string
	for _, name := range []string{"a.log", "b.log", "c.log"} {
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, []byte(strings.Repeat("x\n", 2<<20)), 0600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	d := contextBashMatch("cat "+strings.Join(paths, " "), root)
	if d == nil || d.Rule != "context:whole-file-dump" {
		t.Fatalf("three unmeasured files must stay denied, got %v", d)
	}
	if strings.Contains(d.Message, "4611686018427387903") {
		t.Fatalf("sentinel leaked into the message: %s", d.Message)
	}
}

// A pipe is only an exemption when the downstream command shrinks its input.
func TestPipeExemptionNeedsAReducingSink(t *testing.T) {
	root := t.TempDir()
	big := filepath.Join(root, "big.txt")
	if err := os.WriteFile(big, []byte(strings.Repeat("x\n", 500)), 0600); err != nil {
		t.Fatal(err)
	}
	if d := contextBashMatch("cat "+big+" | jq .", root); d != nil {
		t.Fatalf("a reducing sink stays allowed, got %v", d)
	}
	if d := contextBashMatch("cat "+big+" | cat", root); d == nil || d.Rule != "context:whole-file-dump" {
		t.Fatalf("| cat reproduces the file whole and must be denied, got %v", d)
	}
}

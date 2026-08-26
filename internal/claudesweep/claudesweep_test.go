package claudesweep

import (
	"strings"
	"testing"
	"time"
)

func TestIsSwarmSocket(t *testing.T) {
	cases := map[string]bool{
		"claude-swarm-11256":     true,
		"claude-swarm-1":         true,
		"default":                false,
		"claude-swarm-":          false,
		"claude-swarmish":        false,
		"tmux-501":               false,
		"claude-swarm-1/../evil": false,
		"":                       false,
	}
	for name, want := range cases {
		if got := IsSwarmSocket(name); got != want {
			t.Errorf("IsSwarmSocket(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestParseAge(t *testing.T) {
	valid := map[string]time.Duration{
		"24h":   24 * time.Hour,
		"3d":    72 * time.Hour,
		"1d12h": 36 * time.Hour,
		"90m":   90 * time.Minute,
		"0.5d":  12 * time.Hour,
	}
	for input, want := range valid {
		got, err := ParseAge(input)
		if err != nil {
			t.Errorf("ParseAge(%q): %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("ParseAge(%q) = %v, want %v", input, got, want)
		}
	}
	for _, input := range []string{"", "banana", "-5h", "0", "0s", "24"} {
		if _, err := ParseAge(input); err == nil {
			t.Errorf("ParseAge(%q) should fail", input)
		}
	}
}

func TestPaneIdleTranscriptWins(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	age := 24 * time.Hour

	stale := now.Add(-30 * time.Hour)
	idle, reason := PaneIdle(PaneEvidence{TranscriptMtime: &stale}, now, age)
	if !idle {
		t.Errorf("stale transcript should be idle, got %q", reason)
	}

	fresh := now.Add(-time.Hour)
	// A busy-looking screen must not matter when the transcript is fresh…
	idle, _ = PaneIdle(PaneEvidence{TranscriptMtime: &fresh, CaptureTail: "❯"}, now, age)
	if idle {
		t.Error("fresh transcript must keep the pane active")
	}
	// …and an idle-looking screen must not matter either: transcript is authoritative.
	old := now.Add(-100 * time.Hour)
	idle, _ = PaneIdle(PaneEvidence{TranscriptMtime: &fresh, CaptureTail: "❯", SessionCreated: old}, now, age)
	if idle {
		t.Error("fresh transcript must override idle screen markers")
	}
}

func TestPaneIdleFallbackNeedsMarkerAndOldSession(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	age := 24 * time.Hour
	oldSession := now.Add(-72 * time.Hour)
	youngSession := now.Add(-2 * time.Hour)

	idle, _ := PaneIdle(PaneEvidence{CaptureTail: "some output\n❯\n", SessionCreated: oldSession}, now, age)
	if !idle {
		t.Error("prompt marker + old session should be idle")
	}
	idle, _ = PaneIdle(PaneEvidence{CaptureTail: "Ready for a new task? /clear to save tokens", SessionCreated: oldSession}, now, age)
	if !idle {
		t.Error("new-task hint + old session should be idle")
	}
	idle, _ = PaneIdle(PaneEvidence{CaptureTail: "❯", SessionCreated: youngSession}, now, age)
	if idle {
		t.Error("marker on a young session must not be idle")
	}
	idle, _ = PaneIdle(PaneEvidence{CaptureTail: "Running tests…\ncompiling", SessionCreated: oldSession}, now, age)
	if idle {
		t.Error("old session without idle marker must not be idle")
	}
	idle, _ = PaneIdle(PaneEvidence{CaptureTail: "❯"}, now, age)
	if idle {
		t.Error("marker without a session age must not be idle")
	}
}

func TestHasIdleMarkerOnlyInspectsTrailingLines(t *testing.T) {
	if !HasIdleMarker("build ok\n\n❯ \n\n") {
		t.Error("trailing prompt line should count despite blank lines")
	}
	buried := "❯\n" + strings.Repeat("log line\n", idleMarkerWindow+1)
	if HasIdleMarker(buried) {
		t.Error("a prompt buried above the trailing window must not count")
	}
	if HasIdleMarker("") {
		t.Error("empty capture has no marker")
	}
}

func TestLiveVerdict(t *testing.T) {
	if got := LiveVerdict(nil); got != VerdictActive {
		t.Errorf("no panes should be conservative ACTIVE, got %s", got)
	}
	idlePane := Pane{Idle: true}
	busyPane := Pane{Idle: false}
	if got := LiveVerdict([]Pane{idlePane, busyPane}); got != VerdictActive {
		t.Errorf("one busy pane must keep the swarm ACTIVE, got %s", got)
	}
	if got := LiveVerdict([]Pane{idlePane, idlePane}); got != VerdictIdleReapable {
		t.Errorf("all-idle panes should be IDLE-REAPABLE, got %s", got)
	}
}

func TestParsePaneLine(t *testing.T) {
	line := "%3\t4242\tnode\t1755600000\t/Users/dev/Work/lovinka\tteammate-researcher"
	info, err := ParsePaneLine(line)
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != "%3" || info.PID != 4242 || info.Command != "node" {
		t.Errorf("parsed %+v", info)
	}
	if info.SessionCreated.Unix() != 1755600000 {
		t.Errorf("session created = %v", info.SessionCreated)
	}
	if info.Path != "/Users/dev/Work/lovinka" || info.Title != "teammate-researcher" {
		t.Errorf("parsed %+v", info)
	}
	for _, malformed := range []string{"", "just one field", "%1\tnot-a-pid\tzsh\t123\t/tmp"} {
		if _, err := ParsePaneLine(malformed); err == nil {
			t.Errorf("ParsePaneLine(%q) should fail", malformed)
		}
	}
}

func TestParseLsofTranscripts(t *testing.T) {
	output := strings.Join([]string{
		"p4242",
		"fcwd", "n/Users/dev/Work/lovinka",
		"f3", "n/Users/dev/.claude/projects/-Users-dev-Work-lovinka/abc123.jsonl",
		"f4", "n/dev/null",
		"f5", "n/Users/dev/.claude/projects/-Users-dev/other.log",
	}, "\n")
	got := ParseLsofTranscripts(output)
	want := "/Users/dev/.claude/projects/-Users-dev-Work-lovinka/abc123.jsonl"
	if len(got) != 1 || got[0] != want {
		t.Errorf("ParseLsofTranscripts = %v, want [%s]", got, want)
	}
}

func TestParseOrphans(t *testing.T) {
	output := strings.Join([]string{
		"  101     1 05-12:33:12 /usr/local/bin/claude --agent-id researcher-a1",
		"  202   500 01:02:03 claude --agent-id worker-b2",       // parent alive → not orphaned
		"  303     1 12:00:00 /bin/zsh -c something --agent-id",  // not a claude binary
		"  404     1 00:10:00 /opt/homebrew/bin/claude --resume", // no --agent-id
		"garbage line",
	}, "\n")
	got := ParseOrphans(output)
	if len(got) != 1 {
		t.Fatalf("ParseOrphans = %+v, want exactly one", got)
	}
	if got[0].PID != 101 || got[0].Elapsed != "05-12:33:12" || !strings.Contains(got[0].Command, "--agent-id researcher-a1") {
		t.Errorf("orphan = %+v", got[0])
	}
}

func TestFormatAge(t *testing.T) {
	cases := map[time.Duration]string{
		45 * time.Minute:              "45m",
		26 * time.Hour:                "1d2h",
		72 * time.Hour:                "3d",
		3 * time.Hour:                 "3h",
		-time.Hour:                    "0m",
		74*time.Hour + 30*time.Minute: "3d2h",
	}
	for input, want := range cases {
		if got := FormatAge(input); got != want {
			t.Errorf("FormatAge(%v) = %q, want %q", input, got, want)
		}
	}
}

func TestRenderLaunchdPlist(t *testing.T) {
	plist := RenderLaunchdPlist("/usr/local/bin/vybava", []string{"claude-sweep", "--kill", "--age", "24h"}, "/Users/dev/Library/Logs/claude-sweep.log")
	for _, fragment := range []string{
		"<string>" + LaunchdLabel + "</string>",
		"<string>/usr/local/bin/vybava</string>",
		"<string>claude-sweep</string>",
		"<string>--kill</string>",
		"<string>24h</string>",
		"<key>Hour</key>\n    <integer>6</integer>",
		"<string>/Users/dev/Library/Logs/claude-sweep.log</string>",
	} {
		if !strings.Contains(plist, fragment) {
			t.Errorf("plist missing %q", fragment)
		}
	}
	escaped := RenderLaunchdPlist(`/odd/pa"th/<bin>`, nil, "/log")
	if !strings.Contains(escaped, "&quot;") || !strings.Contains(escaped, "&lt;bin&gt;") {
		t.Error("plist arguments must be XML-escaped")
	}
}

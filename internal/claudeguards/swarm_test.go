package claudeguards

import (
	"reflect"
	"testing"
)

func TestLeaderPID(t *testing.T) {
	cases := map[string]struct {
		pid int
		ok  bool
	}{
		"claude-swarm-4202": {4202, true},
		"claude-swarm-0":    {0, false},
		"claude-swarm-abc":  {0, false},
		"default":           {0, false},
		"swarm-4202":        {0, false},
	}
	for name, want := range cases {
		pid, ok := leaderPID(name)
		if pid != want.pid || ok != want.ok {
			t.Errorf("leaderPID(%q) = %d,%v want %d,%v", name, pid, ok, want.pid, want.ok)
		}
	}
}

func TestSelectSwarmsDeadLeadersAndOwnSession(t *testing.T) {
	sockets := []string{
		"default",           // not a swarm: never touched
		"claude-swarm-100",  // leader alive, unrelated: keep
		"claude-swarm-200",  // leader dead: sweep
		"claude-swarm-300",  // leader is our ancestor (session ending): tear down
		"claude-swarm-junk", // malformed: ignore
	}
	alive := func(pid int) bool { return pid == 100 || pid == 300 }
	ancestors := map[int]bool{300: true, 1: true}

	got := selectSwarms(sockets, alive, ancestors)
	want := []string{"claude-swarm-200", "claude-swarm-300"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selectSwarms = %v want %v", got, want)
	}
}

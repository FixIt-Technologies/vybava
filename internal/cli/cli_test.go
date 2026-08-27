package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ in, want string }{
		{"~", home},
		{"~/Exports/FixIt/perf/", filepath.Join(home, "Exports/FixIt/perf")},
		{"/abs/path", "/abs/path"},
		{"relative/path", "relative/path"},
		{"~user/x", "~user/x"}, // only the current user's ~ is expanded
	}
	for _, c := range cases {
		got, err := expandHome(c.in)
		if err != nil {
			t.Fatalf("expandHome(%q) error: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("expandHome(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

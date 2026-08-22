package fontfreeze

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPlanInstancesSortedAndResolved(t *testing.T) {
	m := Manifest{Fonts: []Font{{
		Master:    "assets/fonts/Fraunces-variable.ttf",
		Family:    "fraunces",
		Out:       "assets/fonts",
		Languages: []string{"latin", "latin-ext"},
		Instances: map[string]map[string]float64{
			"heading": {"wght": 600, "opsz": 120, "SOFT": 40, "WONK": 1},
			"brick":   {"wght": 400, "opsz": 14},
		},
	}}}

	jobs, err := Plan(m, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("want 2 jobs, got %d", len(jobs))
	}
	// Instance names iterate sorted: brick before heading.
	if got := jobs[0].Output; got != "/repo/assets/fonts/fraunces-brick.woff2" {
		t.Errorf("job order/output wrong: %s", got)
	}
	// Axis args sorted by tag for deterministic invocations.
	want := []string{"opsz=14", "wght=400"}
	if !reflect.DeepEqual(jobs[0].InstancerArgs, want) {
		t.Errorf("instancer args = %v, want %v", jobs[0].InstancerArgs, want)
	}
	if !strings.HasPrefix(jobs[0].Unicodes, LanguagePresets["latin"]) {
		t.Errorf("unicodes should start with the latin preset")
	}
	if !strings.HasSuffix(jobs[0].Unicodes, LanguagePresets["latin-ext"]) {
		t.Errorf("unicodes should end with the latin-ext preset")
	}
}

func TestPlanStaticFontIsSubsetOnly(t *testing.T) {
	m := Manifest{Fonts: []Font{{
		Master:    "fonts/Static.ttf",
		Family:    "static",
		Languages: []string{"latin"},
	}}}
	jobs, err := Plan(m, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].InstancerArgs != nil {
		t.Fatalf("static font should yield one subset-only job, got %+v", jobs)
	}
	// Out defaults to the master's directory.
	if jobs[0].Output != filepath.Join("fonts", "static.woff2") {
		t.Errorf("output = %s", jobs[0].Output)
	}
}

func TestPlanRejectsUnknownLanguage(t *testing.T) {
	m := Manifest{Fonts: []Font{{Master: "a.ttf", Family: "a", Languages: []string{"klingon"}}}}
	if _, err := Plan(m, "."); err == nil || !strings.Contains(err.Error(), "klingon") {
		t.Fatalf("want unknown-language error naming the input, got %v", err)
	}
}

func TestRunDrivesInstancerThenSubset(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "fam-heading.woff2")
	var calls [][]string
	runner := func(name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		// subset call creates the output so Run can stat it
		for _, a := range args {
			if strings.HasPrefix(a, "--output-file=") {
				os.WriteFile(strings.TrimPrefix(a, "--output-file="), []byte("x"), 0o644)
			}
		}
		return nil
	}

	report, err := Run([]Job{{
		Master: "m.ttf", Output: out,
		InstancerArgs: []string{"wght=600"}, Unicodes: "U+0-FF",
	}}, runner)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("want instancer + subset calls, got %d", len(calls))
	}
	if calls[0][1] != "varLib.instancer" || calls[1][1] != "subset" {
		t.Errorf("call order wrong: %v", calls)
	}
	// The feature-preservation flag is load-bearing; losing it silently
	// drops kerning from outputs.
	if !contains(calls[1], "--layout-features=*") {
		t.Errorf("subset must keep all layout features: %v", calls[1])
	}
	if len(report.Outputs) != 1 || report.Outputs[0].Bytes != 1 {
		t.Errorf("report = %+v", report)
	}
}

func TestLoadManifestRejectsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fonts.yaml")
	os.WriteFile(path, []byte("fonts: []\n"), 0o644)
	if _, err := LoadManifest(path); err == nil {
		t.Fatal("want error for empty manifest")
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

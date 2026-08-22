// Package fontfreeze freezes variable webfonts at the style positions a site
// actually renders, then subsets them to the languages it actually sets.
//
// A variable font is base outlines plus delta data for every position of its
// axes; sites typically render one or two positions and ship the whole space.
// Instancing evaluates the deltas at fixed coordinates — the same math the
// browser does per page load — and writes a static font that renders
// pixel-identically at those coordinates at a fraction of the bytes.
//
// Subsetting here is per LANGUAGE BLOCK (Google Fonts' own unicode ranges),
// never per character: copy changes can never break the output. What is
// removed is unused style space and unused alphabets, nothing else.
//
// The heavy lifting is delegated to fonttools (the reference implementation;
// `brew install fonttools`) — this package plans the work, drives the tool,
// and reports what it produced.
package fontfreeze

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// LanguagePresets are Google Fonts' canonical per-subset unicode ranges —
// the exact coverage next/font/google (and fonts.googleapis.com CSS) serves.
// Subsetting to these blocks matches what the hosted font would have shipped.
var LanguagePresets = map[string]string{
	"latin":        "U+0-FF,U+131,U+152-153,U+2BB-2BC,U+2C6,U+2DA,U+2DC,U+304,U+308,U+329,U+2000-206F,U+20AC,U+2122,U+2191,U+2193,U+2212,U+2215,U+FEFF,U+FFFD",
	"latin-ext":    "U+100-2BA,U+2BD-2C5,U+2C7-2CC,U+2CE-2D7,U+2DD-2FF,U+304,U+308,U+329,U+1D00-1DBF,U+1E00-1E9F,U+1EF2-1EFF,U+2020,U+20A0-20AB,U+20AD-20C0,U+2113,U+2C60-2C7F,U+A720-A7FF",
	"vietnamese":   "U+102-103,U+110-111,U+128-129,U+168-169,U+1A0-1A1,U+1AF-1B0,U+300-301,U+303-304,U+308-309,U+323,U+329,U+1EA0-1EF9,U+20AB",
	"cyrillic":     "U+301,U+400-45F,U+490-491,U+4B0-4B1,U+2116",
	"cyrillic-ext": "U+460-52F,U+1C80-1C8A,U+20B4,U+2DE0-2DFF,U+A640-A69F,U+FE2E-FE2F",
	"greek":        "U+370-377,U+37A-37F,U+384-38A,U+38C,U+38E-3A1,U+3A3-3FF",
}

// Manifest is the fonts.yaml a project keeps next to its font masters.
type Manifest struct {
	Fonts []Font `yaml:"fonts"`
}

type Font struct {
	// Master is the variable (or static) font file, relative to the manifest.
	Master string `yaml:"master"`
	// Family names the output files: <out>/<family>-<instance>.woff2.
	Family string `yaml:"family"`
	// Out is the output directory, relative to the manifest.
	Out string `yaml:"out"`
	// Languages are LanguagePresets keys; their ranges are unioned.
	Languages []string `yaml:"languages"`
	// Instances maps an instance name to pinned axis coordinates, e.g.
	// heading: {wght: 600, opsz: 120}. Empty means subset-only (static
	// master), producing <family>.woff2.
	Instances map[string]map[string]float64 `yaml:"instances"`
}

// Job is one planned output file. Instancer args are nil for subset-only jobs.
type Job struct {
	Master        string   `json:"master"`
	Output        string   `json:"output"`
	InstancerArgs []string `json:"instancer_args,omitempty"`
	Unicodes      string   `json:"unicodes"`
}

type Output struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

type Report struct {
	Manifest string   `json:"manifest"`
	Outputs  []Output `json:"outputs"`
}

func LoadManifest(path string) (Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(m.Fonts) == 0 {
		return Manifest{}, fmt.Errorf("%s declares no fonts", path)
	}
	return m, nil
}

// Plan expands a manifest into jobs. Pure — no filesystem, no execution —
// so the mapping from manifest to fonttools invocations is testable.
// Paths in the manifest resolve against baseDir (the manifest's directory).
func Plan(m Manifest, baseDir string) ([]Job, error) {
	var jobs []Job
	for _, font := range m.Fonts {
		if font.Master == "" || font.Family == "" {
			return nil, fmt.Errorf("font entries need both master and family")
		}
		ranges, err := unicodeUnion(font.Languages)
		if err != nil {
			return nil, fmt.Errorf("font %s: %w", font.Family, err)
		}
		out := font.Out
		if out == "" {
			out = filepath.Dir(font.Master)
		}
		master := filepath.Join(baseDir, font.Master)
		if len(font.Instances) == 0 {
			jobs = append(jobs, Job{
				Master:   master,
				Output:   filepath.Join(baseDir, out, font.Family+".woff2"),
				Unicodes: ranges,
			})
			continue
		}
		for _, name := range sortedKeys(font.Instances) {
			jobs = append(jobs, Job{
				Master:        master,
				Output:        filepath.Join(baseDir, out, font.Family+"-"+name+".woff2"),
				InstancerArgs: axisArgs(font.Instances[name]),
				Unicodes:      ranges,
			})
		}
	}
	return jobs, nil
}

// axisArgs renders pinned coordinates as fonttools varLib.instancer
// arguments, sorted so identical manifests always produce identical
// invocations.
func axisArgs(axes map[string]float64) []string {
	args := make([]string, 0, len(axes))
	for _, tag := range sortedKeys(axes) {
		args = append(args, tag+"="+strconv.FormatFloat(axes[tag], 'f', -1, 64))
	}
	return args
}

func unicodeUnion(languages []string) (string, error) {
	if len(languages) == 0 {
		return "", fmt.Errorf("languages must list at least one preset (%s)", strings.Join(sortedKeys(LanguagePresets), ", "))
	}
	parts := make([]string, 0, len(languages))
	for _, lang := range languages {
		ranges, ok := LanguagePresets[lang]
		if !ok {
			return "", fmt.Errorf("unknown language %q (have: %s)", lang, strings.Join(sortedKeys(LanguagePresets), ", "))
		}
		parts = append(parts, ranges)
	}
	return strings.Join(parts, ","), nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Runner executes one external command; swapped out in tests.
type Runner func(name string, args ...string) error

func ExecRunner(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out)
	}
	return nil
}

// CheckTooling verifies fonttools is available, with an actionable hint.
func CheckTooling() error {
	if _, err := exec.LookPath("fonttools"); err != nil {
		return fmt.Errorf("fonttools not found on PATH — install it with `brew install fonttools`")
	}
	return nil
}

// Run executes the jobs: instance (when axes are pinned), subset to the
// language ranges, compress to woff2. `--layout-features='*'` keeps every
// OpenType feature — subset's default would silently drop some kerning and
// ligature behaviour.
func Run(jobs []Job, run Runner) (Report, error) {
	var report Report
	for _, job := range jobs {
		if err := os.MkdirAll(filepath.Dir(job.Output), 0o755); err != nil {
			return report, err
		}
		source := job.Master
		if len(job.InstancerArgs) > 0 {
			tmp, err := os.CreateTemp("", "fontfreeze-*.ttf")
			if err != nil {
				return report, err
			}
			tmp.Close()
			defer os.Remove(tmp.Name())
			args := append([]string{"varLib.instancer", job.Master}, job.InstancerArgs...)
			args = append(args, "-o", tmp.Name())
			if err := run("fonttools", args...); err != nil {
				return report, err
			}
			source = tmp.Name()
		}
		if err := run("fonttools", "subset", source,
			"--unicodes="+job.Unicodes,
			"--layout-features=*",
			"--flavor=woff2",
			"--output-file="+job.Output,
		); err != nil {
			return report, err
		}
		info, err := os.Stat(job.Output)
		if err != nil {
			return report, err
		}
		report.Outputs = append(report.Outputs, Output{Path: job.Output, Bytes: info.Size()})
	}
	return report, nil
}

func FormatText(report Report) string {
	var b strings.Builder
	for _, out := range report.Outputs {
		fmt.Fprintf(&b, "%7.1f KB  %s\n", float64(out.Bytes)/1024, out.Path)
	}
	return b.String()
}

// LicenseReminder is printed once per run. Modification is a licensing act:
// OFL fonts allow it (watch Reserved Font Name rules); most commercial
// webfont licenses forbid it outright.
const LicenseReminder = "note: instancing/subsetting modifies the font — verify the license permits it (OFL: yes, mind Reserved Font Names; commercial: usually no)"

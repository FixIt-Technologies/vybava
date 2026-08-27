package pwmcp

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Report is the machine-readable view of the workstation's Playwright state.
type Report struct {
	Version        string   `json:"version"`
	Root           string   `json:"root"`
	Browsers       string   `json:"browsers"`
	Runtime        string   `json:"runtime"`
	ServerInstalls bool     `json:"server_installed"`
	Present        []string `json:"browsers_present"`
	Missing        []string `json:"browsers_missing"`
	Orphans        []string `json:"browsers_orphaned"`
	Warnings       []string `json:"warnings"`
}

// legacyRegistry is Playwright's default location, which lives inside the OS
// cache directory and is therefore fair game for any disk-cleanup routine.
func legacyRegistry() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Caches", "ms-playwright")
}

// Status describes what is installed, what is missing, and what is dead weight.
func (c Config) Status(names []string) Report {
	if len(names) == 0 {
		names = DefaultBrowsers
	}
	report := Report{
		Version: c.Version, Root: c.Root, Browsers: c.Browsers,
		Runtime: c.Runtime, ServerInstalls: c.Installed(),
	}
	if !report.ServerInstalls {
		report.Warnings = append(report.Warnings, "pinned server not installed; run `pwmcp install`")
		return report
	}
	required, err := c.Required(names)
	if err != nil {
		report.Warnings = append(report.Warnings, err.Error())
		return report
	}
	missing, err := c.Missing(names)
	if err != nil {
		report.Warnings = append(report.Warnings, err.Error())
		return report
	}
	report.Missing = missing
	absent := make(map[string]struct{}, len(missing))
	for _, name := range missing {
		absent[name] = struct{}{}
	}
	for name, directory := range required {
		if _, gone := absent[name]; !gone {
			report.Present = append(report.Present, name+" "+directory)
		}
	}
	sort.Strings(report.Present)

	if orphans, err := c.Prune(true); err == nil {
		report.Orphans = orphans
	}
	if strings.HasPrefix(c.Browsers, filepath.Join(os.Getenv("HOME"), "Library", "Caches")) {
		report.Warnings = append(report.Warnings,
			"registry sits under ~/Library/Caches, where disk-cleanup routines delete it; move it with VYBAVA_PWMCP_BROWSERS")
	}
	if legacy := legacyRegistry(); legacy != "" && legacy != c.Browsers && exists(legacy) {
		report.Warnings = append(report.Warnings,
			fmt.Sprintf("a second registry still exists at %s; something is bypassing pwmcp", legacy))
	}
	return report
}

// FormatText renders a Report for a terminal.
func FormatText(report Report) string {
	var out strings.Builder
	fmt.Fprintf(&out, "pin       @playwright/mcp@%s\n", report.Version)
	fmt.Fprintf(&out, "server    %s\n", filepath.Join(report.Root, report.Version))
	fmt.Fprintf(&out, "browsers  %s\n", report.Browsers)
	fmt.Fprintf(&out, "runtime   %s\n", report.Runtime)
	fmt.Fprintf(&out, "installed %t\n", report.ServerInstalls)
	for _, present := range report.Present {
		fmt.Fprintf(&out, "  ok      %s\n", present)
	}
	for _, missing := range report.Missing {
		fmt.Fprintf(&out, "  missing %s\n", missing)
	}
	for _, orphan := range report.Orphans {
		fmt.Fprintf(&out, "  orphan  %s\n", orphan)
	}
	for _, warning := range report.Warnings {
		fmt.Fprintf(&out, "  warn    %s\n", warning)
	}
	return out.String()
}

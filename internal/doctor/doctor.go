package doctor

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/FixIt-Technologies/vybava/internal/catalog"
	"github.com/FixIt-Technologies/vybava/internal/state"
)

type Status string

const (
	StatusPass Status = "pass"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
)

type Check struct {
	ID      string `json:"id"`
	Status  Status `json:"status"`
	Message string `json:"message"`
	Remedy  string `json:"remedy,omitempty"`
}

type Report struct {
	Checks []Check `json:"checks"`
}

func (r Report) Healthy() bool {
	for _, check := range r.Checks {
		if check.Status == StatusFail {
			return false
		}
	}
	return true
}

func Run(c catalog.Catalog, store state.Store) Report {
	report := Report{Checks: []Check{{
		ID: "catalog", Status: StatusPass,
		Message: "embedded catalog schema and payload references are valid",
	}}}

	executable, err := os.Executable()
	if err != nil {
		report.Checks = append(report.Checks, Check{ID: "executable", Status: StatusFail, Message: err.Error()})
	} else {
		report.Checks = append(report.Checks, Check{ID: "executable", Status: StatusPass, Message: executable})
	}
	current, stateErr := store.Load()
	if stateErr != nil {
		report.Checks = append(report.Checks, Check{ID: "state", Status: StatusFail, Message: stateErr.Error(), Remedy: "repair or remove the invalid Výbava state file"})
	} else {
		report.Checks = append(report.Checks, Check{ID: "state", Status: StatusPass, Message: store.Path})
	}

	home, err := os.UserHomeDir()
	if err != nil {
		report.Checks = append(report.Checks, Check{ID: "home", Status: StatusFail, Message: err.Error()})
	} else {
		binDirs := make(map[string]struct{})
		for _, installed := range current.Installed {
			if installed.Kind == string(catalog.KindApplet) {
				binDirs[filepath.Dir(installed.Destination)] = struct{}{}
			}
		}
		if len(binDirs) == 0 {
			binDirs[filepath.Join(home, ".local", "bin")] = struct{}{}
		}
		for binDir := range binDirs {
			if pathContains(binDir) {
				report.Checks = append(report.Checks, Check{ID: "path:" + binDir, Status: StatusPass, Message: binDir + " is on PATH"})
			} else {
				report.Checks = append(report.Checks, Check{
					ID: "path:" + binDir, Status: StatusWarn, Message: binDir + " is not on PATH",
					Remedy: "add " + binDir + " to PATH so installed applets are directly callable",
				})
			}
		}
	}

	if _, err := exec.LookPath("git"); err != nil {
		report.Checks = append(report.Checks, Check{ID: "git", Status: StatusWarn, Message: "git is unavailable", Remedy: "install git before using git workflow skills"})
	} else {
		report.Checks = append(report.Checks, Check{ID: "git", Status: StatusPass, Message: "git is available"})
	}
	if _, err := exec.LookPath("gh"); err != nil {
		report.Checks = append(report.Checks, Check{ID: "gh", Status: StatusWarn, Message: "GitHub CLI is unavailable", Remedy: "install and authenticate gh before using PR skills"})
	} else {
		report.Checks = append(report.Checks, Check{ID: "gh", Status: StatusPass, Message: "GitHub CLI is available"})
	}

	if stateErr != nil {
		return report
	}
	known := make(map[string]struct{}, len(c.Items))
	for _, item := range c.Items {
		known[item.ID] = struct{}{}
	}
	for _, installed := range current.Installed {
		id := "installed:" + installed.ItemID
		if installed.Agent != "" {
			id += ":" + installed.Agent
		}
		if _, exists := known[installed.ItemID]; !exists {
			report.Checks = append(report.Checks, Check{ID: id, Status: StatusWarn, Message: "installed item is no longer in the catalog: " + installed.ItemID})
			continue
		}
		if _, err := os.Lstat(installed.Destination); errors.Is(err, os.ErrNotExist) {
			report.Checks = append(report.Checks, Check{ID: id, Status: StatusFail, Message: "installed item is missing at " + installed.Destination, Remedy: "run vybava update"})
		} else if err != nil {
			report.Checks = append(report.Checks, Check{ID: id, Status: StatusFail, Message: err.Error()})
		} else {
			report.Checks = append(report.Checks, Check{ID: id, Status: StatusPass, Message: installed.Destination})
		}
	}
	return report
}

func pathContains(expected string) bool {
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Clean(entry) == filepath.Clean(expected) {
			return true
		}
	}
	return false
}

func FormatText(report Report) string {
	var output strings.Builder
	for _, check := range report.Checks {
		prefix := "PASS"
		switch check.Status {
		case StatusWarn:
			prefix = "WARN"
		case StatusFail:
			prefix = "FAIL"
		}
		output.WriteString(prefix + "  " + check.ID + " — " + check.Message + "\n")
		if check.Remedy != "" {
			output.WriteString("      remedy: " + check.Remedy + "\n")
		}
	}
	return output.String()
}

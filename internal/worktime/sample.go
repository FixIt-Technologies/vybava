// Package worktime provides metadata-only workstation activity observations.
// Window titles are opt-in transient context, never stored by this package.
package worktime

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Sample struct {
	At           time.Time `json:"at"`
	Application  string    `json:"application"`
	IdleSeconds  float64   `json:"idleSeconds"`
	Source       string    `json:"source"`
	WindowTitle  string    `json:"windowTitle,omitempty"`
	ContextError string    `json:"contextError,omitempty"`
}

type Exec func(context.Context, string, ...string) ([]byte, error)

var idlePattern = regexp.MustCompile(`"HIDIdleTime"\s*=\s*(\d+)`)

// DarwinSample keeps subprocess injection available for Linux fixture tests.
func DarwinSample(ctx context.Context, run Exec, now time.Time) (Sample, error) {
	raw, err := run(ctx, "/usr/sbin/ioreg", "-c", "IOHIDSystem", "-d", "4")
	if err != nil {
		return Sample{}, fmt.Errorf("read macOS idle time: %w", err)
	}
	m := idlePattern.FindSubmatch(raw)
	if len(m) != 2 {
		return Sample{}, fmt.Errorf("macOS did not expose HIDIdleTime")
	}
	ns, err := strconv.ParseUint(string(m[1]), 10, 64)
	if err != nil {
		return Sample{}, fmt.Errorf("parse macOS idle time: %w", err)
	}
	// AppKit exposes bundle identity without enumerating windows or their text.
	raw, err = run(ctx, "/usr/bin/osascript", "-l", "JavaScript", "-e", `ObjC.import('AppKit'); JSON.stringify({application: ObjC.unwrap($.NSWorkspace.sharedWorkspace.frontmostApplication.bundleIdentifier)})`)
	if err != nil {
		return Sample{}, fmt.Errorf("read macOS foreground application: %w", err)
	}
	var app struct {
		Application string `json:"application"`
	}
	if err := json.Unmarshal(raw, &app); err != nil {
		return Sample{}, fmt.Errorf("decode foreground application: %w", err)
	}
	if strings.TrimSpace(app.Application) == "" {
		return Sample{}, fmt.Errorf("macOS foreground application is unavailable")
	}
	return Sample{At: now.UTC(), Application: app.Application, IdleSeconds: float64(ns) / 1e9, Source: "macos-foreground"}, nil
}

func NativeSample(ctx context.Context) (Sample, error) {
	if runtime.GOOS != "darwin" {
		return Sample{}, fmt.Errorf("native foreground capture is supported on macOS; this %s host has no human activity source", runtime.GOOS)
	}
	return DarwinSample(ctx, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).Output()
	}, time.Now())
}

// WindowContext reads only the foreground window title. Consumers must match
// locally and discard the raw title; no transcript, keystroke or URL is read.
func WindowContext(ctx context.Context, sample Sample) Sample {
	return DarwinWindowContext(ctx, sample, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).Output()
	})
}

// DarwinWindowContext exposes the subprocess seam for permission-failure tests.
func DarwinWindowContext(ctx context.Context, sample Sample, run Exec) Sample {
	sample.WindowTitle = ""
	sample.ContextError = ""
	raw, err := run(ctx, "/usr/bin/osascript", "-e", `with timeout of 2 seconds
tell application "System Events" to get name of front window of first application process whose frontmost is true
end timeout`)
	if err != nil {
		sample.ContextError = "Foreground window context unavailable; allow Accessibility for the collector to enable task matching."
		return sample
	}
	sample.WindowTitle = strings.TrimSpace(string(raw))
	return sample
}

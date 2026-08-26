package claudesweep

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// LaunchdLabel identifies the daily sweep LaunchAgent.
const LaunchdLabel = "com.vybava.claude-sweep"

func launchdPaths() (plistPath, logPath string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("resolve home: %w", err)
	}
	plistPath = filepath.Join(home, "Library", "LaunchAgents", LaunchdLabel+".plist")
	logPath = filepath.Join(home, "Library", "Logs", "claude-sweep.log")
	return plistPath, logPath, nil
}

// RenderLaunchdPlist renders the LaunchAgent that runs the sweep daily at
// 06:00 and appends stdout+stderr to logPath.
func RenderLaunchdPlist(program string, args []string, logPath string) string {
	var arguments strings.Builder
	for _, argument := range append([]string{program}, args...) {
		arguments.WriteString("    <string>" + xmlEscape(argument) + "</string>\n")
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + LaunchdLabel + `</string>
  <key>ProgramArguments</key>
  <array>
` + arguments.String() + `  </array>
  <key>StartCalendarInterval</key>
  <dict>
    <key>Hour</key>
    <integer>6</integer>
    <key>Minute</key>
    <integer>0</integer>
  </dict>
  <key>StandardOutPath</key>
  <string>` + xmlEscape(logPath) + `</string>
  <key>StandardErrorPath</key>
  <string>` + xmlEscape(logPath) + `</string>
</dict>
</plist>
`
}

func xmlEscape(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return replacer.Replace(value)
}

// InstallLaunchd writes and (re)loads the daily sweep LaunchAgent. It always
// points the job at the real vybava executable and dispatches through the
// `claude-sweep` subcommand, so it works no matter how the installer linked
// the applet.
func InstallLaunchd(run CommandRunner, age string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", errors.New("launchd scheduling is only available on macOS")
	}
	if _, err := ParseAge(age); err != nil {
		return "", err
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve current executable: %w", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", fmt.Errorf("resolve executable links: %w", err)
	}
	plistPath, logPath, err := launchdPaths()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return "", fmt.Errorf("create LaunchAgents directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return "", fmt.Errorf("create Logs directory: %w", err)
	}
	content := RenderLaunchdPlist(executable, []string{"claude-sweep", "--kill", "--age", age}, logPath)
	if err := os.WriteFile(plistPath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write plist: %w", err)
	}
	uid := os.Getuid()
	_, _ = run("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", uid, LaunchdLabel)) // ignore: not loaded yet
	if _, err := run("launchctl", "bootstrap", fmt.Sprintf("gui/%d", uid), plistPath); err != nil {
		return plistPath, fmt.Errorf("launchctl bootstrap: %w", err)
	}
	return plistPath, nil
}

// UninstallLaunchd unloads and removes the LaunchAgent. Missing pieces are
// not an error, so it is safe to run twice.
func UninstallLaunchd(run CommandRunner) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", errors.New("launchd scheduling is only available on macOS")
	}
	plistPath, _, err := launchdPaths()
	if err != nil {
		return "", err
	}
	_, _ = run("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), LaunchdLabel)) // ignore: not loaded
	if err := os.Remove(plistPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return plistPath, fmt.Errorf("remove plist: %w", err)
	}
	return plistPath, nil
}

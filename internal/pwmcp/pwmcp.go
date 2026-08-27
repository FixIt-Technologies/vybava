// Package pwmcp keeps every Playwright MCP server on a workstation pinned to
// one @playwright/mcp version, sharing one browser registry that lives outside
// the OS cache directory.
//
// Three failure modes motivate it, all of which cost a 150 MB Chromium download
// on a machine that runs many agent sessions in parallel:
//
//   - Cache sweeps. ~/Library/Caches is the first place any "free up disk"
//     routine reaches, and ms-playwright is pure re-downloadable weight. The
//     registry moves under ~/.local/share so a sweep can never cost a download.
//   - Version fan-out. "@playwright/mcp@latest" resolves separately per project,
//     per worktree and per bunx temp directory, and every distinct
//     playwright-core demands its own Chromium revision.
//   - Silent lock contention. Two concurrent installs meet on Playwright's own
//     __dirlock, where the loser blocks with no output and no timeout. pwmcp
//     takes its own lock first, so the loser is told who holds it.
package pwmcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// PinnedVersion is the single @playwright/mcp version this workstation runs.
// Bumping it here, deliberately, is the only supported way to move.
const PinnedVersion = "0.0.78"

// DefaultBrowsers is the set installed unless the caller asks for more. The MCP
// server drives Chromium; pulling Firefox and WebKit too would cost ~300 MB that
// nothing on this workstation opens.
var DefaultBrowsers = []string{"chromium", "chromium-headless-shell", "ffmpeg"}

// InstallTimeout bounds a dependency or browser install. Generous, because a
// cold Chromium download on a slow link is minutes; bounded, because the failure
// this package exists to fix is an install that hangs forever.
const InstallTimeout = 20 * time.Minute

// Config locates the pinned package tree and the shared browser registry.
type Config struct {
	// Root holds one subdirectory per pinned version, so bumping the pin is
	// additive and rolling back costs nothing.
	Root string
	// Browsers becomes PLAYWRIGHT_BROWSERS_PATH for every process pwmcp starts.
	Browsers string
	// Version is the @playwright/mcp version to install and run.
	Version string
	// Runtime is the JavaScript runtime used to install and to serve.
	Runtime string
}

// Runner executes one install step. Tests substitute it; production passes
// ExecRunner.
type Runner func(ctx context.Context, dir string, env []string, name string, args ...string) error

// ExecRunner runs an install step for real.
//
// Both streams go to stderr on purpose: when pwmcp is serving, stdout is the MCP
// JSON-RPC transport, and one line of npm progress on it corrupts the session.
func ExecRunner(ctx context.Context, dir string, env []string, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	command.Env = env
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// DefaultConfig resolves the workstation layout, honouring the overrides an
// operator may already have in the environment.
func DefaultConfig() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("resolve home directory: %w", err)
	}
	base := filepath.Join(home, ".local", "share", "vybava")
	return Config{
		Root:     firstSet(os.Getenv("VYBAVA_PWMCP_ROOT"), filepath.Join(base, "playwright-mcp")),
		Browsers: firstSet(os.Getenv("VYBAVA_PWMCP_BROWSERS"), os.Getenv("PLAYWRIGHT_BROWSERS_PATH"), filepath.Join(base, "playwright-browsers")),
		Version:  firstSet(os.Getenv("VYBAVA_PWMCP_VERSION"), PinnedVersion),
		Runtime:  firstSet(os.Getenv("VYBAVA_PWMCP_RUNTIME"), "bun"),
	}, nil
}

// PackageDir is the version-scoped install tree.
func (c Config) PackageDir() string { return filepath.Join(c.Root, c.Version) }

// ServerCLI is the pinned MCP entrypoint.
func (c Config) ServerCLI() string {
	return filepath.Join(c.PackageDir(), "node_modules", "@playwright", "mcp", "cli.js")
}

// CoreDir is the playwright-core the pinned server resolved to. Its
// browsers.json is the authority on which browser revisions are required.
func (c Config) CoreDir() string {
	return filepath.Join(c.PackageDir(), "node_modules", "playwright-core")
}

// CoreCLI drives browser installation.
func (c Config) CoreCLI() string { return filepath.Join(c.CoreDir(), "cli.js") }

// Env returns the child environment with the browser registry forced onto the
// shared path. An inherited PLAYWRIGHT_BROWSERS_PATH is dropped rather than
// respected: a project shell exporting its own is precisely how a second
// registry — and a second download of the same Chromium — comes into existence.
func (c Config) Env() []string {
	parent := os.Environ()
	env := make([]string, 0, len(parent)+1)
	for _, entry := range parent {
		if strings.HasPrefix(entry, "PLAYWRIGHT_BROWSERS_PATH=") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "PLAYWRIGHT_BROWSERS_PATH="+c.Browsers)
}

// Installed reports whether the pinned server is already on disk.
func (c Config) Installed() bool { return exists(c.ServerCLI()) }

// Ensure installs the pinned server if it is missing. Concurrent callers are
// safe: the loser waits on pwmcp's lock, which reports its holder, instead of
// racing into Playwright's silent one.
func (c Config) Ensure(ctx context.Context, run Runner, progress io.Writer) error {
	if c.Installed() {
		return nil
	}
	if _, err := exec.LookPath(c.Runtime); err != nil {
		return fmt.Errorf("%s is required to install the pinned Playwright MCP: %w", c.Runtime, err)
	}
	release, err := c.lock(ctx, progress)
	if err != nil {
		return err
	}
	defer release()
	// Whoever held the lock may have just done the work.
	if c.Installed() {
		return nil
	}
	if err := os.MkdirAll(c.PackageDir(), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", c.PackageDir(), err)
	}
	if err := os.WriteFile(filepath.Join(c.PackageDir(), "package.json"), c.manifest(), 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	fmt.Fprintf(progress, "pwmcp: installing @playwright/mcp@%s into %s\n", c.Version, c.PackageDir())
	installCtx, cancel := context.WithTimeout(ctx, InstallTimeout)
	defer cancel()
	if err := run(installCtx, c.PackageDir(), c.Env(), c.Runtime, "install"); err != nil {
		return err
	}
	if !c.Installed() {
		return fmt.Errorf("install completed but %s is missing", c.ServerCLI())
	}
	return nil
}

// EnsureBrowsers downloads only the revisions the pinned playwright-core asks
// for and that are not already in the shared registry.
func (c Config) EnsureBrowsers(ctx context.Context, run Runner, progress io.Writer, names []string) error {
	if len(names) == 0 {
		names = DefaultBrowsers
	}
	missing, err := c.Missing(names)
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}
	release, err := c.lock(ctx, progress)
	if err != nil {
		return err
	}
	defer release()
	if missing, err = c.Missing(names); err != nil || len(missing) == 0 {
		return err
	}
	if err := os.MkdirAll(c.Browsers, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", c.Browsers, err)
	}
	fmt.Fprintf(progress, "pwmcp: downloading %s into %s\n", strings.Join(missing, ", "), c.Browsers)
	installCtx, cancel := context.WithTimeout(ctx, InstallTimeout)
	defer cancel()
	args := append([]string{c.CoreCLI(), "install"}, missing...)
	return run(installCtx, c.PackageDir(), c.Env(), c.Runtime, args...)
}

// Serve runs the pinned MCP server with this process's stdio as its transport.
func (c Config) Serve(ctx context.Context, args []string) error {
	if !c.Installed() {
		return fmt.Errorf("pinned server missing at %s: run `pwmcp install` first", c.ServerCLI())
	}
	command := exec.CommandContext(ctx, c.Runtime, append([]string{c.ServerCLI()}, args...)...)
	command.Env = c.Env()
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	// A killed MCP server should get the chance to close its browser; SIGKILL
	// would strand the Chromium process and its profile directory.
	command.Cancel = func() error { return command.Process.Signal(os.Interrupt) }
	command.WaitDelay = 10 * time.Second
	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr
		}
		return fmt.Errorf("run %s: %w", c.ServerCLI(), err)
	}
	return nil
}

func (c Config) manifest() []byte {
	return []byte(fmt.Sprintf(`{
  "name": "vybava-playwright-mcp",
  "private": true,
  "description": "Pinned Playwright MCP shared by every project on this workstation. Managed by vybava pwmcp; move the pin in internal/pwmcp, not here.",
  "dependencies": {
    "@playwright/mcp": "%s"
  }
}
`, c.Version))
}

func firstSet(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

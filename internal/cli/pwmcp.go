package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/FixIt-Technologies/vybava/internal/pwmcp"
	"github.com/spf13/cobra"
)

func (rt *runtime) pwmcpApplet() *cobra.Command {
	command := rt.pwmcpCommand("pwmcp")
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetOut(rt.stdout)
	command.SetErr(rt.stderr)
	command.PersistentFlags().BoolVar(&rt.json, "json", false, "emit stable JSON output")
	return command
}

func (rt *runtime) pwmcpCommand(use string) *cobra.Command {
	command := &cobra.Command{
		Use:   use,
		Short: "Run every Playwright MCP server from one pinned install and one shared browser registry",
		Long: "Point every project's MCP config at `pwmcp serve` so the workstation resolves\n" +
			"one @playwright/mcp version instead of one per project, per worktree and per\n" +
			"bunx temp directory. The browser registry moves out of the OS cache directory,\n" +
			"where disk-cleanup routines delete it and charge the next session a fresh\n" +
			"150 MB download. Profiles stay isolated per server; the binary stays shared.",
	}
	command.AddCommand(
		rt.pwmcpServeCommand(), rt.pwmcpInstallCommand(),
		rt.pwmcpStatusCommand(), rt.pwmcpPruneCommand(), rt.pwmcpEnvCommand(),
	)
	return command
}

func (rt *runtime) pwmcpServeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "serve [-- server-args...]",
		Short: "Install on first use, then run the pinned MCP server over stdio",
		Long: "Every argument is forwarded to @playwright/mcp untouched, so this is a drop-in\n" +
			"replacement for `npx @playwright/mcp@latest`. --isolated is prepended unless the\n" +
			"arguments already choose a profile (--isolated, --user-data-dir, --config,\n" +
			"--storage-state) or pass --shared-profile.",
		// The server owns a large and moving flag surface. Parsing any of it here
		// would mean re-declaring it on every upstream release, and silently
		// rejecting flags in between.
		DisableFlagParsing: true,
		RunE: func(_ *cobra.Command, args []string) error {
			config, err := pwmcp.DefaultConfig()
			if err != nil {
				return err
			}
			// SIGINT and SIGTERM belong to the child, which needs them to close
			// its browser; ignoring them here would strand a Chromium process.
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := config.Ensure(ctx, pwmcp.ExecRunner, rt.stderr); err != nil {
				return err
			}
			if err := config.EnsureBrowsers(ctx, pwmcp.ExecRunner, rt.stderr, nil); err != nil {
				return err
			}
			forwarded, isolated := pwmcp.Isolate(args)
			if isolated {
				fmt.Fprintln(rt.stderr, "pwmcp: profile isolated (pass --shared-profile to opt out)")
			}
			return config.Serve(ctx, forwarded)
		},
	}
}

func (rt *runtime) pwmcpInstallCommand() *cobra.Command {
	var browsers []string
	command := &cobra.Command{
		Use:   "install",
		Short: "Install the pinned server and the browser revisions it needs",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			config, err := pwmcp.DefaultConfig()
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := config.Ensure(ctx, pwmcp.ExecRunner, rt.stderr); err != nil {
				return err
			}
			if err := config.EnsureBrowsers(ctx, pwmcp.ExecRunner, rt.stderr, browsers); err != nil {
				return err
			}
			report := config.Status(browsers)
			if rt.json {
				return writeJSON(rt.stdout, report)
			}
			_, err = fmt.Fprint(rt.stdout, pwmcp.FormatText(report))
			return err
		},
	}
	command.Flags().StringSliceVar(&browsers, "browser", nil,
		"browsers to install (default "+strings.Join(pwmcp.DefaultBrowsers, ",")+")")
	return command
}

func (rt *runtime) pwmcpStatusCommand() *cobra.Command {
	var browsers []string
	command := &cobra.Command{
		Use:   "status",
		Short: "Report the pin, the registry location, and any orphaned revisions",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			config, err := pwmcp.DefaultConfig()
			if err != nil {
				return err
			}
			report := config.Status(browsers)
			if rt.json {
				if err := writeJSON(rt.stdout, report); err != nil {
					return err
				}
			} else if _, err := fmt.Fprint(rt.stdout, pwmcp.FormatText(report)); err != nil {
				return err
			}
			if len(report.Warnings) > 0 {
				return ErrFindings
			}
			return nil
		},
	}
	command.Flags().StringSliceVar(&browsers, "browser", nil, "browsers to check")
	return command
}

func (rt *runtime) pwmcpPruneCommand() *cobra.Command {
	var dryRun bool
	command := &cobra.Command{
		Use:   "prune",
		Short: "Delete browser revisions no installed pin still needs",
		Long: "The safe alternative to wiping the registry: that frees the same disk and then\n" +
			"charges every later session a fresh download. Revisions still referenced by any\n" +
			"installed pin are kept, so rolling the pin back stays free.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			config, err := pwmcp.DefaultConfig()
			if err != nil {
				return err
			}
			removed, err := config.Prune(dryRun)
			if err != nil {
				return err
			}
			if rt.json {
				return writeJSON(rt.stdout, map[string]any{"dry_run": dryRun, "removed": removed})
			}
			verb := "removed"
			if dryRun {
				verb = "would remove"
			}
			for _, path := range removed {
				if _, err := fmt.Fprintf(rt.stdout, "%s %s\n", verb, path); err != nil {
					return err
				}
			}
			_, err = fmt.Fprintf(rt.stdout, "pwmcp: %d orphaned revision(s)\n", len(removed))
			return err
		},
	}
	command.Flags().BoolVar(&dryRun, "dry-run", false, "list orphans without deleting them")
	return command
}

func (rt *runtime) pwmcpEnvCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "env",
		Short: "Print the shell export that points direct Playwright runs at the shared registry",
		Long: "A project running its own `playwright test` bypasses pwmcp entirely and would\n" +
			"otherwise build a second registry in the OS cache. Eval this in a shell profile\n" +
			"so those runs share the same browsers.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			config, err := pwmcp.DefaultConfig()
			if err != nil {
				return err
			}
			if rt.json {
				return writeJSON(rt.stdout, map[string]string{"PLAYWRIGHT_BROWSERS_PATH": config.Browsers})
			}
			_, err = fmt.Fprintf(rt.stdout, "export PLAYWRIGHT_BROWSERS_PATH=%q\n", config.Browsers)
			return err
		},
	}
}

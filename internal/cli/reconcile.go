package cli

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/FixIt-Technologies/vybava/internal/reconcile"
	"github.com/spf13/cobra"
)

func (rt *runtime) reconcileApplet() *cobra.Command {
	command := rt.reconcileCommand("reconcile")
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetOut(rt.stdout)
	command.SetErr(rt.stderr)
	command.PersistentFlags().BoolVar(&rt.json, "json", false, "emit stable JSON output")
	return command
}

func (rt *runtime) reconcileCommand(use string) *cobra.Command {
	var manifestPath string
	command := &cobra.Command{
		Use:   use,
		Short: "Pull-based GitOps: converge a box to its infra repo's merged main",
		Long: "Reads reconcile.yaml (the per-box manifest), pulls origin/main into the\n" +
			"clone and converges the mapped files. Unknown subcommands are rejected.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	command.PersistentFlags().StringVar(&manifestPath, "manifest", "reconcile.yaml", "per-box manifest (clone root)")

	// Under --json stdout carries ONLY the JSON document: the engine's tick log
	// moves to stderr so `vybava reconcile status --json | jq` never sees a
	// "[ts] pending converge …" line ahead of the document.
	logOut := func() io.Writer {
		if rt.json {
			return rt.stderr
		}
		return rt.stdout
	}
	engine := func() (*reconcile.Engine, error) {
		m, err := reconcile.Load(manifestPath)
		if err != nil {
			return nil, err
		}
		return &reconcile.Engine{M: m, Version: rt.version, Out: logOut(), Err: rt.stderr}, nil
	}
	emit := func(res reconcile.Result) error {
		if rt.json {
			return writeJSON(rt.stdout, res)
		}
		return nil
	}

	run := &cobra.Command{
		Use:   "run",
		Short: "Cron entry point: pull, sweep, hooks, alert (report or converge per the mode file)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			e, err := engine()
			if err != nil {
				return err
			}
			res, runErr := e.Run()
			if err := emit(res); err != nil {
				return err
			}
			return runErr
		},
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Read-only drift summary: no fetch, no state, no alerts",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			e, err := engine()
			if err != nil {
				return err
			}
			if rt.json {
				rep, err := e.StatusReport(20)
				if err != nil {
					return err
				}
				return writeJSON(rt.stdout, rep)
			}
			rep, err := e.StatusReport(5)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(rt.stdout, "%s (%s): %s — mode %s, HEAD %s, last-good %s%s\n",
				rep.Repo, rep.HostLabel, rep.Sync, rep.Mode, shortSHA(rep.Commit), orDash(shortSHA(rep.LastGood)), pinNote(rep.Pin))
			return err
		},
	}
	force := &cobra.Command{
		Use:   "force <repo-relative-path>",
		Short: "Stamp the repo version of ONE held file over the live one (backed up first)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			e, err := engine()
			if err != nil {
				return err
			}
			if err := e.Force(args[0]); err != nil {
				return err
			}
			if rt.json {
				return writeJSON(rt.stdout, map[string]string{"status": "ok", "path": args[0]})
			}
			return nil
		},
	}
	var unpin bool
	rollback := &cobra.Command{
		Use:   "rollback [<sha>]",
		Short: "Re-converge the box to the last-good (or given) commit and pin it there",
		Long: "Resets the clone to the target commit, converges (HELD files stay HELD)\n" +
			"and pins the box: `run` follows the pin instead of origin/main until\n" +
			"`rollback --unpin`.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			e, err := engine()
			if err != nil {
				return err
			}
			sha := ""
			if len(args) == 1 {
				sha = args[0]
			}
			res, rbErr := e.Rollback(sha, unpin)
			if err := emit(res); err != nil {
				return err
			}
			return rbErr
		},
	}
	rollback.Flags().BoolVar(&unpin, "unpin", false, "clear the pin so the next tick follows origin/main again")

	var listen string
	var hub bool
	var interval time.Duration
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Read-only status page + /status.json on the WireGuard address (--hub aggregates the estate)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := reconcile.Load(manifestPath)
			if err != nil {
				return err
			}
			if listen == "" {
				listen = m.Serve.Listen
			}
			if listen == "" {
				return fmt.Errorf("serve: no listen address (--listen or serve.listen in the manifest)")
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if hub {
				if len(m.Serve.Hosts) == 0 {
					return fmt.Errorf("serve --hub: serve.hosts is empty in the manifest")
				}
				h := &reconcile.Hub{Hosts: m.Serve.Hosts, Interval: interval}
				go h.Poll(ctx)
				return reconcile.Serve(ctx, listen, h.Handler(), rt.stderr)
			}
			e := &reconcile.Engine{M: m, Version: rt.version, Out: rt.stdout, Err: rt.stderr}
			return reconcile.Serve(ctx, listen, (&reconcile.Server{Engine: e}).Handler(), rt.stderr)
		},
	}
	serve.Flags().StringVar(&listen, "listen", "", "<wireguard-ip>:<port> to bind (defaults to serve.listen)")
	serve.Flags().BoolVar(&hub, "hub", false, "poll serve.hosts and render one estate page")
	serve.Flags().DurationVar(&interval, "interval", 30*time.Second, "hub poll interval")

	command.AddCommand(run, status, force, rollback, serve)
	return command
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func pinNote(pin string) string {
	if pin == "" {
		return ""
	}
	return ", PINNED to " + shortSHA(pin)
}

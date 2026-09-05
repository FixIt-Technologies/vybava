package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/FixIt-Technologies/vybava/internal/handoffs"
	"github.com/spf13/cobra"
)

func (rt *runtime) handoffsApplet() *cobra.Command {
	command := rt.handoffsCommand("handoffs")
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetOut(rt.stdout)
	command.SetErr(rt.stderr)
	command.PersistentFlags().BoolVar(&rt.json, "json", false, "emit stable JSON output")
	return command
}

func (rt *runtime) handoffsCommand(use string) *cobra.Command {
	command := &cobra.Command{
		Use:   use,
		Short: "Handoff ledger upkeep — decide which open handoffs are still live",
	}
	command.AddCommand(rt.handoffsReconcileCommand())
	return command
}

func (rt *runtime) handoffsReconcileCommand() *cobra.Command {
	var (
		home      string
		project   string
		apply     bool
		staleDays int
	)
	command := &cobra.Command{
		Use:   "reconcile",
		Short: "Judge every open handoff by its branches and PRs; archive the dead ones with --apply",
		Long: `Reads each open/in-progress handoff's Branch line and PR references, asks
git (no fetch) and gh whether any of them is still alive, and prints a verdict
per handoff: live, dead or unknown. Dry run by default; --apply flips dead
handoffs to status: done (the work merged) or abandoned and moves them under
<project>/archive/.
Unknown is never archived and nothing is ever deleted.`,
		Example: `  handoffs reconcile                       # dry run over ~/.claude/handoffs
  handoffs reconcile --project fixit       # one project
  handoffs reconcile --apply               # archive the dead ones
  handoffs reconcile --json | jq .summary`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			userHome, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			home, err = expandHome(home)
			if err != nil {
				return err
			}
			env := handoffs.Env{
				Home: home, UserHome: userHome, Projects: filepath.Join(userHome, "Work", "Projects"), Now: time.Now(),
				Registry: func() ([]byte, error) {
					return os.ReadFile(filepath.Join(userHome, ".claude", "docs", "timesheet-repo-registry.md"))
				},
				Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
					return exec.CommandContext(ctx, name, args...).CombinedOutput()
				},
			}
			report, err := handoffs.Reconcile(cmd.Context(), env, handoffs.Options{Project: project, Apply: apply, StaleDays: staleDays})
			if rt.json {
				if writeErr := writeJSON(rt.stdout, report); writeErr != nil {
					return writeErr
				}
				return err
			}
			fmt.Fprintf(rt.stdout, "%-40s %-22s %-8s %s\n", "SLUG", "PROJECT", "VERDICT", "REASON")
			for _, item := range report.Items {
				reason := item.Reason
				if item.ArchiveStatus != "" {
					reason += " → " + item.ArchiveStatus
				}
				fmt.Fprintf(rt.stdout, "%-40s %-22s %-8s %s\n", trunc(item.Slug, 40), trunc(item.Project, 22), item.Verdict, reason)
			}
			for _, item := range report.Items {
				if item.Archived != "" {
					fmt.Fprintf(rt.stdout, "moved %s → %s\n", item.Path, item.Archived)
				}
			}
			s := report.Summary
			fmt.Fprintf(rt.stdout, "live %d · dead %d · unknown %d · archived %d", s.Live, s.Dead, s.Unknown, s.Archived)
			if !apply && s.Dead > 0 {
				fmt.Fprint(rt.stdout, "   (dry run — pass --apply to archive the dead ones)")
			}
			fmt.Fprintln(rt.stdout)
			return err
		},
	}
	command.Flags().StringVar(&home, "home", "~/.claude/handoffs", "handoffs home")
	command.Flags().StringVar(&project, "project", "", "only this project slug")
	command.Flags().BoolVar(&apply, "apply", false, "archive dead handoffs (status: done when merged, else abandoned; moved under archive/)")
	command.Flags().IntVar(&staleDays, "stale-days", handoffs.DefaultStaleDays, "a handoff with no branch/PR evidence is dead after this many untouched days")
	return command
}

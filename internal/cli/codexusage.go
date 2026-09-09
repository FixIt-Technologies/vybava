package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/henderson-tech/vybava/internal/codexusage"
	"github.com/spf13/cobra"
)

func (rt *runtime) codexusageApplet() *cobra.Command {
	command := rt.codexusageCommand("codexusage")
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetOut(rt.stdout)
	command.SetErr(rt.stderr)
	command.PersistentFlags().BoolVar(&rt.json, "json", false, "emit stable JSON output")
	return command
}

func (rt *runtime) codexusageCommand(use string) *cobra.Command {
	var (
		since string
		top   int
		idle  bool
	)
	command := &cobra.Command{
		Use:   use,
		Short: "Explain where the Codex plan limit went — per thread, with the runway left",
		Long: `Reads ~/.codex rollouts and reports which Codex threads spent the account's
plan limit, how much of it is left, and how long it lasts at the measured burn
rate. Live threads are matched to their PID and terminal so an expensive one
can actually be closed.

Codex meters TOTAL tokens — cache reads included, not just fresh input — so a
resumed thread carrying 150k of context bills that context on every single
call. Those threads are marked "resumed"; they are almost always the answer.

The allowance is derived, not published: percentage consumed is regressed
against tokens observed in the same window.`,
		Example: `  codexusage                  # today, per thread
  codexusage --since 7am      # since you sat down
  codexusage --since 3h       # the last three hours
  codexusage --idle           # also list parked live threads
  codexusage --json           # stable output for scripts`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			now := time.Now()
			start, err := codexusage.ParseSince(since, now)
			if err != nil {
				return err
			}
			env := codexusage.Env{
				Home: home, Now: now,
				Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
					return exec.CommandContext(ctx, name, args...).Output()
				},
			}
			report, err := codexusage.Run(cmd.Context(), env, codexusage.Options{Since: start, Top: top, IncludeIdle: idle})
			if err != nil {
				return err
			}
			if rt.json {
				return writeJSON(rt.stdout, report)
			}
			rt.codexusageReport(report, home)
			return nil
		},
	}
	command.Flags().StringVar(&since, "since", "today", "window start: 7am, 3h, today, yesterday, week, or 2006-01-02T15:04")
	command.Flags().IntVar(&top, "top", 12, "show at most this many spending threads (0 = all)")
	command.Flags().BoolVar(&idle, "idle", false, "also list live threads that spent nothing in the window")
	return command
}

func (rt *runtime) codexusageReport(report codexusage.Report, home string) {
	fmt.Fprintf(rt.stdout, "CODEX USAGE  %s → %s  (%s)\n",
		report.Since.Format("Mon 15:04"), report.Until.Format("15:04"),
		codexusage.Duration(report.Until.Sub(report.Since)))

	if report.Calls == 0 {
		fmt.Fprintln(rt.stdout, "\nno model calls in this window")
		rt.codexusageWarnings(report)
		return
	}

	for _, plan := range report.Plans {
		rt.codexusagePlan(plan, report.Until)
	}

	total := report.Usage
	fmt.Fprintf(rt.stdout, "\n%s tokens   %s cached (%.1f%%)   %s fresh   %s out   %d calls\n",
		codexusage.Human(total.Total()), codexusage.Human(total.Cached), total.CacheRate(),
		codexusage.Human(total.Fresh()), codexusage.Human(total.Output), report.Calls)

	fmt.Fprintf(rt.stdout, "\n%-7s %9s %5s %6s %9s  %-36s %s\n", "PID", "TOKENS", "%LIM", "CALLS", "CTX/CALL", "THREAD", "WHERE")
	resumed := false
	for _, session := range report.Sessions {
		label := session.Name
		if session.Resumed() {
			label, resumed = "↩ "+label, true
		}
		fmt.Fprintf(rt.stdout, "%-7s %9s %5.0f %6d %9s  %-36s %s\n",
			codexusagePID(session), codexusage.Human(session.Usage.Total()), session.Points, session.Calls,
			codexusage.Human(session.AvgContext), trunc(label, 36), shortenHome(session.CWD, home))
	}
	if resumed {
		fmt.Fprintln(rt.stdout, "\n↩ resumed an older transcript — every call re-bills the context it inherited; /compact or start fresh")
	}

	if len(report.Idle) > 0 {
		fmt.Fprintf(rt.stdout, "\nPARKED — live, spent nothing in this window (%d)\n", len(report.Idle))
		for _, session := range report.Idle {
			fmt.Fprintf(rt.stdout, "%-7s %-8s %-36s %s\n",
				codexusagePID(session), session.TTY, trunc(session.Name, 36), shortenHome(session.CWD, home))
		}
	}
	rt.codexusageWarnings(report)
}

func (rt *runtime) codexusagePlan(plan codexusage.PlanReport, now time.Time) {
	fmt.Fprintf(rt.stdout, "\n%-9s %3.0f%%  %s  resets in %s (%s)\n",
		plan.Plan, plan.EndPercent, codexusageBar(plan.EndPercent),
		codexusage.Duration(plan.ResetsAt.Sub(now)), plan.ResetsAt.Format("Mon 2 Jan 15:04"))

	if plan.Points <= 0 {
		return
	}
	detail := fmt.Sprintf("          +%.0fpp in %s · %s/pp · ~%s allowance · %.1f pp/h",
		plan.Points, codexusage.Duration(plan.Elapsed),
		codexusage.Human(plan.TokensPerPoint()), codexusage.Human(plan.Allowance()), plan.PointsPerHour())
	if runway, ok := plan.Runway(); ok {
		detail += fmt.Sprintf(" · empty in ~%s", codexusage.Duration(runway))
	} else if plan.Exhausted {
		detail += " · EXHAUSTED"
	}
	fmt.Fprintln(rt.stdout, detail)
}

func (rt *runtime) codexusageWarnings(report codexusage.Report) {
	for _, warning := range report.Warnings {
		fmt.Fprintln(rt.stderr, "note:", warning)
	}
}

func codexusagePID(session codexusage.SessionReport) string {
	if session.PID == 0 {
		return "—"
	}
	return fmt.Sprintf("%d", session.PID)
}

func codexusageBar(percent float64) string {
	const width = 20
	filled := int(percent/100*width + 0.5)
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

func shortenHome(path, home string) string {
	if home != "" && strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

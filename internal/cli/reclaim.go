package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/FixIt-Technologies/vybava/internal/reclaim"
	"github.com/spf13/cobra"
)

func (rt *runtime) reclaimApplet() *cobra.Command {
	command := rt.reclaimCommand("reclaim")
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetOut(rt.stdout)
	command.SetErr(rt.stderr)
	command.PersistentFlags().BoolVar(&rt.json, "json", false, "emit stable JSON output")
	return command
}

func (rt *runtime) reclaimCommand(use string) *cobra.Command {
	var (
		tier     int
		until    string
		dryRun   bool
		only     []string
		skip     []string
		keepDays int
		list     bool
	)
	command := &cobra.Command{
		Use:   use,
		Short: "Emergency disk reclaim — delete regenerating caches biggest-first, no scan, no prompt",
		Long: `Frees disk space on a dev machine in seconds by running a fixed ladder of
deletions, each of which regenerates on its own. Nothing is scanned first;
steps in a tier run concurrently and every finished step prints the volume's
free space at that moment, so partial wins land while the rest is working.

Tiers: 1 build/package caches (Go, Docker build cache + images, bun, npm,
DerivedData, gradle, pnpm, cargo, pip/uv) · 2 tool/app caches, brew, logs,
orphaned Playwright revisions, dead simulators · 3 aggressive but reversible
(iOS DeviceSupport, device-less simulator runtimes, simulator logs, aged
Messages/app sandbox temp, Trash). Default runs all three.

Never touched: Docker volumes and containers, screen recordings, anything
unclassified — those are surfaced as by-hand notes at the end.`,
		Example: `  reclaim                 # everything reversible, biggest-first
  reclaim --until 100G    # stop as soon as 100G is free
  reclaim --tier 1        # only the huge build/package caches
  reclaim --dry-run       # size the ladder, delete nothing
  reclaim --list          # print the ladder`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			env := reclaim.Env{
				Home: home, Volume: home, Now: time.Now(), GOOS: goruntime.GOOS,
				LookPath: exec.LookPath,
				Free:     reclaim.Free,
				Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
					return exec.CommandContext(ctx, name, args...).CombinedOutput()
				},
				Stderr: func(s string) { fmt.Fprintln(rt.stderr, s) },
			}
			opts := reclaim.Options{MaxTier: reclaim.Tier(tier), DryRun: dryRun, Only: only, Skip: skip, KeepDays: keepDays}
			if until != "" {
				n, err := reclaim.ParseHuman(until)
				if err != nil {
					return err
				}
				opts.Until = n
			}
			if list {
				return rt.reclaimList(reclaim.Plan(env, opts))
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			var progress reclaim.Progress
			if !rt.json {
				free, total, err := reclaim.Free(home)
				if err != nil {
					return err
				}
				fmt.Fprintf(rt.stdout, "BEFORE  free %s of %s%s\n", reclaim.Human(free), reclaim.Human(total), dryLabel(dryRun))
				progress = &reclaimPrinter{rt: rt}
			}
			report, err := reclaim.Run(ctx, env, opts, progress)
			if rt.json {
				return writeJSON(rt.stdout, report)
			}
			if err != nil {
				fmt.Fprintf(rt.stdout, "interrupted: %v\n", err)
			}
			rt.reclaimSummary(report)
			return nil
		},
	}
	command.Flags().IntVar(&tier, "tier", 3, "highest tier to run (1 bulk caches · 2 tool caches · 3 aggressive)")
	command.Flags().StringVar(&until, "until", "", "stop once this much is free, e.g. 100G")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "size each step, delete nothing")
	command.Flags().StringSliceVar(&only, "only", nil, "run only these step ids")
	command.Flags().StringSliceVar(&skip, "skip", nil, "skip these step ids")
	command.Flags().IntVar(&keepDays, "keep-days", 60, "aged steps keep files newer than this")
	command.Flags().BoolVar(&list, "list", false, "print the ladder and exit")
	return command
}

func dryLabel(dry bool) string {
	if dry {
		return "   (dry run — nothing is deleted)"
	}
	return ""
}

type reclaimPrinter struct{ rt *runtime }

func (p *reclaimPrinter) Step(r reclaim.Result) {
	switch r.Status {
	case reclaim.StatusSkipped:
		fmt.Fprintf(p.rt.stdout, "[%d] %-44s  skipped: %s\n", r.Tier, trunc(r.Title, 44), r.Reason)
	case reclaim.StatusFailed:
		fmt.Fprintf(p.rt.stdout, "[%d] %-44s  FAILED  %s\n", r.Tier, trunc(r.Title, 44), firstLine(r.Error))
	default:
		size := reclaim.Human(r.Bytes)
		if r.Bytes == 0 && r.Reason != "" {
			size = "?"
		}
		fmt.Fprintf(p.rt.stdout, "[%d] %-44s  %8s  %5.1fs  free %s\n", r.Tier, trunc(r.Title, 44), size, r.Seconds, reclaim.Human(r.FreeAfter))
	}
}

func (p *reclaimPrinter) TierDone(tier reclaim.Tier, free int64, elapsed time.Duration) {
	fmt.Fprintf(p.rt.stdout, "── tier %d done in %.1fs · free %s\n", tier, elapsed.Seconds(), reclaim.Human(free))
}

func (rt *runtime) reclaimSummary(report reclaim.Report) {
	fmt.Fprintf(rt.stdout, "AFTER   free %s of %s  (%s in %.0fs)\n", reclaim.Human(report.FreeAfter), reclaim.Human(report.Total), reclaim.Signed(report.Freed()), report.Seconds)
	if report.Reached {
		fmt.Fprintf(rt.stdout, "target %s reached — remaining steps skipped\n", reclaim.Human(report.Until))
	}
	if len(report.Notes) > 0 {
		fmt.Fprintln(rt.stdout, "NOT DELETED — by hand:")
		for _, n := range report.Notes {
			fmt.Fprintf(rt.stdout, "  %-32s %8s  %s\n", n.Title, reclaim.Human(n.Bytes), n.Detail)
			if n.Action != "" {
				fmt.Fprintf(rt.stdout, "  %-32s %8s  %s\n", "", "", n.Action)
			}
		}
	}
	fmt.Fprintln(rt.stdout, "note: Docker/OrbStack sparse images return host space ~1 min after a prune — df lags.")
}

func (rt *runtime) reclaimList(plan []reclaim.Step) error {
	if rt.json {
		return writeJSON(rt.stdout, plan)
	}
	for _, s := range plan {
		what := strings.Join(s.Paths, " ")
		if what == "" {
			what = "(" + s.Needs + ")"
		}
		fmt.Fprintf(rt.stdout, "[%d] %-16s %-44s  regenerates: %s\n      %s\n", s.Tier, s.ID, trunc(s.Title, 44), s.Regenerates, what)
	}
	return nil
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/henderson-tech/vybava/internal/worktime"
	"github.com/spf13/cobra"
)

func (rt *runtime) worktimeApplet() *cobra.Command {
	c := rt.worktimeCommand("worktime")
	c.SetOut(rt.stdout)
	c.SetErr(rt.stderr)
	c.SilenceErrors, c.SilenceUsage = true, true
	c.PersistentFlags().BoolVar(&rt.json, "json", false, "emit stable JSON")
	return c
}

func (rt *runtime) worktimeCommand(use string) *cobra.Command {
	return rt.worktimeCommandWithSources(use, worktime.NativeSample, worktime.WindowContext)
}

func (rt *runtime) worktimeCommandWithSources(use string, native func(context.Context) (worktime.Sample, error), window func(context.Context, worktime.Sample) worktime.Sample) *cobra.Command {
	c := &cobra.Command{Use: use, Short: "Observe foreground application and idle duration without recording content"}
	var interval time.Duration
	var windowContext bool
	sample := &cobra.Command{Use: "sample", Short: "Read one native activity sample", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 8*time.Second)
		defer cancel()
		s, err := native(ctx)
		if err != nil {
			return err
		}
		if windowContext {
			s = window(ctx, s)
		}
		if rt.json {
			return json.NewEncoder(rt.stdout).Encode(s)
		}
		if s.ContextError != "" {
			if _, err = fmt.Fprintf(rt.stderr, "Warning: %s\n", s.ContextError); err != nil {
				return err
			}
		}
		_, err = fmt.Fprintf(rt.stdout, "%s · idle %.0fs\n", s.Application, s.IdleSeconds)
		return err
	}}
	sample.Flags().BoolVar(&windowContext, "window-context", false, "read foreground window title for local-only task matching")
	watch := &cobra.Command{Use: "watch", Short: "Stream native observations as JSON lines until stopped", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if interval < time.Second || interval > time.Minute {
			return fmt.Errorf("interval must be between 1s and 1m")
		}
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			if err := cmd.Context().Err(); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 8*time.Second)
			s, err := native(ctx)
			cancel()
			if err != nil {
				return err
			}
			if err := cmd.Context().Err(); err != nil {
				return err
			}
			if err := json.NewEncoder(rt.stdout).Encode(s); err != nil {
				return err
			}
			select {
			case <-cmd.Context().Done():
				return cmd.Context().Err()
			case <-t.C:
			}
		}
	}}
	watch.Flags().DurationVar(&interval, "interval", 5*time.Second, "sampling interval")
	c.AddCommand(sample, watch)
	return c
}

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/FixIt-Technologies/vybava/internal/operator"
	"github.com/spf13/cobra"
)

func (rt *runtime) operatorMessagesCommand(store func() (operator.Store, error)) *cobra.Command {
	var binary, database string
	var interval time.Duration
	var once bool
	c := &cobra.Command{Use: "messages", Short: "Read Messages changes without activating or modifying the app", Args: cobra.NoArgs}
	c.Flags().StringVar(&binary, "reader", "", "absolute path to the verified imsg 0.15.2 reader")
	c.Flags().StringVar(&database, "db", "~/Library/Messages/chat.db", "Messages database, opened read-only")
	c.Flags().DurationVar(&interval, "interval", 30*time.Second, "check interval (minimum 5s)")
	c.Flags().BoolVar(&once, "once", false, "perform one check, print coverage and exit")
	c.RunE = func(cmd *cobra.Command, _ []string) error {
		if interval < 5*time.Second {
			return errors.New("Messages interval must be at least 5s")
		}
		var err error
		binary, err = expandHome(binary)
		if err != nil {
			return err
		}
		database, err = expandHome(database)
		if err != nil {
			return err
		}
		s, err := store()
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		reader := operator.MessagesReader{Binary: binary, Database: database}
		for {
			added, err := s.ScanMessages(ctx, reader)
			if err != nil {
				return err
			}
			var coverage operator.SourceCoverage
			if err := s.View(func(state *operator.State) error {
				if state.Messages == nil {
					return errors.New("Messages source state is missing")
				}
				coverage = state.Messages.Coverage
				return nil
			}); err != nil {
				return err
			}
			if rt.json {
				if err := writeJSON(rt.stdout, struct {
					Observed []string                `json:"observed"`
					Coverage operator.SourceCoverage `json:"coverage"`
				}{added, coverage}); err != nil {
					return err
				}
			} else {
				status := "checked"
				if coverage.Error != "" {
					status = coverage.Error
				}
				if _, err := fmt.Fprintf(rt.stdout, "Messages: %s · %d observations · sending: human-only\n", status, len(added)); err != nil {
					return err
				}
			}
			if once {
				if coverage.Error != "" {
					return errors.New(coverage.Error)
				}
				return nil
			}
			if err := waitMessages(ctx, interval); err != nil {
				return nil
			}
		}
	}
	return c
}

func waitMessages(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

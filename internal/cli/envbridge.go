package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/henderson-tech/vybava/internal/envbridge"
	"github.com/spf13/cobra"
)

func (rt *runtime) envbridgeApplet() *cobra.Command {
	cmd := rt.envbridgeCommand()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetOut(rt.stdout)
	cmd.SetErr(rt.stderr)
	cmd.PersistentFlags().BoolVar(&rt.json, "json", false, "emit machine-readable output")
	return cmd
}

func (rt *runtime) envbridgeCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "envbridge", Short: "Transfer explicit environment keys through a private, expiring Unix socket"}
	var serveSocket string
	var serveKeys []string
	var ttl time.Duration
	serve := &cobra.Command{Use: "serve", Short: "Read JSON values from stdin; retain only named keys in memory", Args: cobra.NoArgs}
	serve.Flags().StringVar(&serveSocket, "socket", "", "absolute socket path in an existing private directory")
	serve.Flags().StringSliceVar(&serveKeys, "key", nil, "allowed environment key (repeatable)")
	serve.Flags().DurationVar(&ttl, "ttl", 2*time.Minute, "lifetime, from 1s to 10m")
	serve.RunE = func(command *cobra.Command, _ []string) error {
		if ttl < time.Second || ttl > 10*time.Minute {
			return errors.New("ttl must be between 1s and 10m")
		}
		values, err := envbridge.Input(rt.stdin, serveKeys)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(command.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		ctx, cancel := context.WithTimeout(ctx, ttl)
		defer cancel()
		return envbridge.Serve(ctx, serveSocket, values, func() error {
			if rt.json {
				return json.NewEncoder(rt.stdout).Encode(struct {
					Ready bool `json:"ready"`
					Keys  int  `json:"keyCount"`
				}{true, len(values)})
			}
			_, err := fmt.Fprintf(rt.stdout, "Ready: %d keys available for %s (values remain in memory)\n", len(values), ttl)
			return err
		})
	}
	var readSocket, format string
	var readKeys []string
	read := &cobra.Command{Use: "read", Short: "Emit selected values to a consuming process; output is sensitive", Args: cobra.NoArgs}
	read.Flags().StringVar(&readSocket, "socket", "", "absolute private socket path")
	read.Flags().StringSliceVar(&readKeys, "key", nil, "requested environment key (repeatable)")
	read.Flags().StringVar(&format, "format", "", "required output format: json or shell (contains values)")
	read.RunE = func(command *cobra.Command, _ []string) error {
		if format == "" && rt.json {
			format = "json"
		}
		if format != "json" && format != "shell" {
			return errors.New("select --format=json or --format=shell; read output contains values")
		}
		values, err := envbridge.Read(command.Context(), readSocket, readKeys)
		if err != nil {
			return err
		}
		if format == "json" {
			return json.NewEncoder(rt.stdout).Encode(values)
		}
		text, err := envbridge.ShellExports(values)
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(rt.stdout, text)
		return err
	}
	cmd.AddCommand(serve, read)
	return cmd
}

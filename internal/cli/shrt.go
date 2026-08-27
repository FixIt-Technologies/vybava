package cli

import (
	"bufio"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/FixIt-Technologies/vybava/internal/shrt"
	"github.com/spf13/cobra"
)

func (rt *runtime) shrtCommand(use string) *cobra.Command {
	var osc8 bool
	var label, base string
	command := &cobra.Command{
		Use:   use,
		Short: "Shorten URLs into terminal-safe luko.to links",
		Long: "Shorten URLs so they never wrap (and never truncate on click) in narrow\n" +
			"terminal panes. Known hosts shorten offline via static rules; everything\n" +
			"else is minted through the luko.to API (token from $LUKO_TOKEN or the\n" +
			"macOS Keychain, service \"luko.to\").",
		Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			client := shrt.Client{Base: base, Token: shrt.LoadToken()}
			var firstErr error
			var results []shrt.Result
			for _, arg := range args {
				result, err := client.Shorten(arg)
				if err != nil {
					// Keep going and still print something clickable, but
					// surface the reason and fail the exit code.
					fmt.Fprintf(rt.stderr, "shrt: %s: %v\n", arg, err)
					if firstErr == nil {
						firstErr = err
					}
				}
				results = append(results, result)
			}
			if rt.json {
				return writeJSON(rt.stdout, results)
			}
			for _, result := range results {
				line := result.Short
				if osc8 {
					text := label
					if text == "" {
						text = result.Short
					}
					line = shrt.OSC8(text, result.Short)
				}
				if _, err := fmt.Fprintln(rt.stdout, line); err != nil {
					return err
				}
			}
			return firstErr
		},
	}
	command.Flags().BoolVar(&osc8, "osc8", false, "emit an OSC 8 hyperlink instead of plain text")
	command.Flags().StringVar(&label, "label", "", "visible text for --osc8 (default: the short URL)")
	command.Flags().StringVar(&base, "base", "", "redirector origin override (default "+shrt.DefaultBase+")")
	command.AddCommand(rt.shrtServeCommand(), rt.shrtTokenCommand())
	return command
}

func (rt *runtime) shrtServeCommand() *cobra.Command {
	var addr, store, base string
	command := &cobra.Command{
		Use:   "serve",
		Short: "Run the luko.to redirector server",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			linkStore, err := shrt.OpenStore(store)
			if err != nil {
				return err
			}
			server := &shrt.Server{
				Base:      strings.TrimRight(base, "/"),
				MintToken: strings.TrimSpace(os.Getenv("LUKO_MINT_TOKEN")),
				Store:     linkStore,
				Log:       log.New(rt.stderr, "shrt: ", log.LstdFlags),
			}
			if server.MintToken == "" {
				fmt.Fprintln(rt.stderr, "shrt: LUKO_MINT_TOKEN unset — minting disabled, static redirects only")
			}
			fmt.Fprintf(rt.stderr, "shrt: serving %s on %s (store %s)\n", server.Base, addr, store)
			httpServer := &http.Server{
				Addr:              addr,
				Handler:           server.Handler(),
				ReadHeaderTimeout: 5 * time.Second,
			}
			return httpServer.ListenAndServe()
		},
	}
	command.Flags().StringVar(&addr, "addr", ":8080", "listen address")
	command.Flags().StringVar(&store, "store", "/data/links.jsonl", "minted-links JSONL path")
	command.Flags().StringVar(&base, "base", shrt.DefaultBase, "public origin used in mint responses")
	return command
}

func (rt *runtime) shrtTokenCommand() *cobra.Command {
	command := &cobra.Command{Use: "token", Short: "Manage the mint token"}
	set := &cobra.Command{
		Use:   "set",
		Short: "Store the mint token in the macOS Keychain (reads one line from stdin)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			reader := bufio.NewReader(rt.stdin)
			line, err := reader.ReadString('\n')
			if err != nil && line == "" {
				return fmt.Errorf("reading token from stdin: %w", err)
			}
			token := strings.TrimSpace(line)
			if token == "" {
				return fmt.Errorf("empty token")
			}
			if err := shrt.StoreToken(token); err != nil {
				return err
			}
			fmt.Fprintln(rt.stdout, "token stored in keychain (service luko.to)")
			return nil
		},
	}
	command.AddCommand(set)
	return command
}

func (rt *runtime) shrtApplet() *cobra.Command {
	command := rt.shrtCommand("shrt [url...]")
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetOut(rt.stdout)
	command.SetErr(rt.stderr)
	command.PersistentFlags().BoolVar(&rt.json, "json", false, "emit stable JSON output")
	return command
}

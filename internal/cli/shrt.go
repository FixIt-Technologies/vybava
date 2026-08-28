package cli

import (
	"bufio"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
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
			client := shrt.Client{Base: base, Token: shrt.LoadToken(), DynRules: shrt.LoadRuleCache()}
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
	command.AddCommand(rt.shrtServeCommand(), rt.shrtTokenCommand(), rt.shrtRuleCommand())
	return command
}

func (rt *runtime) shrtRuleCommand() *cobra.Command {
	var base string
	newClient := func() shrt.Client {
		return shrt.Client{Base: base, Token: shrt.LoadToken()}
	}
	displayBase := func() string {
		if base != "" {
			return strings.TrimRight(base, "/")
		}
		return shrt.DefaultBase
	}
	command := &cobra.Command{
		Use:   "rule",
		Short: "Manage dynamic prefix rules (server-side, cached locally)",
		Long: "A rule maps a URL prefix to a named namespace: rule \"sentry\" with prefix\n" +
			"https://sentry.example.com/issues/ makes any matching URL shorten to\n" +
			"luko.to/sentry/<tail> — offline via the local cache, immediately\n" +
			"everywhere via the server. Prefixes must end with \"/\".",
	}
	command.PersistentFlags().StringVar(&base, "base", "", "redirector origin override (default "+shrt.DefaultBase+")")

	add := &cobra.Command{
		Use:   "add <name> <url-prefix>",
		Short: "Create a rule",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			rule, err := newClient().CreateRule(args[0], args[1])
			if err != nil {
				return err
			}
			if rt.json {
				return writeJSON(rt.stdout, rule)
			}
			_, err = fmt.Fprintf(rt.stdout, "%s/%s/… -> %s…\n", displayBase(), rule.Name, rule.Prefix)
			return err
		},
	}
	update := &cobra.Command{
		Use:   "update <name> <url-prefix>",
		Short: "Replace a rule's prefix",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			rule, err := newClient().UpdateRule(args[0], args[1])
			if err != nil {
				return err
			}
			if rt.json {
				return writeJSON(rt.stdout, rule)
			}
			_, err = fmt.Fprintf(rt.stdout, "%s/%s/… -> %s…\n", displayBase(), rule.Name, rule.Prefix)
			return err
		},
	}
	remove := &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove", "delete"},
		Short:   "Delete a rule (existing short links under it stop resolving)",
		Args:    cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := newClient().DeleteRule(args[0]); err != nil {
				return err
			}
			_, err := fmt.Fprintf(rt.stdout, "rule %s deleted\n", args[0])
			return err
		},
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List rules (refreshes the local cache)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			rules, err := newClient().FetchRules()
			if err != nil {
				return err
			}
			if rt.json {
				return writeJSON(rt.stdout, rules)
			}
			for _, rule := range rules {
				if _, err := fmt.Fprintf(rt.stdout, "%-14s %s\n", rule.Name, rule.Prefix); err != nil {
					return err
				}
			}
			return nil
		},
	}
	command.AddCommand(add, update, remove, list)
	return command
}

func (rt *runtime) shrtServeCommand() *cobra.Command {
	var addr, store, rules, base string
	command := &cobra.Command{
		Use:   "serve",
		Short: "Run the luko.to redirector server",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			linkStore, err := shrt.OpenStore(store)
			if err != nil {
				return err
			}
			if rules == "" {
				rules = filepath.Join(filepath.Dir(store), "rules.json")
			}
			ruleStore, err := shrt.OpenRuleStore(rules)
			if err != nil {
				return err
			}
			tokenStore, err := shrt.OpenTokenStore(filepath.Join(filepath.Dir(store), "tokens.json"))
			if err != nil {
				return err
			}
			server := &shrt.Server{
				Base:      strings.TrimRight(base, "/"),
				MintToken: strings.TrimSpace(os.Getenv("LUKO_MINT_TOKEN")),
				Store:     linkStore,
				Rules:     ruleStore,
				Tokens:    tokenStore,
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
				ReadTimeout:       10 * time.Second,
				WriteTimeout:      30 * time.Second,
				IdleTimeout:       120 * time.Second,
			}
			return httpServer.ListenAndServe()
		},
	}
	command.Flags().StringVar(&addr, "addr", ":8080", "listen address")
	command.Flags().StringVar(&store, "store", "/data/links.jsonl", "minted-links JSONL path")
	command.Flags().StringVar(&rules, "rules", "", "dynamic-rules JSON path (default: rules.json beside --store)")
	command.Flags().StringVar(&base, "base", shrt.DefaultBase, "public origin used in mint responses")
	return command
}

func (rt *runtime) shrtTokenCommand() *cobra.Command {
	var base string
	newClient := func() shrt.Client {
		return shrt.Client{Base: base, Token: shrt.LoadToken()}
	}
	command := &cobra.Command{Use: "token", Short: "Manage tokens: your local one (set) and team members' (issue/revoke/list, admin)"}
	command.PersistentFlags().StringVar(&base, "base", "", "redirector origin override (default "+shrt.DefaultBase+")")
	issue := &cobra.Command{
		Use:   "issue <name>",
		Short: "Issue a named member token (admin) — the value prints ONCE",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			issued, err := newClient().IssueToken(args[0])
			if err != nil {
				return err
			}
			if rt.json {
				return writeJSON(rt.stdout, issued)
			}
			_, err = fmt.Fprintf(rt.stdout, "%s\n", issued.Token)
			if err == nil {
				fmt.Fprintf(rt.stderr, "token for %q printed above — it cannot be shown again; deliver it securely\n", issued.Name)
			}
			return err
		},
	}
	revoke := &cobra.Command{
		Use:     "revoke <name>",
		Aliases: []string{"rm"},
		Short:   "Revoke a member token (admin) — access ends immediately",
		Args:    cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := newClient().RevokeToken(args[0]); err != nil {
				return err
			}
			_, err := fmt.Fprintf(rt.stdout, "token %s revoked\n", args[0])
			return err
		},
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List member token names (admin) — never values",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			tokens, err := newClient().ListTokens()
			if err != nil {
				return err
			}
			if rt.json {
				return writeJSON(rt.stdout, tokens)
			}
			for _, tok := range tokens {
				if _, err := fmt.Fprintf(rt.stdout, "%-14s issued %s\n", tok.Name, tok.CreatedAt.Format("2006-01-02")); err != nil {
					return err
				}
			}
			return nil
		},
	}
	command.AddCommand(issue, revoke, list)
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

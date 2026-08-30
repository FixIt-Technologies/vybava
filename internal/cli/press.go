package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/FixIt-Technologies/vybava/internal/press"
	"github.com/spf13/cobra"
)

func (rt *runtime) pressApplet() *cobra.Command {
	command := rt.pressCommand("press")
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetOut(rt.stdout)
	command.SetErr(rt.stderr)
	command.PersistentFlags().BoolVar(&rt.json, "json", false, "emit stable JSON output")
	return command
}

func (rt *runtime) pressCommand(use string) *cobra.Command {
	var project string
	command := &cobra.Command{
		Use:   use,
		Short: "Deterministic state for the press document family",
		Long: "Every mutation of ~/Exports/<project>/ — config, PRESS.md index, artifact\n" +
			"notes — goes through press, so an agent never hand-edits state.",
	}
	command.PersistentFlags().StringVar(&project, "project", "",
		"project name; overrides git-based resolution (required outside a git repository)")

	resolve := func() (press.Runtime, string, error) {
		rt := press.New(nil)
		name, err := rt.Resolve(project)
		return rt, name, err
	}

	command.AddCommand(
		rt.pressResolveCommand(&project),
		rt.pressInitCommand(resolve),
		rt.pressConfigCommand(resolve),
		rt.pressIndexCommand(resolve),
		rt.pressAresCommand(&project),
		rt.pressLintCommand(resolve),
		rt.pressDoctrineCommand(),
		rt.pressIdentityCommand(),
	)
	return command
}

type resolver func() (press.Runtime, string, error)

func (rt *runtime) pressResolveCommand(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "resolve",
		Short: "Print the project name for this working directory",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			name, err := press.New(nil).Resolve(*project)
			if err != nil {
				return err
			}
			if rt.json {
				return writeJSON(rt.stdout, map[string]string{"project": name})
			}
			_, err = fmt.Fprintln(rt.stdout, name)
			return err
		},
	}
}

func (rt *runtime) pressInitCommand(resolve resolver) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create the project's deliverable home, config, and index (idempotent)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			runner, name, err := resolve()
			if err != nil {
				return err
			}
			created, err := runner.Init(name)
			if err != nil {
				return err
			}
			if rt.json {
				return writeJSON(rt.stdout, map[string]any{
					"project": name, "dir": runner.ProjectDir(name), "created": created,
				})
			}
			state := "already present"
			if created {
				state = "created"
			}
			_, err = fmt.Fprintf(rt.stdout, "%s — %s (%s)\n", name, runner.ProjectDir(name), state)
			return err
		},
	}
}

func (rt *runtime) pressConfigCommand(resolve resolver) *cobra.Command {
	command := &cobra.Command{Use: "config", Short: "Read and write the project config"}
	command.AddCommand(
		&cobra.Command{
			Use:   "get <dot.path>",
			Short: "Read one config value",
			Args:  cobra.ExactArgs(1),
			RunE: func(_ *cobra.Command, args []string) error {
				runner, name, err := resolve()
				if err != nil {
					return err
				}
				value, err := runner.ConfigGet(name, args[0])
				if err != nil {
					return err
				}
				if !rt.json {
					// Scalars print bare so `press config get project.name` is
					// directly usable in a shell; structures stay JSON.
					switch v := value.(type) {
					case string:
						_, err := fmt.Fprintln(rt.stdout, v)
						return err
					case bool, float64:
						_, err := fmt.Fprintf(rt.stdout, "%v\n", v)
						return err
					}
				}
				return writeJSON(rt.stdout, value)
			},
		},
		&cobra.Command{
			Use:   "set <dot.path> <value>",
			Short: "Write one config value (JSON when it parses, string otherwise)",
			Args:  cobra.ExactArgs(2),
			RunE: func(_ *cobra.Command, args []string) error {
				runner, name, err := resolve()
				if err != nil {
					return err
				}
				if err := runner.ConfigSet(name, args[0], args[1]); err != nil {
					return err
				}
				if rt.json {
					return writeJSON(rt.stdout, map[string]any{"path": args[0], "ok": true})
				}
				_, err = fmt.Fprintln(rt.stdout, "ok")
				return err
			},
		},
	)
	return command
}

func (rt *runtime) pressIndexCommand(resolve resolver) *cobra.Command {
	command := &cobra.Command{Use: "index", Short: "Record and list the project's artifacts"}

	var kind string
	list := &cobra.Command{
		Use:   "list",
		Short: "List recorded artifacts",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			runner, name, err := resolve()
			if err != nil {
				return err
			}
			entries, err := runner.IndexList(name, kind)
			if err != nil {
				return err
			}
			if rt.json {
				return writeJSON(rt.stdout, entries)
			}
			return writeIndexText(rt.stdout, entries)
		},
	}
	list.Flags().StringVar(&kind, "kind", "", "pdf|logo|design (default: all)")

	entry := press.Entry{}
	add := &cobra.Command{
		Use:   "add",
		Short: "Record or update an artifact, then regenerate the index",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			runner, name, err := resolve()
			if err != nil {
				return err
			}
			id, created, err := runner.IndexAdd(name, entry)
			if err != nil {
				return err
			}
			if rt.json {
				return writeJSON(rt.stdout, map[string]any{"id": id, "created": created})
			}
			verb := "updated"
			if created {
				verb = "recorded"
			}
			_, err = fmt.Fprintf(rt.stdout, "%s %s\n", verb, id)
			return err
		},
	}
	flags := add.Flags()
	flags.StringVar(&entry.Kind, "kind", "pdf", "pdf|logo|design")
	flags.StringVar(&entry.Type, "type", "", "offer|documentation|legal for pdf; free for logo and design")
	flags.StringVar(&entry.File, "file", "", "path relative to the project directory")
	flags.StringVar(&entry.Title, "title", "", "human title")
	flags.StringVar(&entry.Version, "version", "", "artifact version")
	flags.StringVar(&entry.Issuer, "issuer", "", "legal: issuing party")
	flags.StringVar(&entry.Target, "target", "", "legal or offer: counterparty")
	flags.StringVar(&entry.Status, "status", "", "draft|sent|signed|published")
	flags.StringVar(&entry.ID, "id", "", "stable id (default: slug of file or title)")

	command.AddCommand(list, add)
	return command
}

func (rt *runtime) pressAresCommand(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "ares <ICO>",
		Short: "Look up a Czech company in the ARES registry (cached in the project config)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			runner := press.New(nil)
			// A project is only needed for the cache; outside a repository the
			// lookup still works, it just is not cached.
			name, _ := runner.Resolve(*project)
			info, err := runner.Ares(name, args[0])
			if err != nil {
				return err
			}
			if rt.json {
				return writeJSON(rt.stdout, info)
			}
			for _, key := range []string{"name", "ico", "dic", "legalForm", "address"} {
				if v, _ := info[key].(string); v != "" {
					if _, err := fmt.Fprintf(rt.stdout, "%-10s %s\n", key, v); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
}

func (rt *runtime) pressLintCommand(resolve resolver) *cobra.Command {
	var fix bool
	command := &cobra.Command{
		Use:   "lint",
		Short: "Validate the project's press state; --fix self-corrects what it can",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			runner, name, err := resolve()
			if err != nil {
				return err
			}
			report, err := runner.Lint(name, fix)
			if err != nil {
				return err
			}
			if rt.json {
				if err := writeJSON(rt.stdout, report); err != nil {
					return err
				}
			} else {
				for _, line := range append(
					prefixed("fixed: ", report.Fixed), prefixed("problem: ", report.Problems)...,
				) {
					if _, err := fmt.Fprintln(rt.stdout, line); err != nil {
						return err
					}
				}
				if report.OK() {
					if _, err := fmt.Fprintln(rt.stdout, "ok"); err != nil {
						return err
					}
				}
			}
			if !report.OK() {
				return ErrFindings
			}
			return nil
		},
	}
	command.Flags().BoolVar(&fix, "fix", false, "auto-correct fixable issues")
	return command
}

func (rt *runtime) pressDoctrineCommand() *cobra.Command {
	var schema bool
	command := &cobra.Command{
		Use:   "doctrine",
		Short: "Print the press family's shared law, or its config schema",
		Long: "The doctrine every press skill reads before acting. It ships inside the\n" +
			"binary so the three skills share one source that cannot rot into a\n" +
			"stale filesystem path.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			text := press.Conventions()
			kind := "conventions"
			if schema {
				text = press.ConfSchema()
				kind = "schema"
			}
			if rt.json {
				// --schema is already JSON; emit it as itself rather than a
				// string-wrapped copy an automation would have to unwrap twice.
				if schema {
					_, err := fmt.Fprintln(rt.stdout, strings.TrimRight(text, "\n"))
					return err
				}
				return writeJSON(rt.stdout, map[string]string{kind: text})
			}
			_, err := fmt.Fprintln(rt.stdout, strings.TrimRight(text, "\n"))
			return err
		},
	}
	command.Flags().BoolVar(&schema, "schema", false, "print the .press.conf.json JSON Schema instead")
	return command
}

func (rt *runtime) pressIdentityCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "identity",
		Short: "The machine-local issuer identity, house rate, and brand tokens",
		Long: "Press deliverables carry an issuer's registry identity, commercial rate,\n" +
			"and brand. That is private business data, so it lives in a file outside\n" +
			"any checkout (~/.config/press/identity.json, or PRESS_IDENTITY) and is\n" +
			"never committed. Skills read it from here instead of hardcoding it.",
	}
	command.AddCommand(
		&cobra.Command{
			Use:   "path",
			Short: "Print the identity file location",
			Args:  cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				if rt.json {
					return writeJSON(rt.stdout, map[string]string{"path": press.IdentityPath()})
				}
				_, err := fmt.Fprintln(rt.stdout, press.IdentityPath())
				return err
			},
		},
		&cobra.Command{
			Use:   "init",
			Short: "Create the placeholder identity file when none exists",
			Args:  cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				path, created, err := press.InitIdentity()
				if err != nil {
					return err
				}
				if rt.json {
					return writeJSON(rt.stdout, map[string]any{"path": path, "created": created})
				}
				if created {
					_, err = fmt.Fprintf(rt.stdout, "created %s — fill it in, then `press identity show`\n", path)
				} else {
					_, err = fmt.Fprintf(rt.stdout, "already present: %s\n", path)
				}
				return err
			},
		},
		&cobra.Command{
			Use:   "show",
			Short: "Print the local identity, and which required fields are still empty",
			Args:  cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				identity, err := press.LoadIdentity()
				if err != nil {
					return err
				}
				if rt.json {
					return writeJSON(rt.stdout, map[string]any{
						"identity": identity,
						"missing":  identity.MissingIdentityFields(),
					})
				}
				if err := writeIdentityText(rt.stdout, identity); err != nil {
					return err
				}
				if missing := identity.MissingIdentityFields(); len(missing) > 0 {
					fmt.Fprintf(rt.stderr, "press: incomplete identity — fill in %s in %s\n",
						strings.Join(missing, ", "), press.IdentityPath())
				}
				return nil
			},
		},
	)
	return command
}

// writeIndexText renders the artifact index for a human reader; `--json` keeps
// the stable machine shape.
func writeIndexText(out io.Writer, entries map[string]any) error {
	empty := true
	for _, kind := range []string{"pdf", "logo", "design"} {
		list, _ := entries[kind].([]any)
		if len(list) == 0 {
			continue
		}
		empty = false
		if _, err := fmt.Fprintf(out, "%s\n", kind); err != nil {
			return err
		}
		for _, item := range list {
			entry, _ := item.(map[string]any)
			if entry == nil {
				continue
			}
			field := func(key string) string { v, _ := entry[key].(string); return v }
			line := "  " + field("id")
			for _, part := range []string{field("title"), field("file"), field("status")} {
				if part != "" {
					line += "  " + part
				}
			}
			if _, err := fmt.Fprintln(out, line); err != nil {
				return err
			}
		}
	}
	if empty {
		_, err := fmt.Fprintln(out, "no artifacts recorded yet")
		return err
	}
	return nil
}

// writeIdentityText prints the local identity as plain fields. Values are the
// operator's own and never leave the machine, so there is nothing to redact.
func writeIdentityText(out io.Writer, identity press.Identity) error {
	for _, field := range []struct{ label, value string }{
		{"name", identity.Issuer.Name}, {"ico", identity.Issuer.ICO},
		{"dic", identity.Issuer.DIC}, {"address", identity.Issuer.Address},
		{"dataBox", identity.Issuer.DataBox}, {"email", identity.Issuer.Email},
		{"web", identity.Issuer.Web},
	} {
		if _, err := fmt.Fprintf(out, "issuer.%-8s %s\n", field.label, field.value); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(out, "commercial.rate     %g %s / %s\n",
		identity.Commercial.DayRate, identity.Commercial.Currency, identity.Commercial.RateUnit); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "commercial.validity %d weeks\n", identity.Commercial.ValidityWeeks); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "brand.accent        %s (font %s)\n", identity.Brand.Accent, identity.Brand.Font)
	return err
}

func prefixed(prefix string, values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = prefix + v
	}
	return out
}

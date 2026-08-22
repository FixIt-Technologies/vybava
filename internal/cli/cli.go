package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	assets "github.com/FixIt-Technologies/vybava"
	"github.com/FixIt-Technologies/vybava/internal/catalog"
	"github.com/FixIt-Technologies/vybava/internal/doctor"
	"github.com/FixIt-Technologies/vybava/internal/fontfreeze"
	"github.com/FixIt-Technologies/vybava/internal/installer"
	"github.com/FixIt-Technologies/vybava/internal/memorylint"
	"github.com/FixIt-Technologies/vybava/internal/state"
	"github.com/FixIt-Technologies/vybava/internal/ui"
	"github.com/spf13/cobra"
)

var ErrFindings = errors.New("lint findings")

type App struct {
	Version string
	Stdout  io.Writer
	Stderr  io.Writer
}

type runtime struct {
	catalog   catalog.Catalog
	installer installer.Installer
	json      bool
	stdout    io.Writer
	stderr    io.Writer
}

func (a App) Command(invokedAs string) (*cobra.Command, error) {
	if a.Stdout == nil {
		a.Stdout = os.Stdout
	}
	if a.Stderr == nil {
		a.Stderr = os.Stderr
	}
	c, err := catalog.Load(assets.FS)
	if err != nil {
		return nil, err
	}
	store, err := state.DefaultStore()
	if err != nil {
		return nil, err
	}
	rt := &runtime{
		catalog: c, stdout: a.Stdout, stderr: a.Stderr,
		installer: installer.Installer{Payload: assets.FS, Store: store},
	}
	if filepath.Base(invokedAs) == "memorylint" {
		return rt.memorylintApplet(), nil
	}
	if filepath.Base(invokedAs) == "fontfreeze" {
		return rt.fontfreezeApplet(), nil
	}

	root := &cobra.Command{
		Use:           "vybava",
		Short:         "Install and maintain FixIt Technologies engineering tools and AI skills",
		Version:       a.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(a.Stdout)
	root.SetErr(a.Stderr)
	root.PersistentFlags().BoolVar(&rt.json, "json", false, "emit stable JSON output")
	root.AddCommand(
		rt.catalogCommand(),
		rt.installCommand(),
		rt.uninstallCommand(),
		rt.updateCommand(),
		rt.doctorCommand(),
		rt.memoryCommand(),
		rt.fontfreezeCommand("fontfreeze [fonts.yaml]"),
		rt.browseCommand(),
	)
	return root, nil
}

func (rt *runtime) catalogCommand() *cobra.Command {
	command := &cobra.Command{Use: "catalog", Short: "Inspect available packages and groups"}
	list := &cobra.Command{
		Use:   "list",
		Short: "List catalog items and groups",
		RunE: func(*cobra.Command, []string) error {
			if rt.json {
				return writeJSON(rt.stdout, rt.catalog)
			}
			if _, err := fmt.Fprintln(rt.stdout, "ITEMS"); err != nil {
				return err
			}
			for _, item := range rt.catalog.Items {
				if _, err := fmt.Fprintf(rt.stdout, "  %-12s %-12s %-12s %s\n", item.ID, item.Kind, item.Status, item.Description); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintln(rt.stdout, "\nGROUPS"); err != nil {
				return err
			}
			for _, group := range rt.catalog.Groups {
				if _, err := fmt.Fprintf(rt.stdout, "  %-12s %s\n      %s\n", group.ID, strings.Join(group.Items, ", "), group.Description); err != nil {
					return err
				}
			}
			return nil
		},
	}
	command.AddCommand(list)
	return command
}

func (rt *runtime) installCommand() *cobra.Command {
	var agent, scope, binDir, rootDir string
	var dryRun bool
	command := &cobra.Command{
		Use:   "install [item-or-group...]",
		Short: "Install selected packages; defaults to recommended",
		Args:  cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, selectors []string) error {
			return rt.install(selectors, installer.Options{
				Agent: installer.Agent(agent), Scope: installer.Scope(scope), BinDir: binDir, RootDir: rootDir, DryRun: dryRun,
			})
		},
	}
	addInstallFlags(command, &agent, &scope, &binDir, &rootDir, &dryRun)
	return command
}

func (rt *runtime) install(selectors []string, options installer.Options) error {
	items, err := rt.catalog.Resolve(selectors)
	if err != nil {
		return err
	}
	operations, err := rt.installer.Plan(items, options)
	if err != nil {
		return err
	}
	if err := rt.installer.Apply(operations, options.DryRun); err != nil {
		return err
	}
	return rt.printOperations(operations, options.DryRun, "installed", "would install")
}

func (rt *runtime) uninstallCommand() *cobra.Command {
	var agent, scope, binDir, rootDir string
	var dryRun bool
	command := &cobra.Command{
		Use:   "uninstall <item-or-group...>",
		Short: "Remove selected Výbava-managed packages",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, selectors []string) error {
			items, err := rt.catalog.Resolve(selectors)
			if err != nil {
				return err
			}
			operations, err := rt.installer.Plan(items, installer.Options{
				Agent: installer.Agent(agent), Scope: installer.Scope(scope), BinDir: binDir, RootDir: rootDir,
			})
			if err != nil {
				return err
			}
			for index := range operations {
				operations[index].Action = "remove " + operations[index].Kind
			}
			if err := rt.installer.Remove(operations, dryRun); err != nil {
				return err
			}
			return rt.printOperations(operations, dryRun, "removed", "would remove")
		},
	}
	addInstallFlags(command, &agent, &scope, &binDir, &rootDir, &dryRun)
	return command
}

func (rt *runtime) updateCommand() *cobra.Command {
	var dryRun bool
	command := &cobra.Command{
		Use:   "update [item...]",
		Short: "Refresh installed packages from the current Výbava release",
		Args:  cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, selectors []string) error {
			current, err := rt.installer.Store.Load()
			if err != nil {
				return err
			}
			filter := make(map[string]struct{})
			if len(selectors) > 0 {
				items, err := rt.catalog.Resolve(selectors)
				if err != nil {
					return err
				}
				for _, item := range items {
					filter[item.ID] = struct{}{}
				}
			}
			var operations []installer.Operation
			for _, installed := range current.Installed {
				if len(filter) > 0 {
					if _, selected := filter[installed.ItemID]; !selected {
						continue
					}
				}
				operations = append(operations, installer.Operation{
					ItemID: installed.ItemID, Kind: installed.Kind, Agent: installed.Agent,
					Scope: installed.Scope, Destination: installed.Destination, Action: "refresh " + installed.Kind,
				})
			}
			if len(operations) == 0 {
				return errors.New("no matching installed packages")
			}
			if err := rt.installer.Apply(operations, dryRun); err != nil {
				return err
			}
			return rt.printOperations(operations, dryRun, "updated", "would update")
		},
	}
	command.Flags().BoolVar(&dryRun, "dry-run", false, "show the update plan without changing files")
	return command
}

func (rt *runtime) doctorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check the catalog, installation state, PATH, and runtime prerequisites",
		RunE: func(*cobra.Command, []string) error {
			report := doctor.Run(rt.catalog, rt.installer.Store)
			if rt.json {
				if err := writeJSON(rt.stdout, report); err != nil {
					return err
				}
			} else {
				if _, err := fmt.Fprint(rt.stdout, doctor.FormatText(report)); err != nil {
					return err
				}
			}
			if !report.Healthy() {
				return ErrFindings
			}
			return nil
		},
	}
}

func (rt *runtime) memoryCommand() *cobra.Command {
	command := &cobra.Command{Use: "memory", Short: "Work with AI memory files"}
	command.AddCommand(rt.memoryLintCommand("lint [memory-home...]"))
	return command
}

func (rt *runtime) fontfreezeApplet() *cobra.Command {
	command := rt.fontfreezeCommand("fontfreeze [fonts.yaml]")
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetOut(rt.stdout)
	command.SetErr(rt.stderr)
	command.PersistentFlags().BoolVar(&rt.json, "json", false, "emit stable JSON output")
	return command
}

func (rt *runtime) fontfreezeCommand(use string) *cobra.Command {
	var dryRun bool
	command := &cobra.Command{
		Use:   use,
		Short: "Freeze variable webfonts at the styles a site renders and subset them per language",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			manifestPath := "fonts.yaml"
			if len(args) == 1 {
				manifestPath = args[0]
			}
			manifest, err := fontfreeze.LoadManifest(manifestPath)
			if err != nil {
				return err
			}
			jobs, err := fontfreeze.Plan(manifest, filepath.Dir(manifestPath))
			if err != nil {
				return err
			}
			if dryRun {
				if rt.json {
					return writeJSON(rt.stdout, jobs)
				}
				for _, job := range jobs {
					fmt.Fprintf(rt.stdout, "%s <- %s %v\n", job.Output, job.Master, job.InstancerArgs)
				}
				return nil
			}
			if err := fontfreeze.CheckTooling(); err != nil {
				return err
			}
			fmt.Fprintln(rt.stderr, fontfreeze.LicenseReminder)
			report, err := fontfreeze.Run(jobs, fontfreeze.ExecRunner)
			if err != nil {
				return err
			}
			report.Manifest = manifestPath
			if rt.json {
				return writeJSON(rt.stdout, report)
			}
			_, err = fmt.Fprint(rt.stdout, fontfreeze.FormatText(report))
			return err
		},
	}
	command.Flags().BoolVar(&dryRun, "dry-run", false, "print planned fonttools work without executing")
	return command
}

func (rt *runtime) memorylintApplet() *cobra.Command {
	command := rt.memoryLintCommand("memorylint [memory-home...]")
	command.Short = "Validate AI memory files and Obsidian links"
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetOut(rt.stdout)
	command.SetErr(rt.stderr)
	command.PersistentFlags().BoolVar(&rt.json, "json", false, "emit stable JSON output")
	command.AddCommand(rt.memoryLintCommand("check [memory-home...]"))
	return command
}

func (rt *runtime) memoryLintCommand(use string) *cobra.Command {
	var failOn string
	command := &cobra.Command{
		Use:   use,
		Short: "Lint memory homes",
		Args:  cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, paths []string) error {
			report, err := memorylint.Lint(paths)
			if err != nil {
				return err
			}
			if rt.json {
				if err := writeJSON(rt.stdout, report); err != nil {
					return err
				}
			} else {
				if _, err := fmt.Fprint(rt.stdout, memorylint.FormatText(report)); err != nil {
					return err
				}
			}
			switch failOn {
			case "warning":
				if len(report.Findings) > 0 {
					return ErrFindings
				}
			case "error":
				if report.Errors() > 0 {
					return ErrFindings
				}
			case "never":
			default:
				return fmt.Errorf("invalid --fail-on %q: use warning, error, or never", failOn)
			}
			return nil
		},
	}
	command.Flags().StringVar(&failOn, "fail-on", "warning", "minimum finding severity that produces a non-zero exit (warning, error, never)")
	return command
}

func (rt *runtime) browseCommand() *cobra.Command {
	var agent, scope, binDir, rootDir string
	var dryRun bool
	command := &cobra.Command{
		Use:   "browse",
		Short: "Interactively select and install catalog packages",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if rt.json {
				return errors.New("browse is interactive; use install --json for automation")
			}
			ids, confirmed, err := ui.Select(rt.catalog)
			if err != nil {
				return err
			}
			if !confirmed {
				_, err := fmt.Fprintln(rt.stdout, "No changes made.")
				return err
			}
			if len(ids) == 0 {
				return errors.New("no packages selected")
			}
			return rt.install(ids, installer.Options{
				Agent: installer.Agent(agent), Scope: installer.Scope(scope), BinDir: binDir, RootDir: rootDir, DryRun: dryRun,
			})
		},
	}
	addInstallFlags(command, &agent, &scope, &binDir, &rootDir, &dryRun)
	return command
}

func (rt *runtime) printOperations(operations []installer.Operation, dryRun bool, past, future string) error {
	if rt.json {
		return writeJSON(rt.stdout, map[string]any{"dry_run": dryRun, "operations": operations})
	}
	verb := past
	if dryRun {
		verb = future
	}
	for _, operation := range operations {
		target := operation.Destination
		if operation.Agent != "" {
			target = operation.Agent + ":" + target
		}
		if _, err := fmt.Fprintf(rt.stdout, "%s %s → %s\n", verb, operation.ItemID, target); err != nil {
			return err
		}
	}
	return nil
}

func addInstallFlags(command *cobra.Command, agent, scope, binDir, rootDir *string, dryRun *bool) {
	command.Flags().StringVar(agent, "agent", "all", "skill target: claude, codex, or all")
	command.Flags().StringVar(scope, "scope", "user", "installation scope: user or project")
	command.Flags().StringVar(binDir, "bin-dir", "", "applet destination (default ~/.local/bin)")
	command.Flags().StringVar(rootDir, "root", "", "project root for project-scoped installs (default current directory)")
	command.Flags().BoolVar(dryRun, "dry-run", false, "show the installation plan without changing files")
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func KnownApplet(invokedAs string) bool {
	return filepath.Base(invokedAs) == "memorylint"
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, ErrFindings) {
		return 1
	}
	return 2
}

func ErrorText(err error) string {
	if err == nil || errors.Is(err, ErrFindings) {
		return ""
	}
	return err.Error()
}

func SortedItemIDs(c catalog.Catalog) []string {
	ids := make([]string, 0, len(c.Items))
	for _, item := range c.Items {
		ids = append(ids, item.ID)
	}
	sort.Strings(ids)
	return ids
}

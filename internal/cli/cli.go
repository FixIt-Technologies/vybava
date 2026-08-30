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
	"github.com/FixIt-Technologies/vybava/internal/perfrig"
	"github.com/FixIt-Technologies/vybava/internal/state"
	"github.com/FixIt-Technologies/vybava/internal/ui"
	"github.com/spf13/cobra"
)

var ErrFindings = errors.New("lint findings")

// ErrHookBlocked exits 2, which is how a Claude Code / Codex PreToolUse hook
// refuses the write. The hook prints its own reason, so ErrorText stays quiet.
var ErrHookBlocked = errors.New("hook blocked the write")

type App struct {
	Version string
	Stdout  io.Writer
	Stderr  io.Writer
	Stdin   io.Reader
}

type runtime struct {
	catalog   catalog.Catalog
	installer installer.Installer
	json      bool
	stdout    io.Writer
	stderr    io.Writer
	stdin     io.Reader
}

func (a App) Command(invokedAs string) (*cobra.Command, error) {
	if a.Stdout == nil {
		a.Stdout = os.Stdout
	}
	if a.Stderr == nil {
		a.Stderr = os.Stderr
	}
	if a.Stdin == nil {
		a.Stdin = os.Stdin
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
		catalog: c, stdout: a.Stdout, stderr: a.Stderr, stdin: a.Stdin,
		installer: installer.Installer{Payload: assets.FS, Store: store},
	}
	if filepath.Base(invokedAs) == "memorylint" {
		return rt.memorylintApplet(), nil
	}
	if filepath.Base(invokedAs) == "fontfreeze" {
		return rt.fontfreezeApplet(), nil
	}
	if filepath.Base(invokedAs) == "perfrig" {
		return rt.perfrigCommand("perfrig"), nil
	}
	if filepath.Base(invokedAs) == "shrt" {
		return rt.shrtApplet(), nil
	}
	if filepath.Base(invokedAs) == "press" {
		return rt.pressApplet(), nil
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
		rt.perfrigCommand("perfrig"),
		rt.shrtCommand("shrt [url...]"),
		rt.pressCommand("press"),
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
	rt.addMemoryActions(command)
	return command
}

// addMemoryActions attaches the write-side commands shared by `vybava memory`
// and the memorylint applet.
func (rt *runtime) addMemoryActions(command *cobra.Command) {
	command.AddCommand(rt.memoryFixCommand(), rt.memoryNewCommand(), rt.memoryReindexCommand(),
		rt.memoryGraphCommand(), rt.memoryRefsCommand(), rt.memoryHookCommand())
}

func (rt *runtime) memoryFixCommand() *cobra.Command {
	var dryRun bool
	command := &cobra.Command{
		Use:   "fix [memory-home...]",
		Short: "Normalize notes onto the flat v2 schema",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, homes []string) error {
			changed, failures, err := memorylint.Fix(homes, dryRun)
			if err != nil {
				return err
			}
			if rt.json {
				if err := writeJSON(rt.stdout, map[string]any{"changed": changed, "failures": failures, "dryRun": dryRun}); err != nil {
					return err
				}
			} else {
				verb := "FIXED"
				if dryRun {
					verb = "WOULD FIX"
				}
				for _, path := range changed {
					if _, err := fmt.Fprintf(rt.stdout, "%s %s\n", verb, path); err != nil {
						return err
					}
				}
				for _, failure := range failures {
					if _, err := fmt.Fprintf(rt.stderr, "SKIPPED %s\n", failure); err != nil {
						return err
					}
				}
				if _, err := fmt.Fprintf(rt.stdout, "memorylint: %d note(s) changed, %d failure(s)\n", len(changed), len(failures)); err != nil {
					return err
				}
			}
			if len(failures) > 0 {
				return ErrFindings
			}
			return nil
		},
	}
	command.Flags().BoolVar(&dryRun, "dry-run", false, "report without writing")
	return command
}

func (rt *runtime) memoryNewCommand() *cobra.Command {
	var home, noteType, name, description string
	var provisional bool
	command := &cobra.Command{
		Use:   "new",
		Short: "Scaffold a note that already satisfies the schema",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			path, err := memorylint.NewNote(home, noteType, name, description, provisional)
			if err != nil {
				return err
			}
			if rt.json {
				return writeJSON(rt.stdout, map[string]any{"path": path})
			}
			_, err = fmt.Fprintln(rt.stdout, path)
			return err
		},
	}
	command.Flags().StringVar(&home, "home", ".", "memory home to create the note in")
	command.Flags().StringVar(&noteType, "type", "", "user, feedback, project or reference")
	command.Flags().StringVar(&name, "name", "", "note slug, which is also its filename stem")
	command.Flags().StringVar(&description, "description", "", "one-line trigger description")
	command.Flags().BoolVar(&provisional, "provisional", false, "born provisional: status provisional with expires 60 days out")
	return command
}

func (rt *runtime) memoryReindexCommand() *cobra.Command {
	var write bool
	var teamIndex string
	command := &cobra.Command{
		Use:   "reindex <memory-home>",
		Short: "Render MEMORY.md deterministically from the notes in a home",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			rendered, err := memorylint.Reindex(args[0], teamIndex, write)
			if err != nil {
				return err
			}
			if rt.json {
				return writeJSON(rt.stdout, map[string]any{"index": string(rendered), "written": write})
			}
			if write {
				_, err = fmt.Fprintf(rt.stdout, "memorylint: wrote %s\n", filepath.Join(args[0], "MEMORY.md"))
				return err
			}
			_, err = rt.stdout.Write(rendered)
			return err
		},
	}
	command.Flags().BoolVar(&write, "write", false, "write MEMORY.md instead of printing it")
	command.Flags().StringVar(&teamIndex, "team-index", "", "path of the companion team index to route readers to")
	return command
}

func (rt *runtime) memoryGraphCommand() *cobra.Command {
	var similar bool
	command := &cobra.Command{
		Use:   "graph [memory-home...]",
		Short: "Print the wikilink graph, or likely duplicate pairs",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, homes []string) error {
			if rt.json {
				report, err := memorylint.GraphData(homes, similar)
				if err != nil {
					return err
				}
				return writeJSON(rt.stdout, report)
			}
			rendered, err := memorylint.Graph(homes, similar)
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(rt.stdout, rendered)
			return err
		},
	}
	command.Flags().BoolVar(&similar, "similar", false, "report likely duplicates instead of the graph")
	return command
}

func (rt *runtime) memoryRefsCommand() *cobra.Command {
	opts := memorylint.DefaultRefOptions()
	var failOn string
	command := &cobra.Command{
		Use:   "refs <memory-home> <file>...",
		Short: "Find references to notes that no longer exist, from outside the home",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			report, err := memorylint.Refs(args[0], args[1:], opts)
			if err != nil {
				return err
			}
			if rt.json {
				if err := writeJSON(rt.stdout, report); err != nil {
					return err
				}
			} else if _, err := fmt.Fprint(rt.stdout, memorylint.FormatText(report)); err != nil {
				return err
			}
			switch failOn {
			case "error":
				if report.Errors() > 0 {
					return ErrFindings
				}
			case "never":
			default:
				return fmt.Errorf("invalid --fail-on %q: use error or never", failOn)
			}
			return nil
		},
	}
	command.Flags().StringVar(&opts.Prefix, "prefix", opts.Prefix, "repo-relative path the home is referenced by")
	command.Flags().BoolVar(&opts.Bare, "bare", false, "also flag unqualified <note>.md names")
	command.Flags().StringVar(&failOn, "fail-on", "error", "error or never")
	return command
}

func (rt *runtime) memoryHookCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "hook",
		Short: "Run as a Claude Code or Codex pre/post-write hook",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			decision := memorylint.RunHook(rt.stdin)
			if !decision.Block {
				return nil
			}
			if _, err := fmt.Fprintln(rt.stderr, "memorylint "+decision.Message); err != nil {
				return err
			}
			return ErrHookBlocked
		},
	}
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
	rt.addMemoryActions(command)
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

// perfrigCommand wires the reusable performance-drill orchestrator. Domain
// logic lives in internal/perfrig; this is thin CLI glue per the architecture
// laws. Subcommands: validate, plan (dry look, runs nothing), run.
func (rt *runtime) perfrigCommand(use string) *cobra.Command {
	command := &cobra.Command{
		Use:   use,
		Short: "Run a performance drill from a testing/<project>/perf manifest",
		Long: "perfrig orchestrates the generic, safety-critical half of a load test — the neighbor guard,\n" +
			"the staged ramp (push-to-first-failure), and the percentile-vs-concurrency report — while each\n" +
			"project supplies its own seed/auth/generator commands via a perf.manifest.yml.",
	}

	validate := &cobra.Command{
		Use:   "validate <manifest>",
		Short: "Parse and validate a perf manifest",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			m, err := perfrig.LoadManifest(args[0])
			if err != nil {
				return err
			}
			if rt.json {
				return writeJSON(rt.stdout, m)
			}
			fmt.Fprintf(rt.stdout, "ok: %s (mode=%s, %d generator(s), %d stage(s))\n",
				m.Project, m.Mode, len(m.Generators), len(m.Ramp.Stages))
			return nil
		},
	}

	plan := &cobra.Command{
		Use:   "plan <manifest>",
		Short: "Print the resolved drill (stages + commands) without running anything",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			m, err := perfrig.LoadManifest(args[0])
			if err != nil {
				return err
			}
			if rt.json {
				return writeJSON(rt.stdout, struct {
					Manifest perfrig.Manifest    `json:"manifest"`
					Stages   []perfrig.StagePlan `json:"stages"`
				}{m, perfrig.PlanRamp(m)})
			}
			fmt.Fprint(rt.stdout, perfrig.Runner{M: m}.Plan())
			return nil
		},
	}

	var maxStage int
	var reportOut string
	run := &cobra.Command{
		Use:   "run <manifest>",
		Short: "Execute the drill: seed → (rig up) → guarded ramp → report",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := perfrig.LoadManifest(args[0])
			if err != nil {
				return err
			}
			// Under --json the live stream moves to stderr so stdout carries
			// exactly one machine-readable document: the drill object.
			streamOut := rt.stdout
			if rt.json {
				streamOut = rt.stderr
			}
			runner := perfrig.Runner{M: m, Opt: perfrig.Options{
				Stdout:   streamOut,
				Stderr:   rt.stderr,
				MaxStage: maxStage,
			}}
			drill, err := runner.Run(cmd.Context())
			if err != nil {
				return err
			}
			report := drill.Markdown()
			if rt.json {
				if jerr := writeJSON(rt.stdout, drill); jerr != nil {
					return jerr
				}
			} else {
				fmt.Fprint(rt.stdout, "\n"+report)
			}
			targets := []string{}
			if reportOut != "" {
				targets = append(targets, reportOut)
			}
			// The manifest's report.out is a directory; each run drops a
			// timestamped artifact there.
			if m.Report.Out != "" {
				dir, derr := expandHome(m.Report.Out)
				if derr != nil {
					return derr
				}
				if derr := os.MkdirAll(dir, 0o755); derr != nil {
					return fmt.Errorf("report.out %q must be a writable directory: %w", dir, derr)
				}
				targets = append(targets, filepath.Join(dir,
					fmt.Sprintf("%s-%s.md", m.Project, drill.StartedAt.Format("20060102-150405"))))
			}
			for _, t := range targets {
				if werr := os.WriteFile(t, []byte(report), 0o644); werr != nil {
					return werr
				}
				fmt.Fprintf(rt.stderr, "report written to %s\n", t)
			}
			return nil
		},
	}
	run.Flags().IntVar(&maxStage, "max-stage", 0, "run only the first N stages (0 = whole ramp); use for calibration")
	run.Flags().StringVar(&reportOut, "report-out", "", "also write the markdown report to this path")

	command.AddCommand(validate, plan, run)
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

// expandHome resolves a leading "~/" (or bare "~") against the user's home
// directory so manifest paths like ~/Exports/... land where they promise.
func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~")), nil
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
	if err == nil || errors.Is(err, ErrFindings) || errors.Is(err, ErrHookBlocked) {
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

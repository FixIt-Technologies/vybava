package cli

import (
	"os"

	"github.com/FixIt-Technologies/vybava/internal/hotfix"
	"github.com/FixIt-Technologies/vybava/internal/runx"
	"github.com/spf13/cobra"
)

func (rt *runtime) hotfixApplet() *cobra.Command {
	command := rt.hotfixCommand("hotfix")
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetOut(rt.stdout)
	command.SetErr(rt.stderr)
	command.PersistentFlags().BoolVar(&rt.json, "json", false, "emit the versioned envelope as JSON")
	return command
}

// hotfixCommand wires the applet verbs. Every verb runs through one
// envelope session: the verb returns a Result or an error, the session
// emits exactly one envelope and owns the exit code (0 ok, 1 infra, 2
// diagnostics).
func (rt *runtime) hotfixCommand(use string) *cobra.Command {
	command := &cobra.Command{
		Use:   use,
		Short: "Release-lineage hotfixes: branch from the tag production runs, deploy from it, forward-port to main",
		Long: "hotfix keeps trunk-based development intact when production needs a fix\n" +
			"the trunk cannot ship: it cuts <prefix><slug> from the latest release\n" +
			"tag in an isolated worktree, opens the PR to main that doubles as CI gate\n" +
			"and forward-port, dispatches the production workflow ON the branch, and\n" +
			"merges once the head has shipped. State is re-derived from git + gh.",
	}
	cwd := func() string {
		wd, err := os.Getwd()
		if err != nil {
			return "."
		}
		return wd
	}
	session := func(cmd *cobra.Command) *runx.Session {
		return &runx.Session{Tool: "hotfix", JSON: rt.json, Verb: cmd.Name(), Stdout: rt.stdout, Stderr: rt.stderr}
	}
	finish := func(s *runx.Session, res hotfix.Result, err error) error {
		if err == nil {
			err = s.Emit(runx.Envelope{OK: true, Data: res.Data, Diagnostics: res.Diagnostics, Next: res.Next})
		} else if res.Data != nil {
			// A verb that failed mid-flight still owes its partial state.
			_ = s.Emit(runx.Envelope{OK: false, Data: res.Data, Diagnostics: res.Diagnostics, Next: res.Next})
		}
		if code := s.Finish(err); code != 0 {
			return runx.ExitError{Code: code}
		}
		return nil
	}
	open := func() (*hotfix.Tool, error) { return hotfix.Open(hotfix.ExecRunner{}, cwd()) }
	slugArg := func(t *hotfix.Tool, args []string) (string, error) {
		if len(args) > 0 {
			return args[0], nil
		}
		return t.InferSlug(cwd())
	}

	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Write a commented hotfix.yaml into the primary checkout",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := session(cmd)
			res, err := hotfix.Init(hotfix.ExecRunner{}, cwd())
			return finish(s, res, err)
		},
	}

	var from string
	startCmd := &cobra.Command{
		Use:   "start <slug>",
		Short: "Cut the hotfix branch from the latest release tag and create its worktree",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := session(cmd)
			t, err := open()
			if err != nil {
				return finish(s, hotfix.Result{}, err)
			}
			res, err := t.Start(args[0], from)
			return finish(s, res, err)
		},
	}
	startCmd.Flags().StringVar(&from, "from", "", "release tag to hotfix (default: highest stable tag)")

	var noFetch bool
	statusCmd := &cobra.Command{
		Use:   "status [slug]",
		Short: "Re-derive the hotfix state and the exact next commands",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := session(cmd)
			t, err := open()
			if err != nil {
				return finish(s, hotfix.Result{}, err)
			}
			slug, err := slugArg(t, args)
			if err != nil {
				return finish(s, hotfix.Result{}, err)
			}
			res, err := t.Status(slug, !noFetch)
			return finish(s, res, err)
		},
	}
	statusCmd.Flags().BoolVar(&noFetch, "no-fetch", false, "skip git fetch (offline view of the last known state)")

	var title string
	prCmd := &cobra.Command{
		Use:   "pr [slug]",
		Short: "Push the branch and open (or reuse) the PR to main — the CI gate and forward-port",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := session(cmd)
			t, err := open()
			if err != nil {
				return finish(s, hotfix.Result{}, err)
			}
			slug, err := slugArg(t, args)
			if err != nil {
				return finish(s, hotfix.Result{}, err)
			}
			res, err := t.PR(slug, title)
			return finish(s, res, err)
		},
	}
	prCmd.Flags().StringVar(&title, "title", "", "PR title (default: hotfix(<base tag>): <slug>)")

	var watch, force bool
	deployCmd := &cobra.Command{
		Use:   "deploy [slug]",
		Short: "Dispatch the production workflow on the hotfix branch",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := session(cmd)
			t, err := open()
			if err != nil {
				return finish(s, hotfix.Result{}, err)
			}
			slug, err := slugArg(t, args)
			if err != nil {
				return finish(s, hotfix.Result{}, err)
			}
			res, err := t.Deploy(slug, watch, force)
			return finish(s, res, err)
		},
	}
	deployCmd.Flags().BoolVar(&watch, "watch", false, "stream the run until it completes (progress on stderr)")
	deployCmd.Flags().BoolVar(&force, "force", false, "deploy without a PR or over failing checks")

	forwardCmd := &cobra.Command{
		Use:   "forward [slug]",
		Short: "Cherry-pick the hotfix onto main in a separate worktree (when the PR merge conflicts)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := session(cmd)
			t, err := open()
			if err != nil {
				return finish(s, hotfix.Result{}, err)
			}
			slug, err := slugArg(t, args)
			if err != nil {
				return finish(s, hotfix.Result{}, err)
			}
			res, err := t.Forward(slug)
			return finish(s, res, err)
		},
	}

	var finishForce bool
	finishCmd := &cobra.Command{
		Use:   "finish [slug]",
		Short: "Merge the PR after the head shipped — the merge commit is the forward-port",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := session(cmd)
			t, err := open()
			if err != nil {
				return finish(s, hotfix.Result{}, err)
			}
			slug, err := slugArg(t, args)
			if err != nil {
				return finish(s, hotfix.Result{}, err)
			}
			res, err := t.Finish(slug, finishForce)
			return finish(s, res, err)
		},
	}
	finishCmd.Flags().BoolVar(&finishForce, "force", false, "merge even though the branch head has not shipped")

	command.AddCommand(initCmd, startCmd, statusCmd, prCmd, deployCmd, forwardCmd, finishCmd)
	return command
}

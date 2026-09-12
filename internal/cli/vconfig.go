package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/henderson-tech/vybava/internal/configdiscover"
	"github.com/henderson-tech/vybava/internal/runx"
	"github.com/henderson-tech/vybava/internal/vconfig"
	"github.com/spf13/cobra"
)

// Closed diagnostic codes for `vybava config`.
const (
	// configDiagMissing — no vybava.config.* above cwd.
	configDiagMissing = "CONFIG_MISSING"
	// configDiagInvalid — the file exists but does not evaluate to JSON.
	configDiagInvalid = "CONFIG_INVALID"
	// configDiagHelperDrift — .vybava/config.ts differs from this binary's helpers.
	configDiagHelperDrift = "HELPER_DRIFT"
	// configDiagExists — init would overwrite an existing config.
	configDiagExists = "CONFIG_EXISTS"
)

// configCommand is the root-level `vybava config` verb family: the shared
// per-repo configuration every applet reads through internal/vconfig.
func (rt *runtime) configCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "config",
		Short: "The shared per-repo vybava.config.ts — init, check, show",
	}
	session := func(cmd *cobra.Command) *runx.Session {
		return &runx.Session{Tool: "vybava", JSON: rt.json, Verb: "config " + cmd.Name(), Stdout: rt.stdout, Stderr: rt.stderr}
	}
	finish := func(s *runx.Session, data any, next []string, code, detail, fix string) error {
		env := runx.Envelope{OK: code == "", Verb: s.Verb, Data: data, Diagnostics: []runx.Diagnostic{}, Next: next}
		if env.Next == nil {
			env.Next = []string{}
		}
		var err error
		if code != "" {
			env.Diagnostics = append(env.Diagnostics, runx.Diagnostic{Code: code, Severity: "error", Detail: detail, Fix: fix})
			if fix != "" {
				env.Next = append(env.Next, fix)
			}
			err = &runx.DiagError{Diag: env.Diagnostics[0]}
		}
		if emitErr := s.Emit(env); emitErr != nil {
			return emitErr
		}
		if c := s.Finish(err); c != 0 {
			return runx.ExitError{Code: c}
		}
		return nil
	}

	var force bool
	initCmd := &cobra.Command{
		Use: "init", Short: "Scaffold vybava.config.ts + the generated .vybava/config.ts helpers in the current directory", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := session(cmd)
			root := workingDir()
			cfgPath := filepath.Join(root, vconfig.FileTS)
			wrote := []string{}
			// An existing config is the repo's; --force only refreshes the generated helpers.
			if _, err := os.Stat(cfgPath); err != nil {
				if err := os.WriteFile(cfgPath, []byte(vconfig.StarterConfig), 0o644); err != nil {
					return finish(s, nil, nil, runx.DiagInfraError, err.Error(), "")
				}
				wrote = append(wrote, vconfig.FileTS)
			}
			written, err := vconfig.WriteHelpers(root, force)
			if errors.Is(err, vconfig.ErrHelperDrift) {
				return finish(s, map[string]any{"written": wrote}, nil, configDiagHelperDrift, err.Error(), "vybava config init --force")
			}
			if err != nil {
				return finish(s, nil, nil, runx.DiagInfraError, err.Error(), "")
			}
			if written {
				wrote = append(wrote, filepath.Join(vconfig.HelperDir, vconfig.HelperFile))
			}
			return finish(s, map[string]any{"written": wrote}, []string{"vybava config check --json"}, "", "", "")
		},
	}
	initCmd.Flags().BoolVar(&force, "force", false, "rewrite the generated .vybava/config.ts helpers (never the config itself)")
	root.AddCommand(initCmd)
	var write bool
	discoverCmd := &cobra.Command{
		Use: "discover", Short: "Suggest guards and locale catalogs from tracked files", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := session(cmd)
			root := workingDir()
			cfg, loadErr := vconfig.Load(root)
			if loadErr == nil {
				root = cfg.Root
			} else if !errors.Is(loadErr, vconfig.ErrNotFound) {
				return finish(s, nil, nil, configDiagInvalid, loadErr.Error(), "")
			}
			r, err := configdiscover.Discover(root)
			if err != nil {
				return finish(s, nil, nil, runx.DiagInfraError, err.Error(), "")
			}
			written := []string{}
			if write {
				if loadErr != nil {
					return finish(s, nil, nil, configDiagMissing, loadErr.Error(), "vybava config init")
				}
				written, err = r.WriteAbsent(cfg)
				if err != nil {
					return finish(s, nil, nil, configDiagInvalid, err.Error(), "")
				}
			}
			if !rt.json {
				if _, err := fmt.Fprint(rt.stdout, r.Snippet()); err != nil {
					return err
				}
				for _, warning := range r.Warnings {
					fmt.Fprintln(rt.stderr, warning)
				}
				if write {
					fmt.Fprintln(rt.stderr, "Added sections:", written)
				}
				return nil
			}
			return finish(s, struct {
				Discovery configdiscover.Result `json:"discovery"`
				Written   []string              `json:"written"`
			}{r, written}, nil, "", "", "")
		},
	}
	discoverCmd.Flags().BoolVar(&write, "write", false, "fill only absent top-level config sections")
	root.AddCommand(discoverCmd)

	root.AddCommand(&cobra.Command{
		Use: "check", Short: "Evaluate the config and verify the helpers match this binary — CI gate", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := session(cmd)
			cfg, err := vconfig.Load(workingDir())
			if errors.Is(err, vconfig.ErrNotFound) {
				return finish(s, nil, nil, configDiagMissing, err.Error(), "vybava config init")
			}
			if err != nil {
				return finish(s, nil, nil, configDiagInvalid, err.Error(), "")
			}
			sections := make([]string, 0, len(cfg.Sections))
			for k := range cfg.Sections {
				sections = append(sections, k)
			}
			data := map[string]any{"path": cfg.Path, "sections": sections}
			if filepath.Ext(cfg.Path) == ".ts" {
				if err := vconfig.CheckHelpers(cfg.Root); err != nil {
					return finish(s, data, nil, configDiagHelperDrift, err.Error(), "vybava config init --force")
				}
			}
			r, err := configdiscover.Discover(cfg.Root)
			if err != nil {
				return finish(s, data, nil, runx.DiagInfraError, err.Error(), "")
			}
			warnings, err := r.Drift(cfg)
			if err != nil {
				return finish(s, data, nil, configDiagInvalid, err.Error(), "")
			}
			diagnostics := []runx.Diagnostic{}
			for _, warning := range warnings {
				diagnostics = append(diagnostics, runx.Diagnostic{Code: "DISCOVERY_DRIFT", Severity: "warning", Detail: warning, Fix: "vybava config discover"})
			}
			return s.Emit(runx.Envelope{OK: true, Verb: s.Verb, Data: data, Diagnostics: diagnostics, Next: []string{}})
		},
	})

	root.AddCommand(&cobra.Command{
		Use: "show", Short: "Print the evaluated config as JSON", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := session(cmd)
			cfg, err := vconfig.Load(workingDir())
			if errors.Is(err, vconfig.ErrNotFound) {
				return finish(s, nil, nil, configDiagMissing, err.Error(), "vybava config init")
			}
			if err != nil {
				return finish(s, nil, nil, configDiagInvalid, err.Error(), "")
			}
			return finish(s, cfg.Sections, nil, "", "", "")
		},
	})
	return root
}

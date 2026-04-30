package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pedromvgomes/gt/internal/clone"
	"github.com/pedromvgomes/gt/internal/config"
	"github.com/pedromvgomes/gt/internal/git"
	"github.com/pedromvgomes/gt/internal/scratch"
	"github.com/pedromvgomes/gt/internal/setauth"
	"github.com/pedromvgomes/gt/internal/setup"
	"github.com/pedromvgomes/gt/internal/ui"
	"github.com/pedromvgomes/gt/internal/update"
	"github.com/pedromvgomes/gt/internal/worktree"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

type options struct {
	verbose      bool
	quiet        bool
	noColor      bool
	cfg          config.Config
	ui           *ui.UI
	updateCheck  chan *update.Available
	updateCancel context.CancelFunc
}

func main() {
	cmd := newRootCommand()
	if err := cmd.Execute(); err != nil {
		printer := ui.New(os.Stdin, os.Stdout, os.Stderr, false, false)
		printer.Error("%v", err)
		os.Exit(ui.ExitCode(err))
	}
}

func newRootCommand() *cobra.Command {
	opts := &options{}
	root := &cobra.Command{
		Use:                "gt",
		Short:              "Small Go CLI for bare-repo + worktree git workflows",
		SilenceUsage:       true,
		SilenceErrors:      true,
		Version:            fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
		PersistentPreRunE:  prepareOpts(opts, true),
		PersistentPostRunE: postRun(opts),
	}

	root.PersistentFlags().BoolVarP(&opts.verbose, "verbose", "v", false, "print debug diagnostics")
	root.PersistentFlags().BoolVarP(&opts.quiet, "quiet", "q", false, "suppress info and success output")
	root.PersistentFlags().BoolVar(&opts.noColor, "no-color", false, "disable ANSI color output")
	root.AddCommand(newCloneCommand(opts))
	root.AddCommand(newWorktreeCommand(opts))
	root.AddCommand(newScratchCommand(opts))
	root.AddCommand(newSetAuthCommand(opts))
	root.AddCommand(newConfigCommand(opts))
	root.AddCommand(newSetupCommand(opts))
	root.AddCommand(newUpdateCommand(opts))
	return root
}

func newSetupCommand(opts *options) *cobra.Command {
	var (
		names   string
		noSetup bool
		yes     bool
		from    string
		show    bool
		dryRun  bool
	)
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Run configured setup templates against the current git repository",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("resolve current directory: %w", err)
			}
			rcontext, err := setup.DetectFromCWD(context.Background(), git.ExecRunner{}, cwd)
			if err != nil {
				return err
			}
			selectOpts := setup.SelectOptions{
				NoSetup: noSetup,
				Names:   parseNames(names),
			}
			templates, err := setup.Select(opts.cfg.Setup, rcontext.RepoURL, selectOpts)
			if err != nil {
				return err
			}
			plan := setup.Plan{Templates: templates, Ctx: rcontext}
			return setup.Execute(context.Background(), opts.ui, plan, setup.RunOptions{
				Yes: yes, Show: show, DryRun: dryRun, From: from,
			})
		},
	}
	cmd.Flags().StringVar(&names, "setup", "", "comma-separated template names to run (overrides match)")
	cmd.Flags().BoolVar(&noSetup, "no-setup", false, "skip all templates")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().StringVar(&from, "from", "", "resume from this template name")
	cmd.Flags().BoolVar(&show, "show", false, "show full template bodies before prompting")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan without executing it")
	cmd.MarkFlagsMutuallyExclusive("setup", "no-setup")
	return cmd
}

func parseNames(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if name := strings.TrimSpace(p); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func prepareOpts(opts *options, loadConfig bool) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		opts.ui = ui.New(os.Stdin, os.Stdout, os.Stderr, opts.noColor, opts.quiet)
		level := slog.LevelWarn
		if opts.verbose {
			level = slog.LevelDebug
		}
		if opts.quiet {
			level = slog.LevelError
		}
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
		if !loadConfig {
			return nil
		}
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve current directory: %w", err)
		}
		cfg, err := config.Load(cwd)
		if err != nil {
			return err
		}
		opts.cfg = cfg
		startBackgroundUpdateCheck(cmd, opts)
		return nil
	}
}

func startBackgroundUpdateCheck(cmd *cobra.Command, opts *options) {
	if cmd.Name() == "update" {
		return
	}
	if !opts.cfg.AutoUpdate.Enabled {
		return
	}
	if ok, _ := update.Eligible(version, opts.ui.Interactive); !ok {
		return
	}
	exe, _ := os.Executable()
	if reason := update.SkipReason(exe); reason != "" {
		return
	}
	statePath, err := update.StatePath()
	if err != nil {
		return
	}
	state, _ := update.LoadState(statePath)
	if !update.DueForCheck(state, time.Now(), opts.cfg.AutoUpdate.CheckInterval) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	ch := make(chan *update.Available, 1)
	opts.updateCheck = ch
	opts.updateCancel = cancel
	go func() {
		defer cancel()
		available, err := update.Check(ctx, version, update.Options{})
		state.LastCheckedAt = time.Now()
		if available != nil {
			state.LatestSeen = available.Latest
			state.LatestSeenAtRaw = time.Now()
		}
		_ = update.SaveState(statePath, state)
		if err != nil {
			ch <- nil
			return
		}
		ch <- available
	}()
}

func postRun(opts *options) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		if opts.updateCheck == nil {
			return nil
		}
		defer func() {
			if opts.updateCancel != nil {
				opts.updateCancel()
			}
		}()
		select {
		case available := <-opts.updateCheck:
			if available == nil {
				return nil
			}
			opts.ui.Info("gt v%s is available (you're on v%s). Run 'gt update' to install.", available.Latest, available.Current)
		case <-time.After(50 * time.Millisecond):
			// Don't block command exit on a slow check.
		}
		return nil
	}
}

func newUpdateCommand(opts *options) *cobra.Command {
	var (
		checkOnly bool
		yes       bool
	)
	cmd := &cobra.Command{
		Use:   "update [--check] [--yes]",
		Short: "Check for and install a newer release of gt",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUpdate(cmd.Context(), opts, checkOnly, yes)
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "only report whether a newer version is available")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func runUpdate(ctx context.Context, opts *options, checkOnly, yes bool) error {
	if version == "dev" {
		return ui.Errorf(ui.ExitUser, "running a development build; install a release to enable updates")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve current executable: %w", err)
	}
	if reason := update.SkipReason(exe); reason != "" {
		return ui.Errorf(ui.ExitUser, "%s; update through that channel instead", reason)
	}

	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	available, err := update.Check(checkCtx, version, update.Options{})
	if err != nil {
		return err
	}

	statePath, _ := update.StatePath()
	if statePath != "" {
		state, _ := update.LoadState(statePath)
		state.LastCheckedAt = time.Now()
		if available != nil {
			state.LatestSeen = available.Latest
			state.LatestSeenAtRaw = time.Now()
		}
		_ = update.SaveState(statePath, state)
	}

	if available == nil {
		opts.ui.Success("gt v%s is up to date", strings.TrimPrefix(version, "v"))
		return nil
	}
	if checkOnly {
		opts.ui.Info("gt v%s is available (you're on v%s)", available.Latest, available.Current)
		return nil
	}

	if !yes {
		answer, err := opts.ui.Prompt(
			fmt.Sprintf("Update gt v%s -> v%s?", available.Current, available.Latest),
			"Y", false, "re-run with --yes",
		)
		if err != nil {
			return err
		}
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "" && a != "y" && a != "yes" {
			return ui.Errorf(ui.ExitUser, "update aborted")
		}
	}

	if err := update.Apply(ctx, opts.ui, available, update.Options{}); err != nil {
		return err
	}
	opts.ui.Success("gt updated to v%s", available.Latest)
	return nil
}

func newCloneCommand(opts *options) *cobra.Command {
	var cloneOpts clone.Options
	var setupNames string
	cmd := &cobra.Command{
		Use:   "clone <url> [folder]",
		Short: "Clone a repository into a bare repo + worktree layout",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) < 1 {
				return ui.Errorf(ui.ExitUser, "repository URL is required")
			}
			if len(args) > 2 {
				return ui.Errorf(ui.ExitUser, "expected at most 2 arguments, got %d", len(args))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cloneOpts.RepoURL = args[0]
			if len(args) == 2 {
				cloneOpts.Folder = args[1]
			}
			cloneOpts.SetupNames = parseNames(setupNames)
			return clone.Run(context.Background(), git.ExecRunner{}, opts.ui, opts.cfg, cloneOpts)
		},
	}
	cmd.Flags().BoolVar(&cloneOpts.ForceSSH, "ssh", false, "force HTTPS repository URLs to SSH")
	cmd.Flags().BoolVar(&cloneOpts.NoSSH, "no-ssh", false, "keep HTTPS repository URLs without prompting")
	cmd.Flags().BoolVar(&cloneOpts.NoSetupAuth, "no-setup-auth", false, "skip post-clone direnv authentication setup")
	cmd.Flags().StringVar(&cloneOpts.User, "user", "", "GitHub user for post-clone direnv authentication setup")
	cmd.Flags().StringVar(&setupNames, "setup", "", "comma-separated setup template names to run after clone")
	cmd.Flags().BoolVar(&cloneOpts.NoSetup, "no-setup", false, "skip post-clone setup templates")
	cmd.Flags().BoolVar(&cloneOpts.YesSetup, "yes", false, "skip the setup confirmation prompt")
	cmd.Flags().BoolVar(&cloneOpts.ShowSetup, "show-setup", false, "show full template bodies before prompting")
	cmd.Flags().BoolVar(&cloneOpts.DryRunSetup, "dry-run-setup", false, "print the setup plan without executing it")
	cmd.MarkFlagsMutuallyExclusive("ssh", "no-ssh")
	cmd.MarkFlagsMutuallyExclusive("setup", "no-setup")
	return cmd
}

func newWorktreeCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "wt",
		Aliases: []string{"worktree"},
		Short:   "Manage gt worktrees",
	}
	cmd.AddCommand(newWorktreeAddCommand(opts))
	cmd.AddCommand(newWorktreeRemoveCommand(opts))
	cmd.AddCommand(newWorktreeListCommand(opts))
	cmd.AddCommand(newWorktreeNukeCommand(opts))
	cmd.AddCommand(newWorktreePruneBranchesCommand(opts))
	return cmd
}

func newWorktreeAddCommand(opts *options) *cobra.Command {
	var addOpts worktree.AddOptions
	cmd := &cobra.Command{
		Use:   "add <type/name>",
		Short: "Create a worktree for a typed branch",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return ui.Errorf(ui.ExitUser, "expected exactly one <type/name> argument")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("resolve current directory: %w", err)
			}
			addOpts.Spec = args[0]
			addOpts.CWD = cwd
			return worktree.Add(context.Background(), git.ExecRunner{}, opts.ui, opts.cfg, addOpts)
		},
	}
	cmd.Flags().StringVar(&addOpts.From, "from", "", "create the branch from this source branch")
	return cmd
}

func newWorktreeRemoveCommand(opts *options) *cobra.Command {
	var rmOpts worktree.RemoveOptions
	cmd := &cobra.Command{
		Use:     "rm [--branch] <name>",
		Aliases: []string{"remove", "delete"},
		Short:   "Remove a worktree by name",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return ui.Errorf(ui.ExitUser, "expected exactly one <name> argument")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("resolve current directory: %w", err)
			}
			rmOpts.Name = args[0]
			rmOpts.CWD = cwd
			return worktree.Remove(context.Background(), git.ExecRunner{}, opts.ui, opts.cfg, rmOpts)
		},
	}
	cmd.Flags().BoolVarP(&rmOpts.DeleteBranch, "branch", "b", false, "also delete the local branch")
	return cmd
}

func newWorktreeListCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List gt worktrees",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("resolve current directory: %w", err)
			}
			return worktree.List(context.Background(), git.ExecRunner{}, opts.ui, opts.cfg, cwd)
		},
	}
}

func newWorktreeNukeCommand(opts *options) *cobra.Command {
	var nukeOpts worktree.NukeOptions
	cmd := &cobra.Command{
		Use:   "nuke [--branches]",
		Short: "Remove all typed worktrees",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("resolve current directory: %w", err)
			}
			nukeOpts.CWD = cwd
			return worktree.Nuke(context.Background(), git.ExecRunner{}, opts.ui, opts.cfg, nukeOpts)
		},
	}
	cmd.Flags().BoolVarP(&nukeOpts.DeleteBranches, "branches", "b", false, "also delete corresponding local branches")
	return cmd
}

func newWorktreePruneBranchesCommand(opts *options) *cobra.Command {
	var pruneOpts worktree.PruneBranchesOptions
	cmd := &cobra.Command{
		Use:   "prune-branches [--dry-run]",
		Short: "Delete local branches without active worktrees",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("resolve current directory: %w", err)
			}
			pruneOpts.CWD = cwd
			return worktree.PruneBranches(context.Background(), git.ExecRunner{}, opts.ui, pruneOpts)
		},
	}
	cmd.Flags().BoolVarP(&pruneOpts.DryRun, "dry-run", "n", false, "show what would be deleted without deleting")
	return cmd
}

func newScratchCommand(opts *options) *cobra.Command {
	var scratchOpts scratch.Options
	cmd := &cobra.Command{
		Use:   "scratch [--reset|--delete]",
		Short: "Create or manage the scratch worktree",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("resolve current directory: %w", err)
			}
			scratchOpts.CWD = cwd
			return scratch.Run(context.Background(), git.ExecRunner{}, opts.ui, scratchOpts)
		},
	}
	cmd.Flags().BoolVarP(&scratchOpts.Reset, "reset", "r", false, "reset scratch to a different branch")
	cmd.Flags().BoolVarP(&scratchOpts.Delete, "delete", "d", false, "remove the scratch worktree and scratch branch")
	return cmd
}

func newSetAuthCommand(opts *options) *cobra.Command {
	var authOpts setauth.Options
	cmd := &cobra.Command{
		Use:   "set-auth [--user <name>]",
		Short: "Scope gh authentication to this gt-managed repository with direnv",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("resolve current directory: %w", err)
			}
			authOpts.CWD = cwd
			return setauth.Run(context.Background(), setauth.ExecRunner{}, opts.ui, authOpts)
		},
	}
	cmd.Flags().StringVar(&authOpts.User, "user", "", "GitHub user to bind GH_TOKEN to")
	return cmd
}

func newConfigCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and manage the gt configuration file",
		// Skip auto-load so 'config validate' can surface config errors.
		PersistentPreRunE: prepareOpts(opts, false),
	}
	cmd.AddCommand(newConfigPathCommand(opts))
	cmd.AddCommand(newConfigShowCommand(opts))
	cmd.AddCommand(newConfigValidateCommand(opts))
	cmd.AddCommand(newConfigEditCommand(opts))
	return cmd
}

func newConfigPathCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the absolute path to the global config file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.GlobalPath()
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(opts.ui.Out, path)
			return nil
		},
	}
}

func newConfigShowCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the contents of the global config file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.GlobalPath()
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					return ui.Errorf(ui.ExitUser, "config file does not exist at %s; run any gt command to bootstrap it", path)
				}
				return fmt.Errorf("read config %s: %w", path, err)
			}
			_, _ = opts.ui.Out.Write(data)
			return nil
		},
	}
}

func newConfigValidateCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Parse and validate the global config file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("resolve current directory: %w", err)
			}
			if _, err := config.Load(cwd); err != nil {
				return err
			}
			opts.ui.Success("config is valid")
			return nil
		},
	}
}

func newConfigEditCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Edit the global config in $VISUAL/$EDITOR",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runConfigEdit(opts)
		},
	}
}

func runConfigEdit(opts *options) error {
	path, err := config.GlobalPath()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve current directory: %w", err)
	}
	// Bootstrap the config file if it does not exist (Load handles the seed).
	if _, err := config.Load(cwd); err != nil {
		// Even if the existing config is invalid, we still want to edit it.
		opts.ui.Warn("current config has issues: %v", err)
	}

	editor := pickEditor()
	if editor == "" {
		return ui.Errorf(ui.ExitUser, "no editor configured; set $VISUAL or $EDITOR")
	}

	original, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "config-*.yaml")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(original); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	for {
		if err := runEditor(editor, tmpPath); err != nil {
			return err
		}
		data, err := os.ReadFile(tmpPath)
		if err != nil {
			return fmt.Errorf("read edited file: %w", err)
		}
		if validateConfigBytes(data) == nil {
			if err := os.WriteFile(path, data, 0o644); err != nil {
				return fmt.Errorf("write config: %w", err)
			}
			opts.ui.Success("config saved")
			return nil
		}
		verr := validateConfigBytes(data)
		opts.ui.Error("config is invalid: %v", verr)
		answer, err := opts.ui.Prompt("Re-open editor? [Y/n]", "Y", false, "")
		if err != nil {
			return err
		}
		if !strings.EqualFold(strings.TrimSpace(answer), "y") && strings.TrimSpace(answer) != "" {
			return ui.Errorf(ui.ExitUser, "config edit aborted; original file unchanged")
		}
	}
}

func pickEditor() string {
	for _, key := range []string{"VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

func runEditor(editor, path string) error {
	cmd := exec.Command("sh", "-c", editor+" "+shellQuote(path))
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func validateConfigBytes(data []byte) error {
	cfg := config.Default()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return err
	}
	if cfg.SSH.HostAliases == nil {
		cfg.SSH.HostAliases = map[string]string{}
	}
	return config.Validate(cfg)
}

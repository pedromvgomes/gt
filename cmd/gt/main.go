package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/pedromvgomes/gt/internal/clone"
	"github.com/pedromvgomes/gt/internal/config"
	"github.com/pedromvgomes/gt/internal/git"
	"github.com/pedromvgomes/gt/internal/scratch"
	"github.com/pedromvgomes/gt/internal/ui"
	"github.com/pedromvgomes/gt/internal/worktree"
	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

type options struct {
	verbose bool
	quiet   bool
	noColor bool
	cfg     config.Config
	ui      *ui.UI
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
		Use:           "gt",
		Short:         "Small Go CLI for bare-repo + worktree git workflows",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			opts.ui = ui.New(os.Stdin, os.Stdout, os.Stderr, opts.noColor, opts.quiet)
			level := slog.LevelWarn
			if opts.verbose {
				level = slog.LevelDebug
			}
			if opts.quiet {
				level = slog.LevelError
			}
			slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("resolve current directory: %w", err)
			}
			cfg, err := config.Load(cwd)
			if err != nil {
				return err
			}
			opts.cfg = cfg
			return nil
		},
	}

	root.PersistentFlags().BoolVarP(&opts.verbose, "verbose", "v", false, "print debug diagnostics")
	root.PersistentFlags().BoolVarP(&opts.quiet, "quiet", "q", false, "suppress info and success output")
	root.PersistentFlags().BoolVar(&opts.noColor, "no-color", false, "disable ANSI color output")
	root.AddCommand(newCloneCommand(opts))
	root.AddCommand(newWorktreeCommand(opts))
	root.AddCommand(newScratchCommand(opts))
	return root
}

func newCloneCommand(opts *options) *cobra.Command {
	var cloneOpts clone.Options
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
			return clone.Run(context.Background(), git.ExecRunner{}, opts.ui, opts.cfg, cloneOpts)
		},
	}
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

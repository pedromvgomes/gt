package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/pedromvgomes/gt/internal/clone"
	"github.com/pedromvgomes/gt/internal/config"
	"github.com/pedromvgomes/gt/internal/git"
	"github.com/pedromvgomes/gt/internal/ui"
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

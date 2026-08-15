package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/pedromvgomes/gt/internal/repogov"
	"github.com/pedromvgomes/gt/internal/repospec"
	"github.com/pedromvgomes/gt/internal/ui"
	"github.com/spf13/cobra"
)

func newRepoSettingsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "settings",
		Short: "Diff or apply the GitHub settings gt manages",
		Long: "Branch protection, merge methods and the single required status check.\n" +
			"These cannot be expressed as committed files, so gt drives them through the\n" +
			"GitHub API using your existing gh credentials.",
	}
	cmd.AddCommand(newRepoSettingsDiffCommand(opts))
	cmd.AddCommand(newRepoSettingsApplyCommand(opts))
	return cmd
}

func newRepoSettingsDiffCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "diff",
		Short: "Show how live GitHub settings differ from " + repospec.FileName,
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			govOpts, spec, err := loadSpecContext()
			if err != nil {
				return err
			}
			changes, err := repogov.SettingsDiff(context.Background(), repogov.ExecGH{},
				spec, govOpts.RepoOwner, govOpts.RepoName)
			if err != nil {
				return err
			}
			printSettingChanges(opts.ui, changes, govOpts)
			return nil
		},
	}
}

func newRepoSettingsApplyCommand(opts *options) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Push the GitHub settings declared in " + repospec.FileName,
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			govOpts, spec, err := loadSpecContext()
			if err != nil {
				return err
			}
			ctx := context.Background()
			gh := repogov.ExecGH{}

			changes, err := repogov.SettingsDiff(ctx, gh, spec, govOpts.RepoOwner, govOpts.RepoName)
			if err != nil {
				return err
			}
			printSettingChanges(opts.ui, changes, govOpts)
			if len(changes) == 0 {
				return nil
			}

			// Branch protection is the safety net for a shared branch, so
			// changing it is never silent. No Interactive guard here on
			// purpose: ui.Prompt already returns the default without a TTY,
			// so a piped or scripted run declines rather than applying
			// unattended. Automation must say --yes explicitly.
			if !yes {
				answer, err := opts.ui.Prompt("Apply these settings? [y/N]", "N", false, "")
				if err != nil {
					return err
				}
				if !strings.EqualFold(strings.TrimSpace(answer), "y") {
					opts.ui.Info("Aborted before applying")
					return nil
				}
			}

			if err := repogov.SettingsApply(ctx, gh, spec, govOpts.RepoOwner, govOpts.RepoName); err != nil {
				return err
			}
			opts.ui.Success("Applied settings to %s/%s", govOpts.RepoOwner, govOpts.RepoName)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

// loadSpecContext resolves the repository and its committed spec together,
// which every settings subcommand needs.
func loadSpecContext() (repogov.Options, repospec.Spec, error) {
	govOpts, err := repoOptions(false)
	if err != nil {
		return repogov.Options{}, repospec.Spec{}, err
	}
	spec, err := repospec.Load(govOpts.WorkDir)
	if err != nil {
		return repogov.Options{}, repospec.Spec{}, err
	}
	return govOpts, spec, nil
}

func printSettingChanges(printer *ui.UI, changes []repogov.SettingChange, opts repogov.Options) {
	target := fmt.Sprintf("%s/%s", opts.RepoOwner, opts.RepoName)
	if len(changes) == 0 {
		printer.Info("%s settings already match.", target)
		return
	}
	_, _ = fmt.Fprintf(printer.Out, "%s has %d setting(s) to change:\n", target, len(changes))
	for _, c := range changes {
		_, _ = fmt.Fprintf(printer.Out, "  %s\n", c)
	}
	_, _ = fmt.Fprintf(printer.Out, "\nBranch protection will require exactly one check: %q\n", repospec.GateCheckName)
}

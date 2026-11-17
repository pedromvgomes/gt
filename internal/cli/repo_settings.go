package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/pedromvgomes/gt/internal/repogov"
	"github.com/pedromvgomes/gt/internal/repospec"
	"github.com/pedromvgomes/gt/internal/ui"
	"go.vipengele.org/cli/spec"
)

func newRepoSettingsCommand(d Deps) *spec.Command {
	return &spec.Command{
		Name:    "settings",
		Summary: "Diff or apply the GitHub settings gt manages",
		Description: "Branch protection, merge methods and the single required status check.\n" +
			"These cannot be expressed as committed files, so gt drives them through the\n" +
			"GitHub API using your existing gh credentials.",
		Commands: []*spec.Command{
			newRepoSettingsDiffCommand(d),
			newRepoSettingsApplyCommand(d),
		},
	}
}

func newRepoSettingsDiffCommand(d Deps) *spec.Command {
	return &spec.Command{
		Name:    "diff",
		Summary: "Show how live GitHub settings differ from " + repospec.FileName,
		Args:    spec.NoArgs(),
		Run: func(ctx context.Context, inv *spec.Invocation) error {
			govOpts, declared, err := loadSpecContext(ctx, inv, d)
			if err != nil {
				return err
			}
			changes, err := repogov.SettingsDiff(ctx, repogov.ExecGH{},
				declared, govOpts.RepoOwner, govOpts.RepoName)
			if err != nil {
				return err
			}
			printSettingChanges(printer(inv), changes, govOpts)
			return nil
		},
	}
}

func newRepoSettingsApplyCommand(d Deps) *spec.Command {
	yes := spec.BoolFlag("yes", "skip the confirmation prompt")
	return &spec.Command{
		Name:    "apply",
		Summary: "Push the GitHub settings declared in " + repospec.FileName,
		Args:    spec.NoArgs(),
		Flags:   []spec.Flag{yes},
		Run: func(ctx context.Context, inv *spec.Invocation) error {
			p := printer(inv)
			govOpts, declared, err := loadSpecContext(ctx, inv, d)
			if err != nil {
				return err
			}
			gh := repogov.ExecGH{}

			changes, err := repogov.SettingsDiff(ctx, gh, declared, govOpts.RepoOwner, govOpts.RepoName)
			if err != nil {
				return err
			}
			printSettingChanges(p, changes, govOpts)
			if len(changes) == 0 {
				return nil
			}

			// Branch protection is the safety net for a shared branch, so
			// changing it is never silent. No Interactive guard here on
			// purpose: ui.Prompt already returns the default without a TTY,
			// so a piped or scripted run declines rather than applying
			// unattended. Automation must say --yes explicitly.
			if !yes.Get(inv) {
				answer, err := p.Prompt("Apply these settings? [y/N]", "N", false, "")
				if err != nil {
					return err
				}
				if !strings.EqualFold(strings.TrimSpace(answer), "y") {
					p.Info("Aborted before applying")
					return nil
				}
			}

			if err := repogov.SettingsApply(ctx, gh, declared, govOpts.RepoOwner, govOpts.RepoName); err != nil {
				return err
			}
			p.Success("Applied settings to %s/%s", govOpts.RepoOwner, govOpts.RepoName)

			// Said again, after the success line. Everything else in the list
			// was just applied; this one thing was not, and a run that ends on
			// "Applied settings" is exactly where a half-finished rollout stops
			// being noticed.
			for _, c := range changes {
				if c.Field != repogov.MergeQueueChangeField {
					continue
				}
				p.Warn("%s is not held to its base yet: %s",
					declared.Settings.BranchProtection.Branch, c.Got)
				p.Info("Everything else was applied. To finish: %s",
					strings.TrimPrefix(c.Want, "deferred — "))
			}
			return nil
		},
	}
}

// loadSpecContext resolves the repository and its committed spec together,
// which every settings subcommand needs.
func loadSpecContext(ctx context.Context, inv *spec.Invocation, d Deps) (repogov.Options, repospec.Spec, error) {
	govOpts, err := repoOptions(ctx, inv, d, false)
	if err != nil {
		return repogov.Options{}, repospec.Spec{}, err
	}
	declared, err := repospec.Load(govOpts.WorkDir)
	if err != nil {
		return repogov.Options{}, repospec.Spec{}, err
	}
	return govOpts, declared, nil
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
	_, _ = fmt.Fprintf(printer.Out, "\nBranch protection will require exactly one check: %q\n", repospec.GateCheckJob)
}

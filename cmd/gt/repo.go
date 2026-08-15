package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/pedromvgomes/gt/internal/git"
	"github.com/pedromvgomes/gt/internal/repogov"
	"github.com/pedromvgomes/gt/internal/repospec"
	"github.com/pedromvgomes/gt/internal/setup"
	"github.com/pedromvgomes/gt/internal/ui"
	"github.com/spf13/cobra"
)

func newRepoCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Manage repository governance from .gt-repo.yaml",
		Long: "Render, verify and repair the governance files a repository is expected to carry:\n" +
			"Dependabot config, the PR gate, conventional-commit enforcement and auto-merge.\n\n" +
			"The committed " + repospec.FileName + " is the source of truth. Shared policy lives in gt's\n" +
			"templates, so changing it for every repo is a single gt release.",
	}
	cmd.AddCommand(newRepoInitCommand(opts))
	cmd.AddCommand(newRepoConfigCommand(opts))
	cmd.AddCommand(newRepoCheckCommand(opts))
	cmd.AddCommand(newRepoSyncCommand(opts))
	cmd.AddCommand(newRepoSettingsCommand(opts))
	cmd.AddCommand(newRepoFleetCommand(opts))
	return cmd
}

// repoOptions resolves the repository context every `gt repo` subcommand needs.
//
// setup.DetectFromCWD supplies the repo identity, which works in a gt-managed
// bare layout and a plain clone alike. Its WorkDir is deliberately NOT used:
// in a bare layout it always points at the default-branch worktree, because
// that is what post-clone setup templates operate on. Governance has to act on
// the worktree you are standing in, or `gt repo sync` run from a feature
// worktree would silently write its changes into the main checkout.
func repoOptions(skipWorkflows bool) (repogov.Options, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return repogov.Options{}, fmt.Errorf("resolve current directory: %w", err)
	}
	ctx := context.Background()
	runner := git.ExecRunner{}

	rcontext, err := setup.DetectFromCWD(ctx, runner, cwd)
	if err != nil {
		return repogov.Options{}, err
	}

	workdir, err := repogov.ResolveWorkDir(ctx, runner, cwd)
	if err != nil {
		return repogov.Options{}, ui.Errorf(ui.ExitUser, "%v", err)
	}

	return repogov.Options{
		WorkDir:       workdir,
		RepoOwner:     rcontext.RepoOwner,
		RepoName:      rcontext.RepoName,
		GTVersion:     version,
		SkipWorkflows: skipWorkflows,
	}, nil
}

func newRepoInitCommand(opts *options) *cobra.Command {
	var (
		force  bool
		dryRun bool
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create " + repospec.FileName + " from what the repository already contains",
		Long: "Detects Dependabot ecosystems from the files on disk and seeds checks.required\n" +
			"from the check names the repository's existing pull_request workflows produce.\n" +
			"The result is a starting point to edit, not a final answer.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			govOpts, err := repoOptions(false)
			if err != nil {
				return err
			}
			specPath := repospec.Path(govOpts.WorkDir)
			if _, err := os.Stat(specPath); err == nil && !force {
				return ui.Errorf(ui.ExitUser,
					"%s already exists; edit it and run 'gt repo sync', or pass --force to regenerate", specPath)
			}

			spec, err := repogov.Init(govOpts)
			if err != nil {
				return err
			}
			printSpecSummary(opts.ui, spec, govOpts)

			if dryRun {
				return nil
			}
			if err := repogov.SaveSpec(govOpts.WorkDir, spec); err != nil {
				return err
			}
			opts.ui.Success("Wrote %s", specPath)
			opts.ui.Info("Review it, then run 'gt repo sync' to render the governance files.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing "+repospec.FileName)
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the detected spec without writing it")
	return cmd
}

func newRepoConfigCommand(_ *options) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Print the resolved " + repospec.FileName + ", with defaults applied",
		Long: "Emits the spec exactly as gt understands it, after defaults and validation.\n\n" +
			"gt's workflows consume this rather than parsing " + repospec.FileName + " themselves.\n" +
			"Re-implementing the defaults in yq had already produced three bugs: `//` is\n" +
			"the alternative operator, so an explicit `false` reads back as the default,\n" +
			"and a key gt defaults in Go had no fallback at all in the workflow.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			govOpts, err := repoOptions(false)
			if err != nil {
				return err
			}
			spec, err := repospec.Load(govOpts.WorkDir)
			if err != nil {
				return err
			}
			if !asJSON {
				return repogov.WriteSpecTo(os.Stdout, spec)
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(spec); err != nil {
				return fmt.Errorf("encode spec: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of YAML")
	return cmd
}

func newRepoCheckCommand(opts *options) *cobra.Command {
	var (
		asJSON        bool
		skipWorkflows bool
	)
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Report governance drift and gate-configuration problems",
		Long: "Renders " + repospec.FileName + ", compares it against the working tree, and lints the\n" +
			"workflow triggers the gate depends on. Exits non-zero when anything is wrong,\n" +
			"which is what makes it usable as a PR check.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			govOpts, err := repoOptions(skipWorkflows)
			if err != nil {
				return err
			}
			report, err := repogov.Check(govOpts)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(report)
			}
			printReport(opts.ui, report, govOpts)
			if !report.Clean() {
				return ui.Errorf(ui.ExitGeneral, "repository is not compliant")
			}
			opts.ui.Success("Repository is compliant")
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable output")
	cmd.Flags().BoolVar(&skipWorkflows, "skip-workflows", false,
		"ignore .github/workflows/** (for CI, where GITHUB_TOKEN cannot write them)")
	return cmd
}

func newRepoSyncCommand(opts *options) *cobra.Command {
	var (
		dryRun        bool
		yes           bool
		skipWorkflows bool
	)
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Write the governance files declared by " + repospec.FileName,
		Long: "Renders and writes the files " + repospec.FileName + " declares.\n\n" +
			"A repository without that file is not governed, which is a valid state: sync\n" +
			"reports it and exits successfully. That is what makes it safe to run from a\n" +
			"post-clone setup template matching every repository you clone.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			govOpts, err := repoOptions(skipWorkflows)
			if err != nil {
				return err
			}
			// "No declaration" means nothing to sync, not a failure. Erroring
			// here would abort the whole `gt clone` setup chain for every
			// repository that has not opted in.
			if !repospec.Exists(govOpts.WorkDir) {
				opts.ui.Info("No %s; repository is not governed. Run 'gt repo init' to start.", repospec.FileName)
				return nil
			}
			report, err := repogov.Check(govOpts)
			if err != nil {
				return err
			}
			printReport(opts.ui, report, govOpts)

			drifted := repogov.Drifted(report.Results)
			if len(drifted) == 0 {
				opts.ui.Info("Nothing to write.")
				return lintOnlyResult(report)
			}
			if dryRun {
				// Lint findings still count. Returning nil here would make
				// `sync --dry-run` pass on a repo with drift *and* lint
				// problems while failing on a clean one with lint problems.
				return lintOnlyResult(report)
			}
			if !yes {
				ok, err := confirmSync(opts.ui, drifted)
				if err != nil {
					return err
				}
				if !ok {
					opts.ui.Info("Aborted before writing")
					return nil
				}
			}
			_, written, err := repogov.Sync(govOpts)
			if err != nil {
				return err
			}
			for _, p := range written {
				opts.ui.Success("wrote %s", p)
			}
			return lintOnlyResult(report)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would change without writing")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&skipWorkflows, "skip-workflows", false,
		"leave .github/workflows/** alone (for CI, where GITHUB_TOKEN cannot write them)")
	return cmd
}

// lintOnlyResult fails a sync that wrote everything it could but still found
// lint problems. Those cannot be fixed by writing files — they are statements
// about the repository's own workflows — so they must not be silently swallowed
// by a successful write.
func lintOnlyResult(report repogov.Report) error {
	if len(report.Findings) > 0 {
		return ui.Errorf(ui.ExitGeneral, "gate configuration has %d problem(s); see above", len(report.Findings))
	}
	return nil
}

func confirmSync(printer *ui.UI, drifted []repogov.Result) (bool, error) {
	if !printer.Interactive {
		// Writing and creating files is the whole point of sync, and the
		// result is git-tracked and reviewable, so an unattended run proceeds.
		// Removing an orphan is not reversible from the working tree alone, so
		// that requires an explicit --yes rather than an assumed one.
		for _, r := range drifted {
			if r.Status == repogov.StatusOrphaned {
				return false, ui.Errorf(ui.ExitUser,
					"%s is no longer declared and would be removed; re-run with --yes to confirm", r.Path)
			}
		}
		return true, nil
	}
	for {
		answer, err := printer.Prompt("Write these? [Y/n/d] (d = show diff)", "Y", false, "")
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "", "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		case "d", "diff":
			printDiff(printer, drifted)
		default:
			printer.Warn("Please answer y, n, or d")
		}
	}
}

func printSpecSummary(printer *ui.UI, spec repospec.Spec, opts repogov.Options) {
	_, _ = fmt.Fprintf(printer.Out, "Detected governance for %s/%s:\n", opts.RepoOwner, opts.RepoName)
	if len(spec.Dependabot) == 0 {
		_, _ = fmt.Fprintln(printer.Out, "  dependabot: (no ecosystems detected)")
	}
	for _, e := range spec.Dependabot {
		_, _ = fmt.Fprintf(printer.Out, "  dependabot: %-15s %s\n", e.Ecosystem, e.Directory)
	}
	if len(spec.Checks.Required) == 0 {
		_, _ = fmt.Fprintln(printer.Out, "  checks:     (none found; add them once CI exists)")
	}
	for _, c := range spec.Checks.Required {
		_, _ = fmt.Fprintf(printer.Out, "  check:      %s\n", c)
	}
	_, _ = fmt.Fprintf(printer.Out, "  gate check: %s  (the only one branch protection needs)\n", repospec.GateCheckName)
}

func printReport(printer *ui.UI, report repogov.Report, opts repogov.Options) {
	if report.VersionStale {
		printer.Warn("rendered by gt %s, running %s — sync to re-render with current policy",
			report.Spec.GTVersion, opts.GTVersion)
	}

	drifted := repogov.Drifted(report.Results)
	if len(drifted) == 0 {
		printer.Info("All %d managed file(s) match.", len(report.Results))
	} else {
		_, _ = fmt.Fprintf(printer.Out, "%d of %d managed file(s) need changing:\n", len(drifted), len(report.Results))
		for _, r := range drifted {
			note := ""
			if r.Status == repogov.StatusOrphaned {
				note = "  (no longer declared; will be removed)"
			}
			_, _ = fmt.Fprintf(printer.Out, "  %-9s %s%s\n", r.Status, r.Path, note)
		}
	}

	// A run that excludes workflow files must say so, rather than reporting a
	// clean repository that merely was not fully inspected. Which workflow
	// files drifted is deliberately not claimed here: they were filtered out
	// before the diff, so this run does not know. The in-repo sync job answers
	// that with a second, unfiltered `gt repo check --json`.
	if opts.SkipWorkflows {
		printer.Info("Skipped %s — GITHUB_TOKEN cannot write workflow files.", repogov.WorkflowDir)
	}

	if len(report.Findings) > 0 {
		_, _ = fmt.Fprintf(printer.Out, "\n%d gate configuration problem(s):\n", len(report.Findings))
		for _, f := range report.Findings {
			_, _ = fmt.Fprintf(printer.Out, "  %s\n", f)
		}
	}
}

func printDiff(printer *ui.UI, drifted []repogov.Result) {
	for _, r := range drifted {
		_, _ = fmt.Fprintf(printer.Out, "\n--- %s (%s)\n", r.Path, r.Status)

		// An orphan has no desired content — showing r.Want would print a
		// single blank line and tell the user nothing about the deletion they
		// are being asked to confirm. Show what is there instead.
		body := r.Want
		if r.Status == repogov.StatusOrphaned {
			_, _ = fmt.Fprintln(printer.Out, "  this file will be REMOVED; its current contents are:")
			body = r.Got
		}
		for _, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
			_, _ = fmt.Fprintf(printer.Out, "  %s\n", line)
		}
	}
}

func printJSON(report repogov.Report) error {
	type jsonFile struct {
		Path     string `json:"path"`
		Status   string `json:"status"`
		Workflow bool   `json:"workflow"`
	}
	type jsonFinding struct {
		Check   string `json:"check,omitempty"`
		Path    string `json:"path,omitempty"`
		Message string `json:"message"`
	}
	out := struct {
		Compliant    bool          `json:"compliant"`
		VersionStale bool          `json:"version_stale"`
		Files        []jsonFile    `json:"files"`
		Findings     []jsonFinding `json:"findings"`
	}{
		Compliant:    report.Clean(),
		VersionStale: report.VersionStale,
		Files:        make([]jsonFile, 0, len(report.Results)),
		Findings:     make([]jsonFinding, 0, len(report.Findings)),
	}
	for _, r := range report.Results {
		out.Files = append(out.Files, jsonFile{Path: r.Path, Status: string(r.Status), Workflow: r.Workflow})
	}
	for _, f := range report.Findings {
		out.Findings = append(out.Findings, jsonFinding{Check: f.Check, Path: f.Path, Message: f.Message})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("encode json report: %w", err)
	}
	if !out.Compliant {
		return ui.Errorf(ui.ExitGeneral, "repository is not compliant")
	}
	return nil
}

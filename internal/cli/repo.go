package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pedromvgomes/gt/internal/git"
	"github.com/pedromvgomes/gt/internal/repogov"
	"github.com/pedromvgomes/gt/internal/repospec"
	"github.com/pedromvgomes/gt/internal/setup"
	"go.vipengele.org/cli/spec"
)

func newRepoCommand(d Deps) *spec.Command {
	return &spec.Command{
		Name:    "repo",
		Summary: "Manage repository governance from .gt-repo.yaml",
		Description: "Render, verify and repair the governance files a repository is expected to carry:\n" +
			"Dependabot config, the PR gate, conventional-commit enforcement and auto-merge.\n\n" +
			"The committed " + repospec.FileName + " is the source of truth. Shared policy lives in gt's\n" +
			"templates, so changing it for every repo is a single gt release.",
		Commands: []*spec.Command{
			newRepoInitCommand(d),
			newRepoConfigCommand(d),
			newRepoCheckCommand(d),
			newRepoSyncCommand(d),
			newRepoSettingsCommand(d),
			newRepoFleetCommand(d),
		},
	}
}

// repoOptions resolves the repository context every `gt repo` subcommand needs.
//
// setup.DetectFromCWD supplies the repo identity, which works in a gt-managed
// bare layout and a plain clone alike. Its WorkDir is deliberately NOT used:
// in a bare layout it always points at the default-branch worktree, because
// that is what post-clone setup templates operate on. Governance has to act on
// the worktree you are standing in, or `gt repo sync` run from a feature
// worktree would silently write its changes into the main checkout.
func repoOptions(ctx context.Context, inv *spec.Invocation, d Deps, skipWorkflows bool) (repogov.Options, error) {
	cwd, err := workDir(inv)
	if err != nil {
		return repogov.Options{}, err
	}
	runner := git.ExecRunner{}

	rcontext, err := setup.DetectFromCWD(ctx, runner, cwd)
	if err != nil {
		return repogov.Options{}, err
	}

	workdir, err := repogov.ResolveWorkDir(ctx, runner, cwd)
	if err != nil {
		return repogov.Options{}, err
	}

	return repogov.Options{
		WorkDir:       workdir,
		RepoOwner:     rcontext.RepoOwner,
		RepoName:      rcontext.RepoName,
		GTVersion:     d.Version,
		SkipWorkflows: skipWorkflows,
	}, nil
}

func newRepoInitCommand(d Deps) *spec.Command {
	var (
		force  = spec.BoolFlag("force", "overwrite an existing "+repospec.FileName)
		dryRun = spec.BoolFlag("dry-run", "print the detected spec without writing it")
	)
	return &spec.Command{
		Name:    "init",
		Summary: "Create " + repospec.FileName + " from what the repository already contains",
		Description: "Detects Dependabot ecosystems from the files on disk and writes a spec with\n" +
			"gt's defaults. The result is a starting point to edit, not a final answer.",
		Args:  spec.NoArgs(),
		Flags: []spec.Flag{force, dryRun},
		Run: func(ctx context.Context, inv *spec.Invocation) error {
			govOpts, err := repoOptions(ctx, inv, d, false)
			if err != nil {
				return err
			}
			specPath := repospec.Path(govOpts.WorkDir)
			if _, err := os.Stat(specPath); err == nil && !force.Get(inv) {
				return fmt.Errorf("%s already exists; edit it and run 'gt repo sync', or pass --force to regenerate", specPath)
			}

			declared, err := repogov.Init(govOpts)
			if err != nil {
				return err
			}
			printSpecSummary(inv, declared, govOpts)

			if dryRun.Get(inv) {
				return nil
			}
			if err := repogov.SaveSpec(govOpts.WorkDir, declared); err != nil {
				return err
			}
			inv.Success("Wrote %s", specPath)
			inv.Info("Review it, then run 'gt repo sync' to render the governance files.")
			return nil
		},
	}
}

func newRepoConfigCommand(d Deps) *spec.Command {
	asJSON := spec.BoolFlag("json", "emit JSON instead of YAML")
	return &spec.Command{
		Name:    "config",
		Summary: "Print the resolved " + repospec.FileName + ", with defaults applied",
		Description: "Emits the spec exactly as gt understands it, after defaults and validation.\n\n" +
			"gt's workflows consume this rather than parsing " + repospec.FileName + " themselves.\n" +
			"Re-implementing the defaults in yq had already produced three bugs: `//` is\n" +
			"the alternative operator, so an explicit `false` reads back as the default,\n" +
			"and a key gt defaults in Go had no fallback at all in the workflow.",
		Args:  spec.NoArgs(),
		Flags: []spec.Flag{asJSON},
		Run: func(ctx context.Context, inv *spec.Invocation) error {
			govOpts, err := repoOptions(ctx, inv, d, false)
			if err != nil {
				return err
			}
			resolved, err := repospec.Load(govOpts.WorkDir)
			if err != nil {
				return err
			}
			if !asJSON.Get(inv) {
				return repogov.WriteSpecTo(inv.Env.Stdout, resolved)
			}
			enc := json.NewEncoder(inv.Env.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(resolved); err != nil {
				return fmt.Errorf("encode spec: %w", err)
			}
			return nil
		},
	}
}

func newRepoCheckCommand(d Deps) *spec.Command {
	var (
		asJSON        = spec.BoolFlag("json", "emit machine-readable output")
		skipWorkflows = spec.BoolFlag("skip-workflows",
			"ignore .github/workflows/** (for CI, where GITHUB_TOKEN cannot write them)")
	)
	return &spec.Command{
		Name:    "check",
		Summary: "Report governance drift and gate-configuration problems",
		Description: "Renders " + repospec.FileName + " and compares it against the working tree.\n" +
			"Exits non-zero on drift, which is what makes it usable as a PR check.",
		Args:  spec.NoArgs(),
		Flags: []spec.Flag{asJSON, skipWorkflows},
		Run: func(ctx context.Context, inv *spec.Invocation) error {
			govOpts, err := repoOptions(ctx, inv, d, skipWorkflows.Get(inv))
			if err != nil {
				return err
			}
			report, err := repogov.Check(govOpts)
			if err != nil {
				return err
			}
			if asJSON.Get(inv) {
				return printJSON(inv.Env.Stdout, report)
			}
			printReport(inv, report, govOpts)
			if !report.Clean() {
				return errors.New("repository is not compliant")
			}
			inv.Success("Repository is compliant")
			return nil
		},
	}
}

func newRepoSyncCommand(d Deps) *spec.Command {
	var (
		dryRun        = spec.BoolFlag("dry-run", "print what would change without writing")
		yes           = spec.BoolFlag("yes", "skip the confirmation prompt")
		skipWorkflows = spec.BoolFlag("skip-workflows",
			"leave .github/workflows/** alone (for CI, where GITHUB_TOKEN cannot write them)")
	)
	return &spec.Command{
		Name:    "sync",
		Summary: "Write the governance files declared by " + repospec.FileName,
		Description: "Renders and writes the files " + repospec.FileName + " declares.\n\n" +
			"A repository without that file is not governed, which is a valid state: sync\n" +
			"reports it and exits successfully. That is what makes it safe to run from a\n" +
			"post-clone setup template matching every repository you clone.",
		Args:  spec.NoArgs(),
		Flags: []spec.Flag{dryRun, yes, skipWorkflows},
		Run: func(ctx context.Context, inv *spec.Invocation) error {
			skip := skipWorkflows.Get(inv)
			govOpts, err := repoOptions(ctx, inv, d, skip)
			if err != nil {
				return err
			}
			// "No declaration" means nothing to sync, not a failure. Erroring
			// here would abort the whole `gt clone` setup chain for every
			// repository that has not opted in.
			if !repospec.Exists(govOpts.WorkDir) {
				inv.Info("No %s; repository is not governed. Run 'gt repo init' to start.", repospec.FileName)
				return nil
			}
			report, err := repogov.Check(govOpts)
			if err != nil {
				return err
			}
			printReport(inv, report, govOpts)

			drifted := repogov.Drifted(report.Results)
			if !report.NeedsWrite(skip) {
				inv.Info("Nothing to write.")
				return nil
			}
			if dryRun.Get(inv) {
				// Same exit code as `check` for the same state. Returning 0
				// here would leave `sync --dry-run` permanently green as a CI
				// drift detector, which is exactly what someone would reach
				// for it to do.
				return errors.New("repository is not compliant")
			}
			if !yes.Get(inv) {
				ok, err := confirmSync(inv, drifted)
				if err != nil {
					return err
				}
				if !ok {
					inv.Info("Aborted before writing")
					return nil
				}
			}
			_, written, err := repogov.Sync(govOpts)
			if err != nil {
				return err
			}
			for _, path := range written {
				inv.Success("wrote %s", path)
			}
			return nil
		},
	}
}

func confirmSync(inv *spec.Invocation, drifted []repogov.Result) (bool, error) {
	if !inv.Env.IsTerminal(spec.Stdin) {
		// Writing and creating files is the whole point of sync, and the
		// result is git-tracked and reviewable, so an unattended run proceeds.
		// Removing an orphan is not reversible from the working tree alone, so
		// that requires an explicit --yes rather than an assumed one.
		for _, r := range drifted {
			if r.Status == repogov.StatusOrphaned {
				return false, fmt.Errorf("%s is no longer declared and would be removed; re-run with --yes to confirm", r.Path)
			}
		}
		return true, nil
	}
	for {
		answer, err := inv.Prompt("Write these? y/n/d (d = show diff)", spec.PromptOptions{Default: "y"})
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		case "d", "diff":
			printDiff(inv, drifted)
		default:
			inv.Warning("Please answer y, n, or d")
		}
	}
}

func printSpecSummary(inv *spec.Invocation, declared repospec.Spec, opts repogov.Options) {
	_, _ = fmt.Fprintf(inv.Env.Stdout, "Detected governance for %s/%s:\n", opts.RepoOwner, opts.RepoName)
	if len(declared.Dependabot) == 0 {
		_, _ = fmt.Fprintln(inv.Env.Stdout, "  dependabot: (no ecosystems detected)")
	}
	for _, e := range declared.Dependabot {
		_, _ = fmt.Fprintf(inv.Env.Stdout, "  dependabot: %-15s %s\n", e.Ecosystem, e.Directory)
	}
	if declared.Pipeline.CI.Enabled {
		_, _ = fmt.Fprintf(inv.Env.Stdout, "  ci stages:  %s\n", strings.Join(declared.Pipeline.CI.Stages, ", "))
	}
	if declared.Pipeline.CD.Enabled {
		_, _ = fmt.Fprintf(inv.Env.Stdout, "  cd stages:  %s\n", strings.Join(declared.Pipeline.CD.Stages, ", "))
		_, _ = fmt.Fprintf(inv.Env.Stdout, "  cd tags:    %s\n", strings.Join(declared.Pipeline.CD.Tags, ", "))
	}
	_, _ = fmt.Fprintf(inv.Env.Stdout, "  gate check: %s  (the only one branch protection needs)\n", repospec.GateCheckJob)
	_, _ = fmt.Fprintln(inv.Env.Stdout,
		"\nThe ci-*/cd-* stage files are yours: gt creates them empty once, then never touches them.")
}

func printReport(inv *spec.Invocation, report repogov.Report, opts repogov.Options) {
	if report.VersionStale {
		inv.Warning("rendered by gt %s, running %s — sync to re-render with current policy",
			report.Spec.GTVersion, opts.GTVersion)
	}

	if report.SpecStale {
		inv.Warning("%s restates values that are gt defaults — sync rewrites it to only the overrides",
			repospec.FileName)
	}

	drifted := repogov.Drifted(report.Results)
	if len(drifted) == 0 {
		inv.Info("All %d managed file(s) match.", len(report.Results))
	} else {
		_, _ = fmt.Fprintf(inv.Env.Stdout, "%d of %d managed file(s) need changing:\n", len(drifted), len(report.Results))
		for _, r := range drifted {
			note := ""
			if r.Status == repogov.StatusOrphaned {
				note = "  (no longer declared; will be removed)"
			}
			_, _ = fmt.Fprintf(inv.Env.Stdout, "  %-9s %s%s\n", r.Status, r.Path, note)
		}
	}

	// A run that excludes workflow files must say so, rather than reporting a
	// clean repository that merely was not fully inspected. Which workflow
	// files drifted is deliberately not claimed here: they were filtered out
	// before the diff, so this run does not know. The in-repo sync job answers
	// that with a second, unfiltered `gt repo check --json`.
	if opts.SkipWorkflows {
		inv.Info("Skipped %s — GITHUB_TOKEN cannot write workflow files.", repogov.WorkflowDir)
	}

}

func printDiff(inv *spec.Invocation, drifted []repogov.Result) {
	for _, r := range drifted {
		_, _ = fmt.Fprintf(inv.Env.Stdout, "\n--- %s (%s)\n", r.Path, r.Status)

		// An orphan has no desired content — showing r.Want would print a
		// single blank line and tell the user nothing about the deletion they
		// are being asked to confirm. Show what is there instead.
		body := r.Want
		if r.Status == repogov.StatusOrphaned {
			_, _ = fmt.Fprintln(inv.Env.Stdout, "  this file will be REMOVED; its current contents are:")
			body = r.Got
		}
		for _, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
			_, _ = fmt.Fprintf(inv.Env.Stdout, "  %s\n", line)
		}
	}
}

func printJSON(w io.Writer, report repogov.Report) error {
	type jsonFile struct {
		Path     string `json:"path"`
		Status   string `json:"status"`
		Workflow bool   `json:"workflow"`
	}
	out := struct {
		Compliant    bool       `json:"compliant"`
		VersionStale bool       `json:"version_stale"`
		Files        []jsonFile `json:"files"`
	}{
		Compliant:    report.Clean(),
		VersionStale: report.VersionStale,
		Files:        make([]jsonFile, 0, len(report.Results)),
	}
	for _, r := range report.Results {
		out.Files = append(out.Files, jsonFile{Path: r.Path, Status: string(r.Status), Workflow: r.Workflow})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("encode json report: %w", err)
	}
	if !out.Compliant {
		return errors.New("repository is not compliant")
	}
	return nil
}

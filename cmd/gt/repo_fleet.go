package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pedromvgomes/gt/internal/git"
	"github.com/pedromvgomes/gt/internal/repogov"
	"github.com/pedromvgomes/gt/internal/repospec"
	"github.com/pedromvgomes/gt/internal/ui"
	"github.com/spf13/cobra"
)

func newRepoFleetCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "Operate across every repository in an owner",
		Long: "Fleet commands run with your own gh credentials, which is what makes them the\n" +
			"escalation path for work GITHUB_TOKEN cannot do in CI — anything touching\n" +
			"a repository's .github/workflows/ directory.",
	}
	cmd.AddCommand(newRepoFleetCheckCommand(opts))
	cmd.AddCommand(newRepoFleetSyncCommand(opts))
	cmd.AddCommand(newRepoFleetMergePendingCommand(opts))
	return cmd
}

func newRepoFleetCheckCommand(opts *options) *cobra.Command {
	return newFleetSweepCommand(opts, false)
}

func newRepoFleetSyncCommand(opts *options) *cobra.Command {
	return newFleetSweepCommand(opts, true)
}

// newFleetSweepCommand builds `fleet check` and `fleet sync`, which differ only
// in whether they write. Sharing the body keeps the two from drifting apart —
// check must report exactly what sync would do.
func newFleetSweepCommand(opts *options, apply bool) *cobra.Command {
	var (
		owner    string
		limit    int
		branch   string
		cacheDir string
	)

	use, short := "check", "Report governance drift across every repository in an owner"
	long := "Clones or refreshes each repository into a cache directory and runs the same\n" +
		"check `gt repo check` runs locally. Read-only."
	if apply {
		use, short = "sync", "Repair governance across every repository, opening a PR per repo"
		long = "Runs `gt repo sync` in each repository and opens a PR with the result.\n\n" +
			"This is the escalation path for files GITHUB_TOKEN cannot write — anything\n" +
			"under .github/workflows/. It uses your own gh credentials, which is why no\n" +
			"long-lived elevated token needs to exist anywhere."
	}

	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long:  long,
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if owner == "" {
				return ui.Errorf(ui.ExitUser, "--owner is required")
			}
			if cacheDir == "" {
				base, err := os.UserCacheDir()
				if err != nil {
					return fmt.Errorf("resolve cache directory: %w", err)
				}
				cacheDir = filepath.Join(base, "gt", "fleet")
			}

			ctx := context.Background()
			gh := repogov.ExecGH{}

			repos, err := repogov.ListRepos(ctx, gh, owner, limit)
			if err != nil {
				return err
			}
			opts.ui.Info("Sweeping %d repositories in %s (cache: %s)...", len(repos), owner, cacheDir)

			results := repogov.FleetSweep(ctx, gh, git.ExecRunner{}, repos, repogov.FleetOptions{
				CacheDir:  cacheDir,
				GTVersion: version,
				Branch:    branch,
				Apply:     apply,
			})
			return printFleetResults(opts.ui, results, apply)
		},
	}
	cmd.Flags().StringVar(&owner, "owner", "", "GitHub user or organization to sweep")
	cmd.Flags().IntVar(&limit, "limit", 200, "maximum repositories to enumerate")
	cmd.Flags().StringVar(&cacheDir, "cache-dir", "", "where working clones are kept (default: user cache dir)")
	if apply {
		cmd.Flags().StringVar(&branch, "branch", repogov.DefaultFleetBranch, "branch to push the sync to")
	}
	return cmd
}

func printFleetResults(printer *ui.UI, results []repogov.FleetResult, applied bool) error {
	var failed, drifted int
	for _, r := range results {
		switch {
		case r.Err != nil:
			failed++
			printer.Warn("%-40s %v", r.Repo, r.Err)
		case !r.Governed:
			_, _ = fmt.Fprintf(printer.Out, "  %-40s not governed\n", r.Repo)
		case r.Clean():
			_, _ = fmt.Fprintf(printer.Out, "  %-40s ok\n", r.Repo)
		default:
			drifted++
			detail := fmt.Sprintf("%d file(s), %d finding(s)", len(r.Drifted), len(r.Findings))
			if r.PR != "" {
				detail += "  -> " + r.PR
			}
			_, _ = fmt.Fprintf(printer.Out, "  %-40s %s\n", r.Repo, detail)
		}
	}

	if drifted == 0 && failed == 0 {
		printer.Success("Fleet is compliant.")
		return nil
	}
	if !applied && drifted > 0 {
		printer.Info("%d repository(ies) need changes. Run 'gt repo fleet sync' to open PRs.", drifted)
	}
	// A sweep that could not read part of the fleet has not answered the
	// question it was asked, so it must not exit 0.
	if failed > 0 {
		return ui.Errorf(ui.ExitGeneral, "%d repository(ies) could not be swept", failed)
	}
	if !applied {
		return ui.Errorf(ui.ExitGeneral, "%d repository(ies) are not compliant", drifted)
	}
	return nil
}

func newRepoFleetMergePendingCommand(opts *options) *cobra.Command {
	var (
		owner   string
		merge   bool
		limit   int
		maxBump string
	)
	cmd := &cobra.Command{
		Use:   "merge-pending",
		Short: "List Dependabot PRs the in-repo auto-merge cannot merge",
		Long: "Dependabot PRs touching .github/workflows/** can never be merged by the\n" +
			"scheduled auto-merge job: no permissions: key grants GITHUB_TOKEN the\n" +
			"`workflow` scope. Every repo with the github-actions ecosystem produces these\n" +
			"routinely, so they accumulate silently until something surfaces them.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if owner == "" {
				return fmt.Errorf("--owner is required")
			}
			ctx := context.Background()
			gh := repogov.ExecGH{}

			repos, err := repogov.ListRepos(ctx, gh, owner, limit)
			if err != nil {
				return err
			}
			opts.ui.Info("Scanning %d repositories in %s...", len(repos), owner)

			total := 0
			eligible := 0
			for _, repo := range repos {
				pending, err := repogov.PendingWorkflowPRs(ctx, gh, repo, maxBump)
				if err != nil {
					// One inaccessible repo must not end the sweep; the point
					// is to see the whole fleet.
					opts.ui.Warn("%s: %v", repo, err)
					continue
				}
				for _, pr := range pending {
					total++
					status := pr.Bump
					if !pr.Eligible {
						status = "skip: " + pr.Reason
					}
					_, _ = fmt.Fprintf(opts.ui.Out, "  %s#%-5d %-40s (%s)\n", pr.Repo, pr.Number, pr.Title, status)
					if !pr.Eligible {
						continue
					}
					eligible++
					if !merge {
						continue
					}
					if err := repogov.MergePending(ctx, gh, pr); err != nil {
						opts.ui.Warn("  merge failed: %v", err)
						continue
					}
					opts.ui.Success("  merged %s#%d", pr.Repo, pr.Number)
				}
			}

			if total == 0 {
				opts.ui.Success("No pending workflow PRs.")
				return nil
			}
			if !merge {
				opts.ui.Info("%d pending PR(s), %d eligible. Re-run with --merge to squash-merge the eligible ones.",
					total, eligible)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&owner, "owner", "", "GitHub user or organization to scan")
	cmd.Flags().BoolVar(&merge, "merge", false, "merge the eligible PRs instead of only reporting them")
	cmd.Flags().IntVar(&limit, "limit", 200, "maximum repositories to enumerate")
	cmd.Flags().StringVar(&maxBump, "max-bump", repospec.BumpMinor,
		"highest bump level to consider eligible (patch, minor, major)")
	return cmd
}

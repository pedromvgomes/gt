package main

import (
	"context"
	"fmt"

	"github.com/pedromvgomes/gt/internal/repogov"
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
	cmd.AddCommand(newRepoFleetMergePendingCommand(opts))
	return cmd
}

func newRepoFleetMergePendingCommand(opts *options) *cobra.Command {
	var (
		owner string
		merge bool
		limit int
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
			for _, repo := range repos {
				pending, err := repogov.PendingWorkflowPRs(ctx, gh, repo)
				if err != nil {
					// One inaccessible repo must not end the sweep; the point
					// is to see the whole fleet.
					opts.ui.Warn("%s: %v", repo, err)
					continue
				}
				for _, pr := range pending {
					total++
					_, _ = fmt.Fprintf(opts.ui.Out, "  %s#%d  %s\n", pr.Repo, pr.Number, pr.Title)
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
				opts.ui.Info("%d pending PR(s). Re-run with --merge to squash-merge them.", total)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&owner, "owner", "", "GitHub user or organization to scan")
	cmd.Flags().BoolVar(&merge, "merge", false, "merge the listed PRs instead of only reporting them")
	cmd.Flags().IntVar(&limit, "limit", 200, "maximum repositories to enumerate")
	return cmd
}

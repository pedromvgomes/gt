package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pedromvgomes/gt/internal/git"
	"github.com/pedromvgomes/gt/internal/repogov"
	"github.com/pedromvgomes/gt/internal/repospec"
	"go.vipengele.org/cli/spec"
)

func newRepoFleetCommand(d Deps) *spec.Command {
	return &spec.Command{
		Name:    "fleet",
		Summary: "Operate across every repository in an owner",
		Description: "Fleet commands run with your own gh credentials, which is what makes them the\n" +
			"escalation path for work GITHUB_TOKEN cannot do in CI — anything touching\n" +
			"a repository's .github/workflows/ directory.",
		Commands: []*spec.Command{
			newFleetSweepCommand(d, false),
			newFleetSweepCommand(d, true),
			newRepoFleetMergePendingCommand(),
		},
	}
}

// newFleetSweepCommand builds `fleet check` and `fleet sync`, which differ only
// in whether they write. Sharing the body keeps the two from drifting apart —
// check must report exactly what sync would do.
func newFleetSweepCommand(d Deps, apply bool) *spec.Command {
	var (
		owner    = spec.StringFlag("owner", "GitHub user or organization to sweep")
		limit    = spec.IntFlag("limit", "maximum repositories to enumerate", spec.Default(200))
		cacheDir = spec.StringFlag("cache-dir", "where working clones are kept (default: user cache dir)")
		branch   = spec.StringFlag("branch", "branch to push the sync to", spec.Default(repogov.DefaultFleetBranch))
	)
	flags := []spec.Flag{owner, limit, cacheDir}
	if apply {
		flags = append(flags, branch)
	}

	name, summary := "check", "Report governance drift across every repository in an owner"
	description := "Clones or refreshes each repository into a cache directory and runs the same\n" +
		"check `gt repo check` runs locally. Read-only."
	if apply {
		name, summary = "sync", "Repair governance across every repository, opening a PR per repo"
		description = "Runs `gt repo sync` in each repository and opens a PR with the result.\n\n" +
			"This is the escalation path for files GITHUB_TOKEN cannot write — anything\n" +
			"under .github/workflows/. It uses your own gh credentials, which is why no\n" +
			"long-lived elevated token needs to exist anywhere."
	}

	return &spec.Command{
		Name:        name,
		Summary:     summary,
		Description: description,
		Args:        spec.NoArgs(),
		Flags:       flags,
		Run: func(ctx context.Context, inv *spec.Invocation) error {
			if owner.Get(inv) == "" {
				return errors.New("--owner is required")
			}
			cache := cacheDir.Get(inv)
			if cache == "" {
				base, err := os.UserCacheDir()
				if err != nil {
					return fmt.Errorf("resolve cache directory: %w", err)
				}
				cache = filepath.Join(base, "gt", "fleet")
			}

			pushTo := ""
			if apply {
				pushTo = branch.Get(inv)
			}
			gh := repogov.ExecGH{}

			repos, err := repogov.ListRepos(ctx, gh, owner.Get(inv), limit.Get(inv))
			if err != nil {
				return err
			}
			inv.Info("Sweeping %d repositories in %s (cache: %s)...", len(repos), owner.Get(inv), cache)

			results := repogov.FleetSweep(ctx, gh, git.ExecRunner{}, repos, repogov.FleetOptions{
				CacheDir:  cache,
				GTVersion: d.Version,
				Branch:    pushTo,
				Apply:     apply,
			})
			return printFleetResults(inv, results, apply)
		},
	}
}

func printFleetResults(inv *spec.Invocation, results []repogov.FleetResult, applied bool) error {
	var failed, drifted int
	for _, r := range results {
		switch {
		case r.Err != nil:
			failed++
			inv.Warning("%-40s %v", r.Repo, r.Err)
		case !r.Governed:
			_, _ = fmt.Fprintf(inv.Env.Stdout, "  %-40s not governed\n", r.Repo)
		case r.Clean():
			_, _ = fmt.Fprintf(inv.Env.Stdout, "  %-40s ok\n", r.Repo)
		default:
			drifted++
			detail := fmt.Sprintf("%d file(s) to change", len(r.Drifted))
			if r.PR != "" {
				detail += "  -> " + r.PR
			}
			_, _ = fmt.Fprintf(inv.Env.Stdout, "  %-40s %s\n", r.Repo, detail)
		}
	}

	if drifted == 0 && failed == 0 {
		inv.Success("Fleet is compliant.")
		return nil
	}
	if !applied && drifted > 0 {
		inv.Info("%d repository(ies) need changes. Run 'gt repo fleet sync' to open PRs.", drifted)
	}
	// A sweep that could not read part of the fleet has not answered the
	// question it was asked, so it must not exit 0.
	if failed > 0 {
		return fmt.Errorf("%d repository(ies) could not be swept", failed)
	}
	if !applied {
		return fmt.Errorf("%d repository(ies) are not compliant", drifted)
	}
	return nil
}

func newRepoFleetMergePendingCommand() *spec.Command {
	var (
		owner   = spec.StringFlag("owner", "GitHub user or organization to scan")
		merge   = spec.BoolFlag("merge", "merge the eligible PRs instead of only reporting them")
		limit   = spec.IntFlag("limit", "maximum repositories to enumerate", spec.Default(200))
		maxBump = spec.StringFlag("max-bump", "highest bump level to consider eligible (patch, minor, major)",
			spec.Default(repospec.BumpMinor))
	)
	return &spec.Command{
		Name:    "merge-pending",
		Summary: "List Dependabot PRs the in-repo auto-merge cannot merge",
		Description: "Dependabot PRs touching .github/workflows/** can never be merged by the\n" +
			"scheduled auto-merge job: no permissions: key grants GITHUB_TOKEN the\n" +
			"`workflow` scope. Every repo with the github-actions ecosystem produces these\n" +
			"routinely, so they accumulate silently until something surfaces them.",
		Args:  spec.NoArgs(),
		Flags: []spec.Flag{owner, merge, limit, maxBump},
		Run: func(ctx context.Context, inv *spec.Invocation) error {
			if owner.Get(inv) == "" {
				return fmt.Errorf("--owner is required")
			}
			gh := repogov.ExecGH{}

			repos, err := repogov.ListRepos(ctx, gh, owner.Get(inv), limit.Get(inv))
			if err != nil {
				return err
			}
			inv.Info("Scanning %d repositories in %s...", len(repos), owner.Get(inv))

			total := 0
			eligible := 0
			for _, repo := range repos {
				pending, err := repogov.PendingWorkflowPRs(ctx, gh, repo, maxBump.Get(inv))
				if err != nil {
					// One inaccessible repo must not end the sweep; the point
					// is to see the whole fleet.
					inv.Warning("%s: %v", repo, err)
					continue
				}
				for _, pr := range pending {
					total++
					status := pr.Bump
					if !pr.Eligible {
						status = "skip: " + pr.Reason
					}
					_, _ = fmt.Fprintf(inv.Env.Stdout, "  %s#%-5d %-40s (%s)\n", pr.Repo, pr.Number, pr.Title, status)
					if pr.Behind && merge.Get(inv) {
						if err := repogov.RequestRebase(ctx, gh, pr); err != nil {
							inv.Warning("  rebase request failed: %v", err)
							continue
						}
						inv.Info("  asked Dependabot to rebase %s#%d", pr.Repo, pr.Number)
					}
					if !pr.Eligible {
						continue
					}
					eligible++
					if !merge.Get(inv) {
						continue
					}
					if err := repogov.MergePending(ctx, gh, pr); err != nil {
						inv.Warning("  merge failed: %v", err)
						continue
					}
					inv.Success("  merged %s#%d", pr.Repo, pr.Number)
				}
			}

			if total == 0 {
				inv.Success("No pending workflow PRs.")
				return nil
			}
			if !merge.Get(inv) {
				inv.Info("%d pending PR(s), %d eligible. Re-run with --merge to squash-merge the eligible ones.",
					total, eligible)
			}
			return nil
		},
	}
}

package repogov

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pedromvgomes/gt/internal/git"
	"github.com/pedromvgomes/gt/internal/repospec"
)

// FleetResult is one repository's outcome in a fleet run.
type FleetResult struct {
	Repo string
	// Governed is false when the repo has no .gt-repo.yaml; such repos are
	// reported and skipped rather than treated as failures.
	Governed bool
	Drifted  []Result
	// PR is the URL of a PR opened by a sync run, when one was opened.
	PR string
	// Err records a per-repo failure. The sweep continues past it: one
	// inaccessible repo must not hide the state of the other nine.
	Err error
}

// Clean reports whether the repository needed nothing.
func (r FleetResult) Clean() bool {
	return r.Err == nil && (!r.Governed || len(r.Drifted) == 0)
}

// FleetOptions configures a fleet sweep.
type FleetOptions struct {
	// CacheDir holds the working clones. Reused across runs so repeated
	// sweeps fetch rather than clone from scratch.
	CacheDir  string
	GTVersion string
	// Branch is the branch fleet sync pushes to.
	Branch string
	// Apply writes and opens PRs. When false the sweep is read-only.
	Apply bool
}

// DefaultFleetBranch matches the branch the in-repo sync workflow uses, so a
// local escalation updates the same PR rather than opening a competing one.
const DefaultFleetBranch = "gt/repo-sync"

// FleetSweep checks — and with Apply, repairs — governance across repositories.
//
// This is the escalation path for everything GITHUB_TOKEN cannot do in CI,
// which is anything under .github/workflows/. It runs with the user's own gh
// credentials, so no long-lived elevated token has to exist anywhere.
func FleetSweep(ctx context.Context, gh GH, runner git.Runner, repos []string, opts FleetOptions) []FleetResult {
	if opts.Branch == "" {
		opts.Branch = DefaultFleetBranch
	}
	out := make([]FleetResult, 0, len(repos))
	for _, repo := range repos {
		out = append(out, sweepOne(ctx, gh, runner, repo, opts))
	}
	return out
}

func sweepOne(ctx context.Context, gh GH, runner git.Runner, repo string, opts FleetOptions) FleetResult {
	res := FleetResult{Repo: repo}

	dir, err := ensureClone(ctx, runner, repo, opts.CacheDir)
	if err != nil {
		res.Err = err
		return res
	}

	if !repospec.Exists(dir) {
		return res // Governed stays false: not an error, just not opted in.
	}
	res.Governed = true

	owner, name, _ := strings.Cut(repo, "/")
	govOpts := Options{
		WorkDir:   dir,
		RepoOwner: owner,
		RepoName:  name,
		GTVersion: opts.GTVersion,
	}

	report, err := Check(govOpts)
	if err != nil {
		res.Err = err
		return res
	}
	res.Drifted = Drifted(report.Results)

	if !opts.Apply || len(res.Drifted) == 0 {
		return res
	}

	url, err := applyAndPropose(ctx, gh, runner, dir, repo, govOpts, opts.Branch)
	if err != nil {
		res.Err = err
		return res
	}
	res.PR = url
	return res
}

// ensureClone fetches an existing cached clone or makes a fresh one, leaving
// the working tree on an up-to-date default branch.
func ensureClone(ctx context.Context, runner git.Runner, repo, cacheDir string) (string, error) {
	dir := filepath.Join(cacheDir, strings.ReplaceAll(repo, "/", "__"))

	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			return "", fmt.Errorf("create cache directory: %w", err)
		}
		if _, err := runner.Run(ctx, cacheDir, "clone", "https://github.com/"+repo+".git", dir); err != nil {
			return "", fmt.Errorf("clone %s: %w", repo, err)
		}
		return dir, nil
	}

	// Reset hard rather than merge: the cache is scratch space, and a leftover
	// branch from a previous sweep must never be mistaken for real drift.
	if _, err := runner.Run(ctx, dir, "fetch", "origin"); err != nil {
		return "", fmt.Errorf("fetch %s: %w", repo, err)
	}
	head, err := runner.Run(ctx, dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve default branch for %s: %w", repo, err)
	}
	branch := strings.TrimPrefix(strings.TrimSpace(head.Stdout), "origin/")
	if _, err := runner.Run(ctx, dir, "checkout", "-B", branch, "origin/"+branch); err != nil {
		return "", fmt.Errorf("checkout %s in %s: %w", branch, repo, err)
	}
	if _, err := runner.Run(ctx, dir, "reset", "--hard", "origin/"+branch); err != nil {
		return "", fmt.Errorf("reset %s: %w", repo, err)
	}
	return dir, nil
}

// applyAndPropose syncs the working tree and opens (or updates) a PR.
func applyAndPropose(
	ctx context.Context, gh GH, runner git.Runner, dir, repo string, govOpts Options, branch string,
) (string, error) {
	if _, _, err := Sync(govOpts); err != nil {
		return "", err
	}

	if _, err := runner.Run(ctx, dir, "switch", "-C", branch); err != nil {
		return "", fmt.Errorf("create branch %s: %w", branch, err)
	}
	if _, err := runner.Run(ctx, dir, "add", "-A"); err != nil {
		return "", fmt.Errorf("stage changes: %w", err)
	}
	// Identity passed per-invocation rather than assumed: the cache clone has
	// no local config, and a machine without a global user.name would fail here
	// with the whole sweep reported as an error.
	if _, err := runner.Run(ctx, dir,
		"-c", "user.name=gt",
		"-c", "user.email=gt@users.noreply.github.com",
		"commit", "-m", "chore(gt): sync repository governance"); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	// Plain --force, not --force-with-lease: a fresh clone has no
	// remote-tracking ref for this branch, so the lease would compare against
	// a null OID and be rejected as stale whenever the branch already exists.
	if _, err := runner.Run(ctx, dir, "push", "--force", "origin", branch); err != nil {
		return "", fmt.Errorf("push %s: %w", branch, err)
	}

	existing, err := gh.Run(ctx, "pr", "list", "--repo", repo,
		"--head", branch, "--state", "open", "--json", "url", "--jq", ".[0].url")
	if err == nil {
		if url := strings.TrimSpace(string(existing)); url != "" {
			return url, nil
		}
	}

	created, err := gh.Run(ctx, "pr", "create", "--repo", repo,
		"--head", branch,
		"--title", "chore(gt): sync repository governance",
		"--body", "Automated `gt repo fleet sync`, run locally because it updates files "+
			"under `.github/workflows/` that `GITHUB_TOKEN` cannot write.")
	if err != nil {
		return "", fmt.Errorf("open PR for %s: %w", repo, err)
	}
	return strings.TrimSpace(string(created)), nil
}

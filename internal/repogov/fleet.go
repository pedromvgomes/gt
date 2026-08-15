package repogov

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/pedromvgomes/gt/internal/repospec"
)

// PendingPR is a Dependabot PR the in-repo auto-merge cannot merge.
type PendingPR struct {
	Repo   string
	Number int
	Title  string
	// Bump is the semver level parsed from the title, or "" when the title
	// does not carry a parseable version pair.
	Bump string
	// Eligible reports whether this PR passes the same gates the in-repo
	// auto-merge applies. Ineligible PRs are still listed — seeing them is the
	// point — but are never merged.
	Eligible bool
	// Reason explains an ineligible verdict.
	Reason string
}

// pullRequest is the subset of the PR list API the fleet commands read.
type pullRequest struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Files  []struct {
		Path string `json:"path"`
	} `json:"files"`
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"mergeStateStatus"`
	IsDraft          bool   `json:"isDraft"`
}

// ListRepos enumerates the repositories in an owner, excluding archived ones.
// Fleet commands operate on what the account actually has rather than a list
// that has to be maintained by hand alongside it.
func ListRepos(ctx context.Context, gh GH, owner string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 200
	}
	raw, err := gh.Run(ctx, "repo", "list", owner,
		"--no-archived", "--limit", fmt.Sprint(limit), "--json", "nameWithOwner")
	if err != nil {
		return nil, err
	}
	var items []struct {
		NameWithOwner string `json:"nameWithOwner"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("parse repo list for %s: %w", owner, err)
	}
	out := make([]string, 0, len(items))
	for _, i := range items {
		out = append(out, i.NameWithOwner)
	}
	sort.Strings(out)
	return out, nil
}

// PendingWorkflowPRs returns open Dependabot PRs that touch
// .github/workflows/**.
//
// These can never be merged by the in-repo auto-merge job: no `permissions:`
// key grants GITHUB_TOKEN the `workflow` scope. Every repo with the
// github-actions ecosystem produces them routinely, so without a command that
// surfaces them they accumulate silently and indefinitely.
func PendingWorkflowPRs(ctx context.Context, gh GH, repo, maxBump string) ([]PendingPR, error) {
	raw, err := gh.Run(ctx, "pr", "list", "--repo", repo,
		"--author", "app/dependabot", "--state", "open",
		// --limit is required: gh defaults to 30, which would silently cap the
		// sweep this command exists to make exhaustive.
		"--limit", "200",
		"--json", "number,title,files,mergeable,mergeStateStatus,isDraft")
	if err != nil {
		return nil, err
	}
	var prs []pullRequest
	if err := json.Unmarshal(raw, &prs); err != nil {
		return nil, fmt.Errorf("parse pull requests for %s: %w", repo, err)
	}

	var out []PendingPR
	for _, pr := range prs {
		if pr.IsDraft || !TouchesWorkflows(pathsOf(pr)) {
			continue
		}
		p := PendingPR{Repo: repo, Number: pr.Number, Title: pr.Title}
		p.Bump, p.Eligible, p.Reason = eligibility(pr, maxBump)
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out, nil
}

// eligibility applies the same gates the in-repo auto-merge job applies.
//
// This path exists only because GITHUB_TOKEN cannot merge workflow files — not
// because the policy should be looser here. Merging without these checks would
// let the escalation route land a major bump with a red gate, which is exactly
// what the policed job refuses to do.
func eligibility(pr pullRequest, maxBump string) (bump string, eligible bool, reason string) {
	if pr.Mergeable != "MERGEABLE" {
		return "", false, fmt.Sprintf("not mergeable (%s)", pr.Mergeable)
	}
	// UNSTABLE means required checks passed but optional ones failed; branch
	// protection already enforces the required set.
	switch pr.MergeStateStatus {
	case "CLEAN", "HAS_HOOKS", "UNSTABLE":
	default:
		return "", false, fmt.Sprintf("checks not green (%s)", pr.MergeStateStatus)
	}

	bump, ok := ParseBump(pr.Title)
	if !ok {
		return "", false, "could not parse versions from the title"
	}
	if bumpRank(bump) > bumpRank(maxBump) {
		return bump, false, fmt.Sprintf("%s exceeds max_bump=%s; needs human review", bump, maxBump)
	}
	return bump, true, ""
}

// dependabotTitle matches Dependabot's stable title format,
// "bump <pkg> from <old> to <new>", optionally suffixed with " in /path".
var dependabotTitle = regexp.MustCompile(
	`from (\d+)\.(\d+)\.\d+(?:-[A-Za-z0-9.\-]+)? to (\d+)\.(\d+)\.\d+(?:-[A-Za-z0-9.\-]+)?`)

// ParseBump classifies a Dependabot PR title as a patch, minor or major bump.
func ParseBump(title string) (string, bool) {
	m := dependabotTitle.FindStringSubmatch(title)
	if m == nil {
		return "", false
	}
	switch {
	case m[1] != m[3]:
		return repospec.BumpMajor, true
	case m[2] != m[4]:
		return repospec.BumpMinor, true
	default:
		return repospec.BumpPatch, true
	}
}

func bumpRank(bump string) int {
	switch bump {
	case repospec.BumpPatch:
		return 1
	case repospec.BumpMinor:
		return 2
	case repospec.BumpMajor:
		return 3
	default:
		return 0
	}
}

func pathsOf(pr pullRequest) []string {
	paths := make([]string, 0, len(pr.Files))
	for _, f := range pr.Files {
		paths = append(paths, f.Path)
	}
	return paths
}

// TouchesWorkflows reports whether any path lies under .github/workflows/,
// which is the boundary GITHUB_TOKEN cannot write across.
func TouchesWorkflows(paths []string) bool {
	for _, p := range paths {
		if strings.HasPrefix(p, WorkflowDir+"/") {
			return true
		}
	}
	return false
}

// MergePending squash-merges a pending PR using the caller's own credentials.
// It refuses anything the policy gates rejected, so the escalation path cannot
// be used to bypass them.
func MergePending(ctx context.Context, gh GH, pr PendingPR) error {
	if !pr.Eligible {
		return fmt.Errorf("%s#%d is not eligible: %s", pr.Repo, pr.Number, pr.Reason)
	}
	_, err := gh.Run(ctx, "pr", "merge", fmt.Sprint(pr.Number),
		"--repo", pr.Repo, "--squash", "--delete-branch")
	return err
}

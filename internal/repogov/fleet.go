package repogov

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// PendingPR is a Dependabot PR the in-repo auto-merge cannot merge.
type PendingPR struct {
	Repo   string
	Number int
	Title  string
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
func PendingWorkflowPRs(ctx context.Context, gh GH, repo string) ([]PendingPR, error) {
	raw, err := gh.Run(ctx, "pr", "list", "--repo", repo,
		"--author", "app/dependabot", "--state", "open",
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
		out = append(out, PendingPR{Repo: repo, Number: pr.Number, Title: pr.Title})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out, nil
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
func MergePending(ctx context.Context, gh GH, pr PendingPR) error {
	_, err := gh.Run(ctx, "pr", "merge", fmt.Sprint(pr.Number),
		"--repo", pr.Repo, "--squash", "--delete-branch")
	return err
}

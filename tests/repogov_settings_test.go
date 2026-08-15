package tests

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pedromvgomes/gt/internal/repogov"
	"github.com/pedromvgomes/gt/internal/repospec"
)

// fakeGH answers gh invocations from a canned table, and records what it was
// asked, so settings logic is testable without a token or a network.
type fakeGH struct {
	responses map[string]string // matched as a substring of the joined args
	errors    map[string]error
	calls     []string
	inputs    [][]byte
}

func (f *fakeGH) Run(ctx context.Context, args ...string) ([]byte, error) {
	return f.RunWithInput(ctx, nil, args...)
}

func (f *fakeGH) RunWithInput(_ context.Context, stdin []byte, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	f.calls = append(f.calls, joined)
	f.inputs = append(f.inputs, stdin)
	// Longest pattern wins. The protection URL contains the repo URL as a
	// prefix, so without this the match would depend on map iteration order.
	if pattern, ok := longestMatch(joined, keysOf(f.errors)); ok {
		return nil, f.errors[pattern]
	}
	if pattern, ok := longestMatch(joined, keysOf(f.responses)); ok {
		return []byte(f.responses[pattern]), nil
	}
	return []byte("{}"), nil
}

func longestMatch(s string, patterns []string) (string, bool) {
	best := ""
	for _, p := range patterns {
		if strings.Contains(s, p) && len(p) > len(best) {
			best = p
		}
	}
	return best, best != ""
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

const compliantRepoJSON = `{
  "allow_squash_merge": true,
  "allow_merge_commit": false,
  "allow_rebase_merge": false,
  "delete_branch_on_merge": true
}`

func compliantProtectionJSON(t *testing.T) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"required_status_checks": map[string]any{
			"strict":   true,
			"contexts": []string{repospec.GateCheckName},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(body)
}

func TestSettingsDiffCleanWhenAligned(t *testing.T) {
	gh := &fakeGH{responses: map[string]string{
		"branches/main/protection": compliantProtectionJSON(t),
		"repos/pedromvgomes/demo":  compliantRepoJSON,
	}}
	changes, err := repogov.SettingsDiff(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo")
	if err != nil {
		t.Fatalf("SettingsDiff() error = %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("SettingsDiff() = %v, want none", changes)
	}
}

// gt enforces squash-only, which is what lets conventional-commit enforcement
// reduce to a PR-title check.
func TestSettingsDiffDetectsNonSquashMerge(t *testing.T) {
	gh := &fakeGH{responses: map[string]string{
		"branches/main/protection": compliantProtectionJSON(t),
		"repos/pedromvgomes/demo": `{
		  "allow_squash_merge": true,
		  "allow_merge_commit": true,
		  "allow_rebase_merge": false,
		  "delete_branch_on_merge": true
		}`,
	}}
	changes, err := repogov.SettingsDiff(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo")
	if err != nil {
		t.Fatalf("SettingsDiff() error = %v", err)
	}
	if len(changes) != 1 || changes[0].Field != "allow_merge_commit" {
		t.Fatalf("SettingsDiff() = %v, want a single allow_merge_commit change", changes)
	}
}

// Branch protection must converge on exactly one context: gt's gate. A repo
// still listing individual CI jobs is drift, because renaming any of them
// would silently unprotect the branch.
func TestSettingsDiffReplacesPerJobRequiredChecks(t *testing.T) {
	prot, err := json.Marshal(map[string]any{
		"required_status_checks": map[string]any{
			"strict":   true,
			"contexts": []string{"CI / build", "CI / test"},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	gh := &fakeGH{responses: map[string]string{
		"branches/main/protection": string(prot),
		"repos/pedromvgomes/demo":  compliantRepoJSON,
	}}
	changes, err := repogov.SettingsDiff(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo")
	if err != nil {
		t.Fatalf("SettingsDiff() error = %v", err)
	}
	if len(changes) != 1 || !strings.Contains(changes[0].Field, "contexts") {
		t.Fatalf("SettingsDiff() = %v, want a contexts change", changes)
	}
	if changes[0].Want != repospec.GateCheckName {
		t.Errorf("want contexts = %q, expected %q", changes[0].Want, repospec.GateCheckName)
	}
}

// An unprotected branch is a 404. That is a state to converge from, not an
// error — otherwise gt could never protect a branch for the first time.
func TestSettingsDiffTreatsMissingProtectionAsDrift(t *testing.T) {
	gh := &fakeGH{
		responses: map[string]string{"repos/pedromvgomes/demo": compliantRepoJSON},
		errors:    map[string]error{"protection": errNotFound{}},
	}
	changes, err := repogov.SettingsDiff(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo")
	if err != nil {
		t.Fatalf("SettingsDiff() error = %v", err)
	}
	if len(changes) != 1 || changes[0].Field != "branch_protection" {
		t.Fatalf("SettingsDiff() = %v, want a branch_protection change", changes)
	}
}

type errNotFound struct{}

func (errNotFound) Error() string { return "gh: HTTP 404: Branch not protected" }

func TestSettingsApplySendsGateAsTheOnlyRequiredCheck(t *testing.T) {
	gh := &fakeGH{}
	if err := repogov.SettingsApply(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}

	var payload map[string]any
	found := false
	for i, call := range gh.calls {
		if strings.Contains(call, "protection") && gh.inputs[i] != nil {
			if err := json.Unmarshal(gh.inputs[i], &payload); err != nil {
				t.Fatalf("protection payload is not JSON: %v", err)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("no branch-protection call with a body; calls = %v", gh.calls)
	}

	checks, ok := payload["required_status_checks"].(map[string]any)
	if !ok {
		t.Fatalf("payload missing required_status_checks: %v", payload)
	}
	contexts, ok := checks["contexts"].([]any)
	if !ok || len(contexts) != 1 || contexts[0] != repospec.GateCheckName {
		t.Errorf("contexts = %v, want exactly [%q]", checks["contexts"], repospec.GateCheckName)
	}
}

// The protection endpoint rejects a review requirement of zero, so it must be
// expressed by dropping the block entirely.
func TestSettingsApplyOmitsReviewsWhenZeroApprovals(t *testing.T) {
	gh := &fakeGH{}
	spec := repospec.Default()
	spec.Settings.BranchProtection.RequiredApprovals = 0
	if err := repogov.SettingsApply(context.Background(), gh, spec, "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}
	for i, call := range gh.calls {
		if !strings.Contains(call, "protection") || gh.inputs[i] == nil {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(gh.inputs[i], &payload); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if payload["required_pull_request_reviews"] != nil {
			t.Errorf("required_pull_request_reviews = %v, want null", payload["required_pull_request_reviews"])
		}
	}
}

func TestSettingsRequireRepositoryIdentity(t *testing.T) {
	if _, err := repogov.SettingsDiff(context.Background(), &fakeGH{}, repospec.Default(), "", "demo"); err == nil {
		t.Error("SettingsDiff() with no owner = nil, want error")
	}
	if err := repogov.SettingsApply(context.Background(), &fakeGH{}, repospec.Default(), "pedromvgomes", ""); err == nil {
		t.Error("SettingsApply() with no name = nil, want error")
	}
}

func TestTouchesWorkflows(t *testing.T) {
	tests := []struct {
		paths []string
		want  bool
	}{
		{[]string{".github/workflows/ci.yml"}, true},
		{[]string{"go.mod", ".github/workflows/release.yml"}, true},
		{[]string{"go.mod", "go.sum"}, false},
		{[]string{".github/dependabot.yml"}, false},
		// Not under the workflows directory, despite the shared prefix.
		{[]string{".github/workflows-notes.md"}, false},
		{nil, false},
	}
	for _, tc := range tests {
		if got := repogov.TouchesWorkflows(tc.paths); got != tc.want {
			t.Errorf("TouchesWorkflows(%v) = %v, want %v", tc.paths, got, tc.want)
		}
	}
}

// These are the PRs the in-repo auto-merge can never merge, so the fleet
// command must find them and ignore everything else.
func TestPendingWorkflowPRsSelectsOnlyWorkflowTouchingPRs(t *testing.T) {
	gh := &fakeGH{responses: map[string]string{
		"pr list": `[
		  {"number":1,"title":"bump actions/checkout from 6.0.0 to 6.1.0","files":[{"path":".github/workflows/ci.yml"}],
		   "isDraft":false,"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"},
		  {"number":2,"title":"bump serde from 1.0.1 to 1.0.2","files":[{"path":"Cargo.toml"}],
		   "isDraft":false,"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"},
		  {"number":3,"title":"bump actions/setup-go from 5.0.0 to 5.0.1","files":[{"path":".github/workflows/release.yml"}],
		   "isDraft":true,"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"}
		]`,
	}}
	pending, err := repogov.PendingWorkflowPRs(context.Background(), gh, "pedromvgomes/demo", repospec.BumpMinor)
	if err != nil {
		t.Fatalf("PendingWorkflowPRs() error = %v", err)
	}
	if len(pending) != 1 || pending[0].Number != 1 {
		t.Fatalf("PendingWorkflowPRs() = %v, want only PR #1", pending)
	}
	if !pending[0].Eligible {
		t.Errorf("PR #1 should be eligible: %s", pending[0].Reason)
	}
}

// This path exists because GITHUB_TOKEN cannot merge workflow files — not
// because the policy should be looser here. It must apply the same gates the
// in-repo job applies, or the escalation route becomes a way around them.
func TestPendingWorkflowPRsAppliesTheSameGatesAsTheInRepoJob(t *testing.T) {
	gh := &fakeGH{responses: map[string]string{
		"pr list": `[
		  {"number":1,"title":"bump actions/checkout from 6.0.0 to 7.0.0","files":[{"path":".github/workflows/ci.yml"}],
		   "isDraft":false,"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"},
		  {"number":2,"title":"bump actions/setup-go from 5.0.0 to 5.0.1","files":[{"path":".github/workflows/ci.yml"}],
		   "isDraft":false,"mergeable":"MERGEABLE","mergeStateStatus":"BLOCKED"},
		  {"number":3,"title":"bump actions/cache from 4.0.0 to 4.0.1","files":[{"path":".github/workflows/ci.yml"}],
		   "isDraft":false,"mergeable":"CONFLICTING","mergeStateStatus":"DIRTY"},
		  {"number":4,"title":"bump some action","files":[{"path":".github/workflows/ci.yml"}],
		   "isDraft":false,"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"}
		]`,
	}}
	pending, err := repogov.PendingWorkflowPRs(context.Background(), gh, "pedromvgomes/demo", repospec.BumpMinor)
	if err != nil {
		t.Fatalf("PendingWorkflowPRs() error = %v", err)
	}
	if len(pending) != 4 {
		t.Fatalf("got %d PRs, want all 4 listed", len(pending))
	}

	wantReason := map[int]string{
		1: "exceeds max_bump", // major bump
		2: "checks not green",
		3: "not mergeable",
		4: "could not parse versions",
	}
	for _, pr := range pending {
		if pr.Eligible {
			t.Errorf("PR #%d should not be eligible", pr.Number)
			continue
		}
		if !strings.Contains(pr.Reason, wantReason[pr.Number]) {
			t.Errorf("PR #%d reason = %q, want it to mention %q", pr.Number, pr.Reason, wantReason[pr.Number])
		}
		// Merging must refuse too, not just the listing.
		if err := repogov.MergePending(context.Background(), gh, pr); err == nil {
			t.Errorf("MergePending(#%d) = nil, want a refusal", pr.Number)
		}
	}
}

func TestParseBump(t *testing.T) {
	tests := map[string]string{
		"bump serde from 1.2.3 to 1.2.4":                    repospec.BumpPatch,
		"bump serde from 1.2.3 to 1.3.0":                    repospec.BumpMinor,
		"bump serde from 1.2.3 to 2.0.0":                    repospec.BumpMajor,
		"bump serde from 1.2.3 to 1.2.4 in /source":         repospec.BumpPatch,
		"chore(deps): bump x from 1.2.3-rc.1 to 1.2.4-rc.2": repospec.BumpPatch,
		"bump the npm group with 3 updates":                 "",
	}
	for title, want := range tests {
		got, ok := repogov.ParseBump(title)
		if want == "" {
			if ok {
				t.Errorf("ParseBump(%q) = %q, want no match", title, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("ParseBump(%q) = %q/%v, want %q", title, got, ok, want)
		}
	}
}

func TestListReposParsesOwnerRepos(t *testing.T) {
	gh := &fakeGH{responses: map[string]string{
		"repo list": `[{"nameWithOwner":"pedromvgomes/gt"},{"nameWithOwner":"pedromvgomes/boma"}]`,
	}}
	repos, err := repogov.ListRepos(context.Background(), gh, "pedromvgomes", 0)
	if err != nil {
		t.Fatalf("ListRepos() error = %v", err)
	}
	if len(repos) != 2 || repos[0] != "pedromvgomes/boma" {
		t.Fatalf("ListRepos() = %v, want sorted owner/name pairs", repos)
	}
	if !strings.Contains(gh.calls[0], "--no-archived") {
		t.Errorf("ListRepos did not exclude archived repos: %q", gh.calls[0])
	}
}

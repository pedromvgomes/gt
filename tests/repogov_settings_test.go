package tests

import (
	"context"
	"encoding/json"
	"errors"
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
  "delete_branch_on_merge": true,
  "squash_merge_commit_title": "PR_TITLE",
  "squash_merge_commit_message": "BLANK"
}`

// rulesetListJSON is the summary list endpoint. gt finds its own ruleset by
// name here before fetching the detail.
func rulesetListJSON(names ...string) string {
	var items []map[string]any
	for i, n := range names {
		items = append(items, map[string]any{
			"id": 100 + i, "name": n, "enforcement": "active", "target": "branch",
		})
	}
	body, _ := json.Marshal(items)
	return string(body)
}

// compliantRulesetJSON is a ruleset detail that matches repospec.Default().
func compliantRulesetJSON(t *testing.T, checks ...string) string {
	t.Helper()
	if len(checks) == 0 {
		checks = []string{repospec.GateCheckJob}
	}
	var required []map[string]any
	for _, c := range checks {
		required = append(required, map[string]any{"context": c})
	}
	body, err := json.Marshal(map[string]any{
		"id": 100, "name": repogov.RulesetName, "target": "branch", "enforcement": "active",
		"conditions": map[string]any{"ref_name": map[string]any{"include": []string{"refs/heads/main"}}},
		"rules": []map[string]any{
			{"type": "deletion"},
			{"type": "non_fast_forward"},
			{"type": "required_linear_history"},
			{"type": "pull_request", "parameters": map[string]any{
				"required_approving_review_count":   0,
				"dismiss_stale_reviews_on_push":     true,
				"required_review_thread_resolution": true,
				"require_code_owner_review":         false,
				"require_last_push_approval":        false,
				"allowed_merge_methods":             []string{"squash"},
			}},
			{"type": "required_status_checks", "parameters": map[string]any{
				"strict_required_status_checks_policy": false,
				"required_status_checks":               required,
			}},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(body)
}

// alignedGH is a fake whose repository, ruleset list and ruleset detail all
// match repospec.Default(), and whose branch has no classic protection.
func alignedGH(t *testing.T) *fakeGH {
	t.Helper()
	return &fakeGH{
		responses: map[string]string{
			"repos/pedromvgomes/demo":              compliantRepoJSON,
			"repos/pedromvgomes/demo/rulesets":     rulesetListJSON(repogov.RulesetName),
			"repos/pedromvgomes/demo/rulesets/100": compliantRulesetJSON(t),
		},
		errors: map[string]error{
			"branches/main/protection": errors.New("gh: Branch not protected (HTTP 404)"),
		},
	}
}

func TestSettingsDiffCleanWhenAligned(t *testing.T) {
	changes, err := repogov.SettingsDiff(context.Background(), alignedGH(t), repospec.Default(), "pedromvgomes", "demo")
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
	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo"] = `{
	  "allow_squash_merge": true,
	  "allow_merge_commit": true,
	  "allow_rebase_merge": false,
	  "delete_branch_on_merge": true,
	  "squash_merge_commit_title": "PR_TITLE",
	  "squash_merge_commit_message": "BLANK"
	}`
	changes, err := repogov.SettingsDiff(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo")
	if err != nil {
		t.Fatalf("SettingsDiff() error = %v", err)
	}
	if len(changes) != 1 || changes[0].Field != "allow_merge_commit" {
		t.Fatalf("SettingsDiff() = %v, want a single allow_merge_commit change", changes)
	}
}

// The ruleset must converge on exactly one context: gt's gate. A repo still
// listing individual CI jobs is drift, because renaming any of them would
// silently unprotect the branch.
func TestSettingsDiffReplacesPerJobRequiredChecks(t *testing.T) {
	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo/rulesets/100"] = compliantRulesetJSON(t, "build", "test")
	changes, err := repogov.SettingsDiff(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo")
	if err != nil {
		t.Fatalf("SettingsDiff() error = %v", err)
	}
	var found bool
	for _, c := range changes {
		if c.Field == "ruleset.required_status_checks" && c.Want == repospec.GateCheckJob {
			found = true
		}
	}
	if !found {
		t.Fatalf("SettingsDiff() = %v, want the gate to replace the per-job checks", changes)
	}
}

// A repository with no gt ruleset is drift to converge from, not an error.
func TestSettingsDiffTreatsMissingRulesetAsDrift(t *testing.T) {
	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo/rulesets"] = `[]`
	changes, err := repogov.SettingsDiff(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo")
	if err != nil {
		t.Fatalf("SettingsDiff() error = %v", err)
	}
	if len(changes) != 1 || changes[0].Got != "absent" {
		t.Fatalf("SettingsDiff() = %v, want a single absent-ruleset change", changes)
	}
}

// Two systems governing one branch is the problem this subsystem exists to
// remove, so gt reports both a second ruleset and leftover classic protection.
// It reports rather than deletes: someone configured those deliberately.
func TestSettingsDiffReportsOtherProtectionOnTheSameBranch(t *testing.T) {
	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo/rulesets"] = rulesetListJSON(repogov.RulesetName, "main branch protection")
	// Rules gt also manages, so removing it loses nothing.
	gh.responses["repos/pedromvgomes/demo/rulesets/101"] = `{"id":101,"name":"main branch protection","target":"branch","enforcement":"active","rules":[{"type":"deletion"},{"type":"non_fast_forward"}]}`
	delete(gh.errors, "branches/main/protection")
	gh.responses["branches/main/protection"] = `{"required_status_checks":{"strict":false,"contexts":[]}}`

	changes, err := repogov.SettingsDiff(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo")
	if err != nil {
		t.Fatalf("SettingsDiff() error = %v", err)
	}
	var sawOther, sawClassic bool
	for _, c := range changes {
		if c.Field == "ruleset main branch protection" {
			sawOther = true
		}
		if c.Field == "classic branch protection" {
			sawClassic = true
		}
	}
	if !sawOther {
		t.Errorf("a second active ruleset on the branch was not reported: %v", changes)
	}
	if !sawClassic {
		t.Errorf("leftover classic branch protection was not reported: %v", changes)
	}
}

// A rule gt does not model is carried into gt's ruleset verbatim and the old
// ruleset is then removed. Dropping it would quietly revoke a protection;
// leaving the old ruleset behind would put two objects on one branch. Copying
// it across is the only option that does neither.
func TestSettingsAbsorbsRulesGtDoesNotModel(t *testing.T) {
	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo/rulesets"] = rulesetListJSON(repogov.RulesetName, "signing")
	gh.responses["repos/pedromvgomes/demo/rulesets/101"] = `{"id":101,"name":"signing","target":"branch","enforcement":"active","rules":[{"type":"required_signatures"}]}`

	changes, err := repogov.SettingsDiff(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo")
	if err != nil {
		t.Fatalf("SettingsDiff() error = %v", err)
	}
	var found bool
	for _, c := range changes {
		if c.Field == "ruleset signing" {
			found = true
			if !strings.Contains(c.Want, "folded into") || !strings.Contains(c.Want, "required_signatures") {
				t.Errorf("want = %q, want it to say folded-in and name required_signatures", c.Want)
			}
		}
	}
	if !found {
		t.Fatalf("unmanaged ruleset not reported: %v", changes)
	}

	if err := repogov.SettingsApply(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}

	var body []byte
	for _, in := range gh.inputs {
		if in != nil {
			body = in
		}
	}
	if !strings.Contains(string(body), "required_signatures") {
		t.Errorf("the unmodelled rule was not carried into gt's ruleset:\n%s", body)
	}
	var deleted bool
	for _, c := range gh.calls {
		if strings.Contains(c, "DELETE") && strings.Contains(c, "/rulesets/101") {
			deleted = true
		}
	}
	if !deleted {
		t.Error("the absorbed ruleset was left behind, so the branch still has two")
	}
}

// bypass_actors are facts about people and apps, not policy gt has an opinion
// on. Apply must write them back untouched or it silently revokes an exemption.
func TestSettingsPreservesBypassActors(t *testing.T) {
	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo/rulesets/100"] = `{"id":100,"name":"gt","target":"branch","enforcement":"active",
	  "bypass_actors":[{"actor_id":5,"actor_type":"Integration","bypass_mode":"always"}],
	  "conditions":{"ref_name":{"include":["refs/heads/main"]}},
	  "rules":[{"type":"deletion"}]}`

	if err := repogov.SettingsApply(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}
	var body []byte
	for _, in := range gh.inputs {
		if in != nil {
			body = in
		}
	}
	if !strings.Contains(string(body), `"actor_id":5`) {
		t.Errorf("bypass actors were dropped on apply:\n%s", body)
	}
}

// The ruleset gt replaces IS removed, or the repository keeps two things
// governing one branch — which is the whole complaint.
func TestSettingsApplyRemovesTheRulesetItSupersedes(t *testing.T) {
	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo/rulesets"] = rulesetListJSON(repogov.RulesetName, "main branch protection")
	gh.responses["repos/pedromvgomes/demo/rulesets/101"] = `{"id":101,"name":"main branch protection","target":"branch","enforcement":"active","rules":[{"type":"deletion"},{"type":"pull_request"}]}`

	if err := repogov.SettingsApply(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}
	var deleted bool
	for _, c := range gh.calls {
		if strings.Contains(c, "DELETE") && strings.Contains(c, "/rulesets/101") {
			deleted = true
		}
	}
	if !deleted {
		t.Fatalf("superseded ruleset was left in place; calls = %v", gh.calls)
	}
}

// gt writes ONE ruleset and never classic protection. Writing both is what put
// two sources of truth on the same branch in the first place.
func TestSettingsApplyWritesARulesetAndNotClassicProtection(t *testing.T) {
	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo/rulesets"] = `[]`
	if err := repogov.SettingsApply(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}

	var createdRuleset bool
	for _, c := range gh.calls {
		if strings.Contains(c, "branches/main/protection") && strings.Contains(c, "PUT") {
			t.Errorf("apply wrote classic branch protection: %s", c)
		}
		if strings.Contains(c, "POST") && strings.HasSuffix(c, "/rulesets --input - --header Accept: application/vnd.github+json") {
			createdRuleset = true
		}
	}
	if !createdRuleset {
		t.Fatalf("apply did not create a ruleset; calls = %v", gh.calls)
	}

	var payload struct {
		Name        string `json:"name"`
		Enforcement string `json:"enforcement"`
		Rules       []struct {
			Type       string `json:"type"`
			Parameters struct {
				AllowedMergeMethods  []string `json:"allowed_merge_methods"`
				RequiredStatusChecks []struct {
					Context string `json:"context"`
				} `json:"required_status_checks"`
			} `json:"parameters"`
		} `json:"rules"`
	}
	var body []byte
	for _, in := range gh.inputs {
		if in != nil {
			body = in
		}
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal ruleset payload: %v", err)
	}
	if payload.Name != repogov.RulesetName || payload.Enforcement != "active" {
		t.Errorf("payload name/enforcement = %q/%q", payload.Name, payload.Enforcement)
	}

	types := map[string]bool{}
	for _, r := range payload.Rules {
		types[r.Type] = true
		if r.Type == "pull_request" && !sameStringSlice(r.Parameters.AllowedMergeMethods, []string{"squash"}) {
			t.Errorf("allowed_merge_methods = %v, want [squash]", r.Parameters.AllowedMergeMethods)
		}
		if r.Type == "required_status_checks" {
			if len(r.Parameters.RequiredStatusChecks) != 1 ||
				r.Parameters.RequiredStatusChecks[0].Context != repospec.GateCheckJob {
				t.Errorf("required checks = %v, want exactly %q", r.Parameters.RequiredStatusChecks, repospec.GateCheckJob)
			}
		}
	}
	for _, want := range []string{"deletion", "non_fast_forward", "required_linear_history", "pull_request", "required_status_checks"} {
		if !types[want] {
			t.Errorf("ruleset is missing the %s rule", want)
		}
	}
}

// An existing gt ruleset is updated in place rather than duplicated.
func TestSettingsApplyUpdatesTheExistingRuleset(t *testing.T) {
	gh := alignedGH(t)
	if err := repogov.SettingsApply(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}
	var updated bool
	for _, c := range gh.calls {
		if strings.Contains(c, "PUT") && strings.Contains(c, "/rulesets/100") {
			updated = true
		}
		if strings.Contains(c, "POST") && strings.Contains(c, "/rulesets ") {
			t.Errorf("apply created a second ruleset instead of updating: %s", c)
		}
	}
	if !updated {
		t.Fatalf("apply did not update ruleset 100; calls = %v", gh.calls)
	}
}

// With CI disabled nothing renders ci-orchestration.yml, so requiring the gate
// would block every PR forever on a check nothing can report.
func TestSettingsApplyOmitsTheGateWhenCIIsDisabled(t *testing.T) {
	spec := repospec.Default()
	spec.Pipeline.CI.Enabled = false
	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo/rulesets"] = `[]`
	if err := repogov.SettingsApply(context.Background(), gh, spec, "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}
	var body []byte
	for _, in := range gh.inputs {
		if in != nil {
			body = in
		}
	}
	if strings.Contains(string(body), "required_status_checks") {
		t.Errorf("gate required with CI disabled:\n%s", body)
	}
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
		// github-actions bumps are the whole reason merge-pending exists: they
		// touch .github/workflows/** and so can never be merged in CI. They are
		// titled with a bare major when the action is pinned to a moving tag.
		"bump actions/checkout from 4 to 5":                 repospec.BumpMajor,
		"bump actions/setup-go from 5 to 5.1":               repospec.BumpMinor,
		"bump actions/cache from v4.0.1 to v4.0.2":          repospec.BumpPatch,
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

// A disabled ruleset is removed like any other — leaving it is the same
// clutter — but its rules are NOT folded into gt's active ruleset. Disabled
// means somebody switched that off deliberately, and carrying it across would
// turn it back on under a different name.
func TestSettingsRemovesDisabledRulesetsWithoutCarryingTheirRules(t *testing.T) {
	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo/rulesets"] = `[
	  {"id":100,"name":"gt","enforcement":"active","target":"branch"},
	  {"id":101,"name":"Code Quality Copilot review","enforcement":"disabled","target":"branch"}
	]`
	gh.responses["repos/pedromvgomes/demo/rulesets/101"] = `{"id":101,"name":"Code Quality Copilot review",
	  "target":"branch","enforcement":"disabled","rules":[{"type":"copilot_code_review"}]}`

	changes, err := repogov.SettingsDiff(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo")
	if err != nil {
		t.Fatalf("SettingsDiff() error = %v", err)
	}
	var reported bool
	for _, c := range changes {
		if strings.Contains(c.Field, "Copilot") {
			reported = true
			if !strings.Contains(c.Want, "disabled") {
				t.Errorf("want = %q, want it to say the rules are not carried over", c.Want)
			}
		}
	}
	if !reported {
		t.Fatalf("disabled ruleset not reported: %v", changes)
	}

	if err := repogov.SettingsApply(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}
	var body []byte
	for _, in := range gh.inputs {
		if in != nil {
			body = in
		}
	}
	if strings.Contains(string(body), "copilot_code_review") {
		t.Errorf("a rule from a DISABLED ruleset was activated by folding it in:\n%s", body)
	}
	var deleted bool
	for _, c := range gh.calls {
		if strings.Contains(c, "DELETE") && strings.Contains(c, "/rulesets/101") {
			deleted = true
		}
	}
	if !deleted {
		t.Error("disabled ruleset was left behind")
	}
}

// The PR-title gate only governs what lands on the default branch if the
// squashed commit actually takes its subject from the PR. GitHub's default,
// COMMIT_OR_PR_TITLE, uses the commit subject when a PR has exactly one commit
// — so a repository can enforce Conventional Commits and still land a
// non-conforming message, with every check green.
func TestSettingsDiffDetectsSquashCommitTitleDrift(t *testing.T) {
	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo"] = `{
	  "allow_squash_merge": true, "allow_merge_commit": false, "allow_rebase_merge": false,
	  "delete_branch_on_merge": true,
	  "squash_merge_commit_title": "COMMIT_OR_PR_TITLE",
	  "squash_merge_commit_message": "COMMIT_MESSAGES"
	}`
	changes, err := repogov.SettingsDiff(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo")
	if err != nil {
		t.Fatalf("SettingsDiff() error = %v", err)
	}
	want := map[string]string{
		"squash_merge_commit_title":   "PR_TITLE",
		"squash_merge_commit_message": "BLANK",
	}
	for _, c := range changes {
		if w, ok := want[c.Field]; ok {
			if c.Want != w {
				t.Errorf("%s want = %q, want %q", c.Field, c.Want, w)
			}
			delete(want, c.Field)
		}
	}
	if len(want) != 0 {
		t.Errorf("undetected drift: %v (changes = %v)", want, changes)
	}
}

// Apply must send both, or a repository on GitHub's defaults stays there.
func TestSettingsApplySendsSquashCommitDefaults(t *testing.T) {
	gh := alignedGH(t)
	if err := repogov.SettingsApply(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}
	var patched string
	for _, c := range gh.calls {
		if strings.Contains(c, "PATCH") {
			patched = c
		}
	}
	for _, want := range []string{"squash_merge_commit_title=PR_TITLE", "squash_merge_commit_message=BLANK"} {
		if !strings.Contains(patched, want) {
			t.Errorf("PATCH does not set %s: %s", want, patched)
		}
	}
}

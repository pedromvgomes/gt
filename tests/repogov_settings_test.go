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
//
// The default spec enables lydite, so a compliant ruleset requires lydite's
// referral status alongside the gate: a referred pull request leaves the lydite
// job green, and that status is the only thing holding it.
func compliantRulesetJSON(t *testing.T, checks ...string) string {
	t.Helper()
	if len(checks) == 0 {
		checks = []string{repospec.GateCheckJob, repospec.LyditeReferralContext}
	}
	var required []map[string]any
	for _, c := range checks {
		required = append(required, map[string]any{"context": c})
	}
	body, err := json.Marshal(map[string]any{
		"id": 100, "name": repogov.RulesetName, "target": "branch", "enforcement": "active",
		"bypass_actors": []map[string]any{
			{"actor_id": 5, "actor_type": "RepositoryRole", "bypass_mode": "pull_request"},
		},
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
				// Every governed repository with a gate holds pull requests to
				// their base.
				"strict_required_status_checks_policy": true,
				"required_status_checks":               required,
			}},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(body)
}

// lyditeRelayVarJSON is the actions/variables list endpoint carrying a single
// GT_LYDITE_RELAY entry, shaped to match what findLyditeRelayVar unmarshals.
func lyditeRelayVarJSON(value string) string {
	return `{"variables":[{"name":"` + repogov.LyditeRelayVar + `","value":"` + value + `"}]}`
}

// alignedGH is a fake whose repository, ruleset list and ruleset detail all
// match repospec.Default(), and whose branch has no classic protection.
func alignedGH(t *testing.T) *fakeGH {
	t.Helper()
	return &fakeGH{
		responses: map[string]string{
			"repos/pedromvgomes/demo":                   compliantRepoJSON,
			"repos/pedromvgomes/demo/rulesets":          rulesetListJSON(repogov.RulesetName),
			"repos/pedromvgomes/demo/rulesets/100":      compliantRulesetJSON(t),
			"repos/pedromvgomes/demo/actions/variables": lyditeRelayVarJSON(repogov.LyditeRelayValue),
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

// The ruleset must converge on gt's gate plus lydite's referral status, and
// nothing else. A repo still listing individual CI jobs is drift, because
// renaming any of them would silently unprotect the branch.
func TestSettingsDiffReplacesPerJobRequiredChecks(t *testing.T) {
	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo/rulesets/100"] = compliantRulesetJSON(t, "build", "test")
	changes, err := repogov.SettingsDiff(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo")
	if err != nil {
		t.Fatalf("SettingsDiff() error = %v", err)
	}
	want := repospec.GateCheckJob + ", " + repospec.LyditeReferralContext
	var found bool
	for _, c := range changes {
		if c.Field == "ruleset.required_status_checks" && c.Want == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("SettingsDiff() = %v, want %q to replace the per-job checks", changes, want)
	}
}

// A referral verdict never fails the lydite job, so ci-gate reports green on a
// pull request lydite has referred. The referral status is required separately
// for exactly that reason — and only where lydite runs to publish it, since a
// context nothing reports blocks every pull request forever.
func TestReferralStatusRequiredOnlyWhereLyditeIsEnabled(t *testing.T) {
	spec := repospec.Default()
	spec.Lydite.Enabled = false

	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo/rulesets/100"] = compliantRulesetJSON(t, repospec.GateCheckJob)
	changes, err := repogov.SettingsDiff(context.Background(), gh, spec, "pedromvgomes", "demo")
	if err != nil {
		t.Fatalf("SettingsDiff() error = %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("SettingsDiff() = %v, want the gate alone to be compliant with lydite off", changes)
	}

	// And a live ruleset still requiring it is drift to converge from.
	gh = alignedGH(t) // its live ruleset requires both
	changes, err = repogov.SettingsDiff(context.Background(), gh, spec, "pedromvgomes", "demo")
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
		t.Fatalf("SettingsDiff() = %v, want the referral context dropped where nothing reports it", changes)
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
			var got []string
			for _, c := range r.Parameters.RequiredStatusChecks {
				got = append(got, c.Context)
			}
			// The default spec enables lydite, so the referral status is
			// required alongside the gate — the gate cannot aggregate it,
			// because a referred pull request leaves its job green.
			want := []string{repospec.GateCheckJob, repospec.LyditeReferralContext}
			if !sameStringSlice(got, want) {
				t.Errorf("required checks = %v, want exactly %v", got, want)
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

		// Requirement bumps, where the manifest pins a constraint rather than
		// a version and Dependabot carries the operator into the title. Cargo
		// writes "=", Ruby "~>", npm "^" — and a repository that pins its
		// toolchain in cargo manifests opens mostly these, all of which were
		// skipped as unparseable.
		"build(deps): update cargo-nextest requirement from =0.9.143 to =0.9.144 in /source/cli/internal/runner/cargo-nextest-pin": repospec.BumpPatch,
		"update rack requirement from ~> 1.2 to ~> 1.3":   repospec.BumpMinor,
		"update lodash requirement from ^1.2.3 to ^2.0.0": repospec.BumpMajor,

		// A compound range reduces to no single old/new pair, so it stays
		// unparseable rather than being classified from its first bound.
		// Misreading this one as a patch bump would merge it unreviewed.
		"update foo requirement from >=1.0,<2.0 to >=1.0,<3.0": "",
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

// A rule gt absorbed on a previous apply lives in gt's OWN ruleset from then
// on. It must not then read as drift: apply deliberately carries unmodelled
// rules through, so reporting one as surplus describes a removal that never
// happens, and leaves `gt repo settings diff` permanently dirty for any
// repository that had a rule to absorb.
//
// wardnet is the live case — its `protected branches` ruleset carried a
// code_quality rule, which the onboarding apply folded into gt's.
func TestSettingsDiffCleanWithAnAbsorbedRule(t *testing.T) {
	gh := alignedGH(t)
	live := compliantRulesetJSON(t)
	// Splice the absorbed rule into gt's own ruleset, as apply left it.
	live = strings.Replace(live, `"rules":[`,
		`"rules":[{"type":"code_quality","parameters":{"severity":"errors"}},`, 1)
	gh.responses["repos/pedromvgomes/demo/rulesets/100"] = live

	changes, err := repogov.SettingsDiff(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo")
	if err != nil {
		t.Fatalf("SettingsDiff() error = %v", err)
	}
	for _, c := range changes {
		if strings.Contains(c.Field, "code_quality") {
			t.Errorf("absorbed rule reported as drift: %s: %s -> %s", c.Field, c.Got, c.Want)
		}
	}
	if len(changes) != 0 {
		t.Fatalf("SettingsDiff() = %v, want none", changes)
	}
}

// The other half of the same contract: a second apply must write the absorbed
// rule back. If it did not, the diff above would be right and the rule would
// be lost on the next run.
func TestSettingsApplyKeepsAnAbsorbedRule(t *testing.T) {
	gh := alignedGH(t)
	live := compliantRulesetJSON(t)
	live = strings.Replace(live, `"rules":[`,
		`"rules":[{"type":"code_quality","parameters":{"severity":"errors"}},`, 1)
	gh.responses["repos/pedromvgomes/demo/rulesets/100"] = live

	if err := repogov.SettingsApply(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}
	var body []byte
	for _, in := range gh.inputs {
		if in != nil {
			body = in
		}
	}
	if !strings.Contains(string(body), "code_quality") {
		t.Errorf("a second apply dropped the absorbed rule:\n%s", body)
	}
}

// With CI disabled gt renders no gate, because requiring a context nothing
// reports blocks every pull request forever. Apply must therefore REMOVE a
// live ci-gate requirement, not carry it: the owned rule set is fixed rather
// than derived from what the spec happens to render, or a conditionally
// omitted rule reads as somebody else's and survives.
func TestSettingsApplyDropsTheGateWhenCIIsDisabled(t *testing.T) {
	gh := alignedGH(t) // live ruleset requires ci-gate
	spec := repospec.Default()
	spec.Pipeline.CI.Enabled = false

	if err := repogov.SettingsApply(context.Background(), gh, spec, "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}
	var body []byte
	for _, in := range gh.inputs {
		if in != nil {
			body = in
		}
	}
	if strings.Contains(string(body), "ci-gate") {
		t.Errorf("apply kept a gate nothing can report, blocking every PR:\n%s", body)
	}
}

// Apply always re-activates gt's own ruleset. If absorbing skipped it while it
// was paused, it would come back active minus every rule gt had absorbed into
// it — a silent protection loss whose only reported change was the enforcement
// flag.
func TestSettingsApplyKeepsAbsorbedRulesFromAPausedGtRuleset(t *testing.T) {
	gh := alignedGH(t)
	live := compliantRulesetJSON(t)
	live = strings.Replace(live, `"rules":[`,
		`"rules":[{"type":"code_quality","parameters":{"severity":"errors"}},`, 1)
	live = strings.Replace(live, `"enforcement":"active"`, `"enforcement":"disabled"`, 1)
	gh.responses["repos/pedromvgomes/demo/rulesets/100"] = live

	if err := repogov.SettingsApply(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}
	var body []byte
	for _, in := range gh.inputs {
		if in != nil {
			body = in
		}
	}
	if !strings.Contains(string(body), "code_quality") {
		t.Errorf("re-activating a paused gt ruleset dropped its absorbed rule:\n%s", body)
	}
}

// lastRulesetBody is the payload apply PUT or POSTed, which is where the rules
// gt actually wrote can be read back.
func lastRulesetBody(gh *fakeGH) string {
	var body []byte
	for _, in := range gh.inputs {
		if in != nil {
			body = in
		}
	}
	return string(body)
}

// withRuleParam returns a ruleset detail with one rule's parameter overridden.
func withRuleParam(t *testing.T, ruleset, ruleType, param string, value any) string {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(ruleset), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, r := range parsed["rules"].([]any) {
		rule := r.(map[string]any)
		if rule["type"] == ruleType {
			rule["parameters"].(map[string]any)[param] = value
		}
	}
	body, err := json.Marshal(parsed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(body)
}

// withMergeQueue returns a ruleset detail carrying a merge_queue rule on top
// of the given one, shaped the way GitHub returns it.
func withMergeQueue(t *testing.T, ruleset string) string {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(ruleset), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	parsed["rules"] = append(parsed["rules"].([]any), map[string]any{
		"type": "merge_queue", "parameters": map[string]any{
			"merge_method":                      "SQUASH",
			"grouping_strategy":                 "ALLGREEN",
			"min_entries_to_merge":              1,
			"min_entries_to_merge_wait_minutes": 5,
			"max_entries_to_build":              5,
			"max_entries_to_merge":              5,
			"check_response_timeout_minutes":    15,
		},
	})
	body, err := json.Marshal(parsed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(body)
}

// Every repository with a required check holds pull requests to their base:
// two green, conflict-free pull requests can still break the branch together,
// and only requiring the branch to be up to date makes the gate run against
// the tree that actually lands. There is no opt-out to honour.
func TestSettingsRequiresBranchesToBeUpToDate(t *testing.T) {
	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo/rulesets/100"] = withRuleParam(t,
		compliantRulesetJSON(t), "required_status_checks", "strict_required_status_checks_policy", false)

	changes, err := repogov.SettingsDiff(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo")
	if err != nil {
		t.Fatalf("SettingsDiff() error = %v", err)
	}
	if len(changes) != 1 || changes[0].Field != "ruleset.strict_required_status_checks_policy" ||
		changes[0].Want != "true" {
		t.Fatalf("SettingsDiff() = %v, want a single strict_required_status_checks_policy -> true", changes)
	}

	if err := repogov.SettingsApply(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}
	if body := lastRulesetBody(gh); !strings.Contains(body, `"strict_required_status_checks_policy":true`) {
		t.Errorf("apply did not require branches to be up to date:\n%s", body)
	}
}

// A spec still carrying a retired freshness key gets the same ruleset as one
// that never did: the key parses, and nothing reads it.
func TestRetiredFreshnessKeysDoNotOptOutOfStrict(t *testing.T) {
	for _, manifest := range []string{
		"settings:\n  branch_protection:\n    base_freshness: none\n",
		"settings:\n  branch_protection:\n    base_freshness: queue\n",
		"settings:\n  branch_protection:\n    require_up_to_date: false\n",
	} {
		spec, err := repospec.Parse([]byte(manifest), "t.yaml")
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", manifest, err)
		}
		gh := alignedGH(t)
		if err := repogov.SettingsApply(context.Background(), gh, spec, "pedromvgomes", "demo"); err != nil {
			t.Fatalf("SettingsApply() error = %v", err)
		}
		body := lastRulesetBody(gh)
		if !strings.Contains(body, `"strict_required_status_checks_policy":true`) {
			t.Errorf("%q opted out of strict:\n%s", manifest, body)
		}
		if strings.Contains(body, "merge_queue") {
			t.Errorf("%q produced a merge queue:\n%s", manifest, body)
		}
	}
}

// With pipeline.ci disabled there is no gate to hold a pull request to, so no
// required_status_checks rule is rendered and strict has nothing to attach to.
func TestSettingsRequiresNoFreshnessWithoutAGate(t *testing.T) {
	spec := repospec.Default()
	spec.Pipeline.CI.Enabled = false
	gh := alignedGH(t)

	if err := repogov.SettingsApply(context.Background(), gh, spec, "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}
	if body := lastRulesetBody(gh); strings.Contains(body, "strict_required_status_checks_policy") {
		t.Errorf("strict was rendered with no required check:\n%s", body)
	}
}

// gt owns the merge_queue rule and never renders it, so a ruleset still
// holding one converges by losing it — and diff names the removal rather than
// leaving apply to do something it never reported.
func TestSettingsRemovesALiveMergeQueue(t *testing.T) {
	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo/rulesets/100"] = withMergeQueue(t, compliantRulesetJSON(t))

	changes, err := repogov.SettingsDiff(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo")
	if err != nil {
		t.Fatalf("SettingsDiff() error = %v", err)
	}
	if len(changes) != 1 || changes[0].Field != "ruleset.rules.merge_queue" ||
		changes[0].Want != "absent" || changes[0].Got != "present" {
		t.Fatalf("SettingsDiff() = %v, want a single ruleset.rules.merge_queue: present -> absent", changes)
	}

	if err := repogov.SettingsApply(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}
	body := lastRulesetBody(gh)
	if strings.Contains(body, "merge_queue") {
		t.Errorf("the live merge queue was carried through instead of removed:\n%s", body)
	}
	if !strings.Contains(body, `"strict_required_status_checks_policy":true`) {
		t.Errorf("removing the queue left the branch without strict:\n%s", body)
	}
}

// A merge queue in a ruleset gt absorbs is not folded into gt's own: the rule
// type is gt-owned, so it is dropped along with the ruleset that held it.
func TestSettingsDoesNotFoldAnotherRulesetsMergeQueue(t *testing.T) {
	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo/rulesets"] = rulesetListJSON(repogov.RulesetName, "legacy")
	gh.responses["repos/pedromvgomes/demo/rulesets/101"] = withMergeQueue(t, `{
	  "id": 101, "name": "legacy", "target": "branch", "enforcement": "active",
	  "conditions": {"ref_name": {"include": ["refs/heads/main"]}},
	  "rules": []
	}`)

	if err := repogov.SettingsApply(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}
	if body := lastRulesetBody(gh); strings.Contains(body, "merge_queue") {
		t.Errorf("another ruleset's merge queue was folded into gt's:\n%s", body)
	}
}

// gt does not manage allow_auto_merge. Nothing gt runs merges through it, and a
// repository may be using it for something gt knows nothing about.
func TestSettingsLeavesAutoMergeAlone(t *testing.T) {
	gh := alignedGH(t)

	if err := repogov.SettingsApply(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}
	for _, c := range gh.calls {
		if strings.Contains(c, "allow_auto_merge") {
			t.Errorf("apply touched auto-merge: %s", c)
		}
	}
}

// merge-pending squash-merges and deletes the branch it merged, asking GitHub
// nothing beyond the merge itself.
func TestMergePendingSquashesAndDeletesTheBranch(t *testing.T) {
	gh := &fakeGH{}
	pr := repogov.PendingPR{
		Repo: "pedromvgomes/demo", Number: 7, Title: "bump x from 1.0.0 to 1.0.1", Eligible: true,
	}
	if err := repogov.MergePending(context.Background(), gh, pr); err != nil {
		t.Fatalf("MergePending() error = %v", err)
	}
	if len(gh.calls) != 1 {
		t.Fatalf("calls = %v, want the merge alone", gh.calls)
	}
	merge := gh.calls[0]
	for _, want := range []string{"pr merge 7", "--repo pedromvgomes/demo", "--squash", "--delete-branch"} {
		if !strings.Contains(merge, want) {
			t.Errorf("merge call = %q, want it to carry %q", merge, want)
		}
	}
}

// GT_LYDITE_RELAY tracks lydite.enabled alone.
func TestLyditeRelayVarTracksLyditeEnabled(t *testing.T) {
	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo/actions/variables"] = `{"variables":[]}`

	changes, err := repogov.SettingsDiff(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo")
	if err != nil {
		t.Fatalf("SettingsDiff() error = %v", err)
	}
	var found bool
	for _, c := range changes {
		if c.Field == repogov.LyditeRelayVar {
			found = true
		}
	}
	if !found {
		t.Fatalf("SettingsDiff() = %v, want a %s change", changes, repogov.LyditeRelayVar)
	}

	spec := repospec.Default()
	spec.Lydite.Enabled = false
	gh = alignedGH(t)
	gh.responses["repos/pedromvgomes/demo/actions/variables"] = `{"variables":[]}`

	changes, err = repogov.SettingsDiff(context.Background(), gh, spec, "pedromvgomes", "demo")
	if err != nil {
		t.Fatalf("SettingsDiff() error = %v", err)
	}
	for _, c := range changes {
		if c.Field == repogov.LyditeRelayVar {
			t.Errorf("SettingsDiff() reported %s with lydite disabled: %v", repogov.LyditeRelayVar, changes)
		}
	}

	if err := repogov.SettingsApply(context.Background(), gh, spec, "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}
	for _, c := range gh.calls {
		if strings.Contains(c, "actions/variables") && (strings.Contains(c, "POST") || strings.Contains(c, "PATCH")) {
			t.Errorf("SettingsApply() wrote %s with lydite disabled: %s", repogov.LyditeRelayVar, c)
		}
	}
}

// An absent variable is reported as unset, not as an empty string, so the
// diff output names what is actually live.
func TestSettingsDiffReportsAnAbsentLyditeRelayVarAsUnset(t *testing.T) {
	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo/actions/variables"] = `{"variables":[]}`

	changes, err := repogov.SettingsDiff(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo")
	if err != nil {
		t.Fatalf("SettingsDiff() error = %v", err)
	}
	var reported repogov.SettingChange
	var found bool
	for _, c := range changes {
		if c.Field == repogov.LyditeRelayVar {
			reported = c
			found = true
		}
	}
	if !found {
		t.Fatalf("SettingsDiff() = %v, want a %s change", changes, repogov.LyditeRelayVar)
	}
	if reported.Got != "(unset)" || reported.Want != repogov.LyditeRelayValue {
		t.Errorf("SettingsDiff() reported %+v, want Got=(unset) Want=%s", reported, repogov.LyditeRelayValue)
	}
}

// A live value that differs from LyditeRelayValue is drift, and the change
// names the old value rather than swallowing it.
func TestSettingsDiffReportsADifferingLyditeRelayVar(t *testing.T) {
	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo/actions/variables"] = lyditeRelayVarJSON("https://stale.example.org")

	changes, err := repogov.SettingsDiff(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo")
	if err != nil {
		t.Fatalf("SettingsDiff() error = %v", err)
	}
	var reported repogov.SettingChange
	var found bool
	for _, c := range changes {
		if c.Field == repogov.LyditeRelayVar {
			reported = c
			found = true
		}
	}
	if !found {
		t.Fatalf("SettingsDiff() = %v, want a %s change", changes, repogov.LyditeRelayVar)
	}
	if reported.Got != "https://stale.example.org" || reported.Want != repogov.LyditeRelayValue {
		t.Errorf("SettingsDiff() reported %+v, want Got=https://stale.example.org Want=%s", reported, repogov.LyditeRelayValue)
	}
}

// A live value already matching LyditeRelayValue is not drift, and diff never
// writes while establishing that: only a GET reaches actions/variables.
func TestSettingsDiffReportsNoChangeWhenLyditeRelayVarAlreadyMatches(t *testing.T) {
	gh := alignedGH(t) // pre-populated with a matching GT_LYDITE_RELAY

	changes, err := repogov.SettingsDiff(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo")
	if err != nil {
		t.Fatalf("SettingsDiff() error = %v", err)
	}
	for _, c := range changes {
		if c.Field == repogov.LyditeRelayVar {
			t.Errorf("SettingsDiff() reported drift on an already-matching value: %v", changes)
		}
	}
	for _, c := range gh.calls {
		if strings.Contains(c, "actions/variables") && (strings.Contains(c, "POST") || strings.Contains(c, "PATCH")) {
			t.Errorf("SettingsDiff() wrote to actions/variables: %s", c)
		}
	}
}

// Apply creates the variable when it is absent, carrying both its name and
// LyditeRelayValue in the POST body.
func TestSettingsApplyCreatesTheLyditeRelayVarWhenAbsent(t *testing.T) {
	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo/actions/variables"] = `{"variables":[]}`

	if err := repogov.SettingsApply(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}
	var created string
	for i, c := range gh.calls {
		if strings.Contains(c, "POST") && strings.HasSuffix(c, "/actions/variables --input - --header Accept: application/vnd.github+json") {
			created = string(gh.inputs[i])
		}
	}
	if created == "" {
		t.Fatalf("apply did not create %s; calls = %v", repogov.LyditeRelayVar, gh.calls)
	}
	if !strings.Contains(created, `"name":"`+repogov.LyditeRelayVar+`"`) || !strings.Contains(created, `"value":"`+repogov.LyditeRelayValue+`"`) {
		t.Errorf("create body = %s, want name and value for %s", created, repogov.LyditeRelayVar)
	}
}

// A failed write is not swallowed: apply must surface it rather than report
// success while the live variable is left exactly as it was.
func TestSettingsApplyPropagatesALyditeRelayVarWriteError(t *testing.T) {
	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo/actions/variables"] = `{"variables":[]}`
	gh.errors["POST repos/pedromvgomes/demo/actions/variables"] = errors.New("rate limited")

	err := repogov.SettingsApply(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo")
	if err == nil {
		t.Fatal("SettingsApply() error = nil, want the write failure to propagate")
	}
}

// Apply updates the variable in place when it is present with a different
// value, carrying only the new value: the path already names the variable.
func TestSettingsApplyUpdatesTheLyditeRelayVarWhenItDiffers(t *testing.T) {
	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo/actions/variables"] = lyditeRelayVarJSON("https://stale.example.org")

	if err := repogov.SettingsApply(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}
	var updated string
	for i, c := range gh.calls {
		if strings.Contains(c, "PATCH") && strings.HasSuffix(c, "/actions/variables/"+repogov.LyditeRelayVar+" --input - --header Accept: application/vnd.github+json") {
			updated = string(gh.inputs[i])
		}
	}
	if updated == "" {
		t.Fatalf("apply did not update %s; calls = %v", repogov.LyditeRelayVar, gh.calls)
	}
	if !strings.Contains(updated, `"value":"`+repogov.LyditeRelayValue+`"`) {
		t.Errorf("update body = %s, want the new value for %s", updated, repogov.LyditeRelayVar)
	}
}

// Apply makes no create/update call when the live value already matches:
// only a GET reaches actions/variables.
func TestSettingsApplyLeavesAMatchingLyditeRelayVarAlone(t *testing.T) {
	gh := alignedGH(t) // pre-populated with a matching GT_LYDITE_RELAY

	if err := repogov.SettingsApply(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}
	for _, c := range gh.calls {
		if strings.Contains(c, "actions/variables") && (strings.Contains(c, "POST") || strings.Contains(c, "PATCH")) {
			t.Errorf("apply wrote to an already-matching %s: %s", repogov.LyditeRelayVar, c)
		}
	}
}

// A behind Dependabot PR is reported as behind, never as failing checks, and
// is not merged; merge-pending asks Dependabot to rebase it.
func TestPendingWorkflowPRsReportsBehindAndRequestsRebase(t *testing.T) {
	gh := &fakeGH{responses: map[string]string{
		"pr list": `[
		  {"number":5,"title":"bump actions/setup-go from 5.0.0 to 5.0.1","files":[{"path":".github/workflows/ci.yml"}],
		   "isDraft":false,"mergeable":"MERGEABLE","mergeStateStatus":"BEHIND"},
		  {"number":6,"title":"bump actions/cache from 4.0.0 to 4.0.1","files":[{"path":".github/workflows/ci.yml"}],
		   "isDraft":false,"mergeable":"MERGEABLE","mergeStateStatus":"BLOCKED"}
		]`,
	}}
	pending, err := repogov.PendingWorkflowPRs(context.Background(), gh, "pedromvgomes/demo", repospec.BumpMinor)
	if err != nil {
		t.Fatalf("PendingWorkflowPRs() error = %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("got %d PRs, want 2", len(pending))
	}
	behind, blocked := pending[0], pending[1]
	if !behind.Behind || behind.Eligible || !strings.Contains(behind.Reason, "behind the base branch") {
		t.Errorf("PR #5 = %+v, want behind, ineligible, reason naming the base branch", behind)
	}
	if behind.Bump != "" {
		t.Errorf("PR #5 Bump = %q, want empty for an ineligible behind PR", behind.Bump)
	}
	if blocked.Behind {
		t.Errorf("PR #6 is BLOCKED, not behind")
	}

	if err := repogov.MergePending(context.Background(), gh, behind); err == nil {
		t.Error("MergePending(behind) = nil, want a refusal")
	}
	if err := repogov.RequestRebase(context.Background(), gh, blocked); err == nil {
		t.Error("RequestRebase(not behind) = nil, want a refusal")
	}
	gh.calls = nil
	if err := repogov.RequestRebase(context.Background(), gh, behind); err != nil {
		t.Fatalf("RequestRebase() error = %v", err)
	}
	if len(gh.calls) != 1 || gh.calls[0] != "pr comment 5 --repo pedromvgomes/demo --body @dependabot rebase" {
		t.Errorf("calls = %v, want exactly one rebase comment", gh.calls)
	}
}

func lastApplyBody(t *testing.T, gh *fakeGH) map[string]any {
	t.Helper()
	var body []byte
	for _, in := range gh.inputs {
		if in != nil {
			body = in
		}
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("ruleset payload is not JSON: %v\n%s", err, body)
	}
	return out
}

func bypassActors(t *testing.T, payload map[string]any) []map[string]any {
	t.Helper()
	raw, ok := payload["bypass_actors"].([]any)
	if !ok {
		t.Fatalf("bypass_actors missing or not a list: %v", payload["bypass_actors"])
	}
	var out []map[string]any
	for _, a := range raw {
		out = append(out, a.(map[string]any))
	}
	return out
}

func TestBypassDefaultsToAdminForPullRequestsOnly(t *testing.T) {
	if got := repospec.Default().Settings.BranchProtection.Bypass; got != repospec.BypassAdmin {
		t.Fatalf("default bypass = %q, want %q", got, repospec.BypassAdmin)
	}
	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo/rulesets"] = `[]`
	if err := repogov.SettingsApply(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}
	actors := bypassActors(t, lastApplyBody(t, gh))
	if len(actors) != 1 || actors[0]["actor_type"] != "RepositoryRole" ||
		actors[0]["actor_id"] != float64(5) || actors[0]["bypass_mode"] != "pull_request" {
		t.Errorf("bypass_actors = %v, want the Admin role for pull requests only", actors)
	}
}

func TestBypassNoneRemovesTheAdminRoleAndKeepsOtherActors(t *testing.T) {
	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo/rulesets/100"] = `{"id":100,"name":"gt","target":"branch","enforcement":"active",
	  "bypass_actors":[
	    {"actor_id":5,"actor_type":"RepositoryRole","bypass_mode":"always"},
	    {"actor_id":9,"actor_type":"Integration","bypass_mode":"always"}],
	  "conditions":{"ref_name":{"include":["refs/heads/main"]}},
	  "rules":[{"type":"deletion"}]}`
	spec := repospec.Default()
	spec.Settings.BranchProtection.Bypass = repospec.BypassNone

	if err := repogov.SettingsApply(context.Background(), gh, spec, "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}
	actors := bypassActors(t, lastApplyBody(t, gh))
	if len(actors) != 1 || actors[0]["actor_type"] != "Integration" {
		t.Errorf("bypass_actors = %v, want only the Integration kept", actors)
	}
}

func TestBypassNoneWritesAnEmptyListSoAnAdminBypassIsCleared(t *testing.T) {
	gh := alignedGH(t)
	spec := repospec.Default()
	spec.Settings.BranchProtection.Bypass = repospec.BypassNone
	if err := repogov.SettingsApply(context.Background(), gh, spec, "pedromvgomes", "demo"); err != nil {
		t.Fatalf("SettingsApply() error = %v", err)
	}
	if actors := bypassActors(t, lastApplyBody(t, gh)); len(actors) != 0 {
		t.Errorf("bypass_actors = %v, want an empty list", actors)
	}
}

func TestBypassAdminRewritesAnAlwaysModeToPullRequest(t *testing.T) {
	gh := alignedGH(t)
	gh.responses["repos/pedromvgomes/demo/rulesets/100"] = `{"id":100,"name":"gt","target":"branch","enforcement":"active",
	  "bypass_actors":[{"actor_id":5,"actor_type":"RepositoryRole","bypass_mode":"always"}],
	  "conditions":{"ref_name":{"include":["refs/heads/main"]}},
	  "rules":[{"type":"deletion"}]}`
	changes, err := repogov.SettingsDiff(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo")
	if err != nil {
		t.Fatalf("SettingsDiff() error = %v", err)
	}
	var found bool
	for _, c := range changes {
		if c.Field == "ruleset.bypass.admin" && c.Want == "pull_request" && c.Got == "always" {
			found = true
		}
	}
	if !found {
		t.Errorf("diff = %v, want ruleset.bypass.admin: pull_request vs always", changes)
	}
}

func TestBypassRejectsUnknownValue(t *testing.T) {
	_, err := repospec.Parse([]byte("settings:\n  branch_protection:\n    bypass: everyone\n"), "x")
	if err == nil || !strings.Contains(err.Error(), "bypass") {
		t.Fatalf("Parse() error = %v, want a bypass complaint", err)
	}
}

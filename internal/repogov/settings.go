package repogov

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/pedromvgomes/gt/internal/repospec"
)

// GH runs the GitHub CLI. It is an interface so settings logic is testable
// without a network or a token.
type GH interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
	// RunWithInput pipes stdin, which `gh api --input -` needs for request
	// bodies too structured to express as repeated -F flags.
	RunWithInput(ctx context.Context, stdin []byte, args ...string) ([]byte, error)
}

// ExecGH shells out to `gh`, reusing whatever credentials the user already has.
// gt never handles a token itself.
type ExecGH struct{}

func (e ExecGH) Run(ctx context.Context, args ...string) ([]byte, error) {
	return e.RunWithInput(ctx, nil, args...)
}

func (ExecGH) RunWithInput(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	// Fixed binary, argv as a slice, no shell. gh is how gt reads and writes
	// the GitHub settings it manages.
	// #nosec G204 -- fixed binary, argv passed directly, no shell involved.
	cmd := exec.CommandContext(ctx, "gh", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("gh %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.Bytes(), nil
}

// SettingChange is one difference between desired and live GitHub state.
type SettingChange struct {
	// Field is the human-facing name of what differs.
	Field string
	Want  string
	Got   string
}

func (c SettingChange) String() string {
	return fmt.Sprintf("%s: %s -> %s", c.Field, c.Got, c.Want)
}

// repoSettings is the subset of the repository API gt manages.
type repoSettings struct {
	AllowSquashMerge    bool `json:"allow_squash_merge"`
	AllowMergeCommit    bool `json:"allow_merge_commit"`
	AllowRebaseMerge    bool `json:"allow_rebase_merge"`
	DeleteBranchOnMerge bool `json:"delete_branch_on_merge"`
}

// RulesetName is the ruleset gt owns. Everything gt enforces on the default
// branch lives in this one object.
//
// Classic branch protection is deliberately not used. Running both leaves two
// places to look and two things to keep in step, and gt exists to remove that
// kind of bookkeeping rather than add to it. Rulesets are also the only one of
// the two that can express allowed merge methods, so squash-only stops being a
// repository-wide toggle a ruleset could silently contradict.
const RulesetName = "gt"

// desiredRuleset is the ruleset gt renders from the spec.
//
// Everything is in one object on purpose: the required check, the pull-request
// requirement, the merge methods, and the history guarantees. A repository
// either matches it or does not.
func desiredRuleset(spec repospec.Spec) map[string]any {
	bp := spec.Settings.BranchProtection
	m := spec.Settings.Merge

	// Merge methods are enforced here rather than only as repository toggles,
	// because a toggle governs which buttons exist while this governs what may
	// land on the protected branch.
	var methods []string
	if m.Squash {
		methods = append(methods, "squash")
	}
	if m.MergeCommit {
		methods = append(methods, "merge")
	}
	if m.Rebase {
		methods = append(methods, "rebase")
	}

	rules := []map[string]any{
		{"type": "deletion"},
		{"type": "non_fast_forward"},
		{"type": "required_linear_history"},
		{"type": "pull_request", "parameters": map[string]any{
			"required_approving_review_count":   bp.RequiredApprovals,
			"dismiss_stale_reviews_on_push":     false,
			"require_code_owner_review":         false,
			"require_last_push_approval":        false,
			"required_review_thread_resolution": false,
			"allowed_merge_methods":             methods,
		}},
	}

	// Only require the gate when something renders it. With CI disabled there
	// is no ci-orchestration.yml, so requiring the context would block every PR
	// forever on a check nothing can report.
	if spec.Pipeline.CI.Enabled {
		rules = append(rules, map[string]any{
			"type": "required_status_checks",
			"parameters": map[string]any{
				"strict_required_status_checks_policy": bp.RequireUpToDate,
				"do_not_enforce_on_create":             false,
				"required_status_checks": []map[string]any{
					{"context": repospec.GateCheckJob},
				},
			},
		})
	}

	return map[string]any{
		"name":        RulesetName,
		"target":      "branch",
		"enforcement": "active",
		"conditions": map[string]any{
			"ref_name": map[string]any{
				"include": []string{"refs/heads/" + bp.Branch},
				"exclude": []string{},
			},
		},
		"rules": rules,
	}
}

// liveRuleset is the parsed shape of what GitHub returns for a ruleset.
type liveRuleset struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Target      string `json:"target"`
	Enforcement string `json:"enforcement"`
	Conditions  struct {
		RefName struct {
			Include []string `json:"include"`
		} `json:"ref_name"`
	} `json:"conditions"`
	Rules []struct {
		Type       string `json:"type"`
		Parameters struct {
			RequiredApprovingReviewCount     int      `json:"required_approving_review_count"`
			AllowedMergeMethods              []string `json:"allowed_merge_methods"`
			StrictRequiredStatusChecksPolicy bool     `json:"strict_required_status_checks_policy"`
			RequiredStatusChecks             []struct {
				Context string `json:"context"`
			} `json:"required_status_checks"`
		} `json:"parameters"`
	} `json:"rules"`
}

// findRuleset returns gt's ruleset, and every other active ruleset targeting
// the same branch.
//
// The second return value is the point: another ruleset on the same branch is
// the two-systems problem again, and gt reports it rather than deleting
// something a human set up deliberately.
func findRuleset(ctx context.Context, gh GH, owner, name, branch string) (*liveRuleset, []liveRuleset, error) {
	raw, err := gh.Run(ctx, "api", fmt.Sprintf("repos/%s/%s/rulesets", owner, name))
	if err != nil {
		return nil, nil, fmt.Errorf("list rulesets: %w", err)
	}
	var summaries []struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		Enforcement string `json:"enforcement"`
		Target      string `json:"target"`
	}
	if err := json.Unmarshal(raw, &summaries); err != nil {
		return nil, nil, fmt.Errorf("parse rulesets: %w", err)
	}

	var mine *liveRuleset
	var others []liveRuleset
	for _, sum := range summaries {
		if sum.Name != RulesetName && (sum.Enforcement != "active" || sum.Target != "branch") {
			continue
		}
		detail, err := gh.Run(ctx, "api", fmt.Sprintf("repos/%s/%s/rulesets/%d", owner, name, sum.ID))
		if err != nil {
			return nil, nil, fmt.Errorf("read ruleset %d: %w", sum.ID, err)
		}
		var parsed liveRuleset
		if err := json.Unmarshal(detail, &parsed); err != nil {
			return nil, nil, fmt.Errorf("parse ruleset %d: %w", sum.ID, err)
		}
		if sum.Name == RulesetName {
			mine = &parsed
			continue
		}
		others = append(others, parsed)
	}
	sort.Slice(others, func(i, j int) bool { return others[i].Name < others[j].Name })
	return mine, others, nil
}

// supersededBy reports whether every rule type in `other` is also managed by
// gt's ruleset, so removing `other` loses nothing.
//
// This is what separates "gt replaces your ad-hoc protection" from "gt deletes
// a rule you needed". A ruleset doing something gt does not model — required
// signatures, push restrictions, a tag target — is never removed; gt reports it
// and leaves the decision to a human.
func supersededBy(other liveRuleset, spec repospec.Spec) (bool, []string) {
	managed := map[string]bool{}
	for _, r := range desiredRuleset(spec)["rules"].([]map[string]any) {
		managed[r["type"].(string)] = true
	}
	var unmanaged []string
	for _, r := range other.Rules {
		if !managed[r.Type] {
			unmanaged = append(unmanaged, r.Type)
		}
	}
	sort.Strings(unmanaged)
	return len(unmanaged) == 0, unmanaged
}

// rulesetChanges compares gt's ruleset against what the spec asks for.
func rulesetChanges(spec repospec.Spec, live *liveRuleset) []SettingChange {
	bp := spec.Settings.BranchProtection
	want := desiredRuleset(spec)

	if live == nil {
		return []SettingChange{{
			Field: "ruleset " + RulesetName, Want: "configured", Got: "absent",
		}}
	}

	var changes []SettingChange
	add := func(field, w, g string) {
		if w != g {
			changes = append(changes, SettingChange{Field: field, Want: w, Got: g})
		}
	}

	add("ruleset.enforcement", "active", live.Enforcement)
	add("ruleset.target", "branch", live.Target)
	add("ruleset.ref_name.include",
		"refs/heads/"+bp.Branch,
		strings.Join(live.Conditions.RefName.Include, ", "))

	wantTypes := map[string]bool{}
	for _, r := range want["rules"].([]map[string]any) {
		wantTypes[r["type"].(string)] = true
	}
	gotTypes := map[string]bool{}
	for _, r := range live.Rules {
		gotTypes[r.Type] = true
	}
	for t := range wantTypes {
		if !gotTypes[t] {
			add("ruleset.rules."+t, "present", "absent")
		}
	}
	for t := range gotTypes {
		if !wantTypes[t] {
			add("ruleset.rules."+t, "absent", "present")
		}
	}

	for _, r := range live.Rules {
		switch r.Type {
		case "pull_request":
			add("ruleset.required_approving_review_count",
				fmt.Sprint(bp.RequiredApprovals),
				fmt.Sprint(r.Parameters.RequiredApprovingReviewCount))
			wantMethods, _ := want["rules"].([]map[string]any)
			var wm []string
			for _, wr := range wantMethods {
				if wr["type"] == "pull_request" {
					wm, _ = wr["parameters"].(map[string]any)["allowed_merge_methods"].([]string)
				}
			}
			if !sameStrings(wm, r.Parameters.AllowedMergeMethods) {
				add("ruleset.allowed_merge_methods",
					strings.Join(wm, ", "),
					strings.Join(r.Parameters.AllowedMergeMethods, ", "))
			}
		case "required_status_checks":
			var got []string
			for _, c := range r.Parameters.RequiredStatusChecks {
				got = append(got, c.Context)
			}
			var wantChecks []string
			if spec.Pipeline.CI.Enabled {
				wantChecks = []string{repospec.GateCheckJob}
			}
			if !sameStrings(wantChecks, got) {
				add("ruleset.required_status_checks",
					strings.Join(wantChecks, ", "), strings.Join(got, ", "))
			}
			add("ruleset.strict_required_status_checks_policy",
				fmt.Sprint(bp.RequireUpToDate),
				fmt.Sprint(r.Parameters.StrictRequiredStatusChecksPolicy))
		}
	}
	return changes
}

// SettingsDiff reports what `SettingsApply` would change.
//
// The ruleset always requires exactly one check — gt's gate. The checks a repo
// actually cares about are declared in .gt-repo.yaml and enforced by the gate
// aggregating them, which is what keeps this list stable forever instead of
// needing an update every time a CI job is renamed.
func SettingsDiff(ctx context.Context, gh GH, spec repospec.Spec, owner, name string) ([]SettingChange, error) {
	if owner == "" || name == "" {
		return nil, fmt.Errorf("could not determine the GitHub repository from origin")
	}
	var changes []SettingChange

	raw, err := gh.Run(ctx, "api", fmt.Sprintf("repos/%s/%s", owner, name))
	if err != nil {
		return nil, err
	}
	var live repoSettings
	if err := json.Unmarshal(raw, &live); err != nil {
		return nil, fmt.Errorf("parse repository settings: %w", err)
	}

	m := spec.Settings.Merge
	for _, c := range []struct {
		field string
		want  bool
		got   bool
	}{
		{"allow_squash_merge", m.Squash, live.AllowSquashMerge},
		{"allow_merge_commit", m.MergeCommit, live.AllowMergeCommit},
		{"allow_rebase_merge", m.Rebase, live.AllowRebaseMerge},
		{"delete_branch_on_merge", m.DeleteBranchOnMerge, live.DeleteBranchOnMerge},
	} {
		if c.want != c.got {
			changes = append(changes, SettingChange{
				Field: c.field, Want: fmt.Sprint(c.want), Got: fmt.Sprint(c.got),
			})
		}
	}

	bp := spec.Settings.BranchProtection
	mine, others, err := findRuleset(ctx, gh, owner, name, bp.Branch)
	if err != nil {
		return nil, err
	}
	changes = append(changes, rulesetChanges(spec, mine)...)

	// Reported, never removed. A second ruleset on the same branch is somebody's
	// deliberate decision, and gt deleting it would be exactly the kind of
	// surprise this subsystem is supposed to prevent — but leaving it unmentioned
	// would let gt claim a repository matches while something else also governs
	// the branch.
	for _, o := range others {
		if ok, unmanaged := supersededBy(o, spec); ok {
			changes = append(changes, SettingChange{
				Field: "ruleset " + o.Name,
				Want:  "removed (fully covered by the " + RulesetName + " ruleset)",
				Got:   "also active on this branch",
			})
			continue
		} else {
			changes = append(changes, SettingChange{
				Field: "ruleset " + o.Name,
				Want:  "kept; gt does not manage " + strings.Join(unmanaged, ", "),
				Got:   "also active on this branch",
			})
		}
	}

	// Same reasoning for classic protection, which gt no longer writes.
	if _, err := gh.Run(ctx, "api", fmt.Sprintf("repos/%s/%s/branches/%s/protection", owner, name, bp.Branch)); err == nil {
		changes = append(changes, SettingChange{
			Field: "classic branch protection", Want: "removed; gt uses a ruleset", Got: "present",
		})
	}

	return changes, nil
}

// SettingsApply pushes the desired merge settings and gt's ruleset.
func SettingsApply(ctx context.Context, gh GH, spec repospec.Spec, owner, name string) error {
	if owner == "" || name == "" {
		return fmt.Errorf("could not determine the GitHub repository from origin")
	}

	m := spec.Settings.Merge
	repoArgs := []string{
		"api", "--method", "PATCH", fmt.Sprintf("repos/%s/%s", owner, name),
		"-F", fmt.Sprintf("allow_squash_merge=%t", m.Squash),
		"-F", fmt.Sprintf("allow_merge_commit=%t", m.MergeCommit),
		"-F", fmt.Sprintf("allow_rebase_merge=%t", m.Rebase),
		"-F", fmt.Sprintf("delete_branch_on_merge=%t", m.DeleteBranchOnMerge),
	}
	if _, err := gh.Run(ctx, repoArgs...); err != nil {
		return err
	}

	bp := spec.Settings.BranchProtection
	mine, others, err := findRuleset(ctx, gh, owner, name, bp.Branch)
	if err != nil {
		return err
	}

	body, err := json.Marshal(desiredRuleset(spec))
	if err != nil {
		return fmt.Errorf("encode ruleset payload: %w", err)
	}

	method, path := "POST", fmt.Sprintf("repos/%s/%s/rulesets", owner, name)
	if mine != nil {
		method = "PUT"
		path = fmt.Sprintf("repos/%s/%s/rulesets/%d", owner, name, mine.ID)
	}
	if _, err := gh.RunWithInput(ctx, body,
		"api", "--method", method, path,
		"--input", "-",
		"--header", "Accept: application/vnd.github+json",
	); err != nil {
		return err
	}

	// Remove what gt just replaced, but only where nothing is lost. Leaving the
	// old ruleset in place is how a repository ends up with two things governing
	// one branch, which is the problem this subsystem exists to remove; deleting
	// one that does something gt does not model would be worse.
	for _, o := range others {
		ok, _ := supersededBy(o, spec)
		if !ok {
			continue
		}
		if _, err := gh.Run(ctx, "api", "--method", "DELETE",
			fmt.Sprintf("repos/%s/%s/rulesets/%d", owner, name, o.ID)); err != nil {
			return fmt.Errorf("remove superseded ruleset %q: %w", o.Name, err)
		}
	}
	return nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

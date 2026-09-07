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
	AllowSquashMerge         bool   `json:"allow_squash_merge"`
	AllowMergeCommit         bool   `json:"allow_merge_commit"`
	AllowRebaseMerge         bool   `json:"allow_rebase_merge"`
	DeleteBranchOnMerge      bool   `json:"delete_branch_on_merge"`
	SquashMergeCommitTitle   string `json:"squash_merge_commit_title"`
	SquashMergeCommitMessage string `json:"squash_merge_commit_message"`
}

// githubSquashTitle and githubSquashMessage translate the spec's lowercase
// vocabulary into the API's. Keeping the spec in its own words means a GitHub
// rename does not become a breaking change to every .gt-repo.yaml.
var githubSquashTitle = map[string]string{
	repospec.SquashTitlePR:       "PR_TITLE",
	repospec.SquashTitleCommitPR: "COMMIT_OR_PR_TITLE",
}

var githubSquashMessage = map[string]string{
	repospec.SquashMessageBlank:   "BLANK",
	repospec.SquashMessagePRBody:  "PR_BODY",
	repospec.SquashMessageCommits: "COMMIT_MESSAGES",
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
			"dismiss_stale_reviews_on_push":     bp.DismissStaleReviews,
			"require_code_owner_review":         bp.RequireCodeOwnerReview,
			"require_last_push_approval":        bp.RequireLastPushApproval,
			"required_review_thread_resolution": bp.RequireThreadResolution,
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
	// BypassActors is kept as raw JSON and written back untouched. gt has no
	// opinion about who may bypass a branch's rules — that is a per-repository
	// fact about people and apps — and dropping it on apply would quietly
	// revoke an exemption somebody relies on.
	BypassActors []json.RawMessage `json:"bypass_actors"`
	Rules        []liveRule        `json:"rules"`
}

// liveRule keeps the parsed fields gt compares AND the original JSON, so a rule
// type gt does not model can be carried through verbatim rather than dropped.
type liveRule struct {
	Type       string `json:"type"`
	Parameters struct {
		RequiredApprovingReviewCount     int      `json:"required_approving_review_count"`
		DismissStaleReviewsOnPush        bool     `json:"dismiss_stale_reviews_on_push"`
		RequireCodeOwnerReview           bool     `json:"require_code_owner_review"`
		RequireLastPushApproval          bool     `json:"require_last_push_approval"`
		RequiredReviewThreadResolution   bool     `json:"required_review_thread_resolution"`
		AllowedMergeMethods              []string `json:"allowed_merge_methods"`
		StrictRequiredStatusChecksPolicy bool     `json:"strict_required_status_checks_policy"`
		RequiredStatusChecks             []struct {
			Context string `json:"context"`
		} `json:"required_status_checks"`
	} `json:"parameters"`
	raw json.RawMessage
}

func (r *liveRule) UnmarshalJSON(data []byte) error {
	type plain liveRule
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*r = liveRule(p)
	r.raw = append(json.RawMessage(nil), data...)
	return nil
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
		// Disabled rulesets are included deliberately. They are still objects
		// governing this branch in the UI, and leaving them behind is the same
		// clutter as leaving an active one — gt removes them too. What it must
		// NOT do is carry their rules across: disabled means somebody switched
		// that off on purpose, and folding it into an active ruleset would turn
		// it back on under a different name.
		if sum.Name != RulesetName && sum.Target != "branch" {
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

// gtRuleTypes is every rule type gt owns — the ones it renders when it wants
// them AND the ones it deliberately omits.
//
// Fixed rather than derived from desiredRuleset(spec), which is the point.
// required_status_checks is conditional there: with pipeline.ci disabled gt
// renders no gate, because requiring a context nothing reports would block
// every pull request forever. Deriving the owned set from that output made the
// rule look *unmanaged* the moment it was omitted, so apply carried a live
// `ci-gate` requirement through verbatim and the repository kept the very gate
// the omission exists to remove — permanently, because diff asked for a
// removal apply would never perform.
var gtRuleTypes = map[string]bool{
	"deletion":                true,
	"non_fast_forward":        true,
	"required_linear_history": true,
	"pull_request":            true,
	"required_status_checks":  true,
}

// managedRuleTypes is the set gt owns, and so the set it will remove when it
// does not want it.
func managedRuleTypes(repospec.Spec) map[string]bool {
	return gtRuleTypes
}

// unmanagedRules returns the rules in `other` that gt does not render itself —
// code_quality and copilot_code_review being the ones in use today.
//
// They are not dropped and they do not force a second ruleset to survive:
// apply copies them verbatim into gt's own ruleset and then removes the old
// one, so the branch ends up governed by exactly one object with the same
// protections it had before.
func unmanagedRules(other liveRuleset, spec repospec.Spec) []liveRule {
	// See findRuleset: a disabled ruleset contributes nothing, because its rules
	// are not in force and copying them would enable them.
	if other.Enforcement != "active" {
		return nil
	}
	return unmanagedRulesOf(other, spec)
}

// unmanagedRulesOf is unmanagedRules without the enforcement gate, for gt's
// OWN ruleset.
//
// That gate exists so absorbing a ruleset somebody switched off does not
// silently switch its rules back on. It must not apply to gt's own ruleset,
// which apply always re-activates: skipping it there meant a ruleset paused in
// the UI came back active minus every rule gt had previously absorbed into it,
// on a run whose only reported change was the enforcement flag.
func unmanagedRulesOf(other liveRuleset, spec repospec.Spec) []liveRule {
	managed := managedRuleTypes(spec)
	var out []liveRule
	for _, r := range other.Rules {
		if !managed[r.Type] {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
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
	// Deliberately no converse loop. A live rule gt does not model is one it
	// absorbed from a ruleset it superseded, and apply carries those through
	// every time — including out of gt's own ruleset on a later run. Reporting
	// one as surplus would describe a removal that never happens and leave
	// `settings diff` permanently dirty for any repository that had a rule to
	// absorb. See unmanagedRules.

	for _, r := range live.Rules {
		switch r.Type {
		case "pull_request":
			add("ruleset.required_approving_review_count",
				fmt.Sprint(bp.RequiredApprovals),
				fmt.Sprint(r.Parameters.RequiredApprovingReviewCount))
			add("ruleset.dismiss_stale_reviews_on_push",
				fmt.Sprint(bp.DismissStaleReviews),
				fmt.Sprint(r.Parameters.DismissStaleReviewsOnPush))
			add("ruleset.required_review_thread_resolution",
				fmt.Sprint(bp.RequireThreadResolution),
				fmt.Sprint(r.Parameters.RequiredReviewThreadResolution))
			add("ruleset.require_code_owner_review",
				fmt.Sprint(bp.RequireCodeOwnerReview),
				fmt.Sprint(r.Parameters.RequireCodeOwnerReview))
			add("ruleset.require_last_push_approval",
				fmt.Sprint(bp.RequireLastPushApproval),
				fmt.Sprint(r.Parameters.RequireLastPushApproval))
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

	// What the squashed commit actually says. Drift here is the quiet kind: the
	// PR-title gate keeps passing while a non-conforming subject lands on the
	// default branch, because GitHub took it from the single commit instead.
	for _, c := range []struct{ field, want, got string }{
		{"squash_merge_commit_title", githubSquashTitle[m.SquashTitle], live.SquashMergeCommitTitle},
		{"squash_merge_commit_message", githubSquashMessage[m.SquashMessage], live.SquashMergeCommitMessage},
	} {
		if c.want != c.got {
			changes = append(changes, SettingChange{Field: c.field, Want: c.want, Got: c.got})
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
		want := "removed (fully covered by the " + RulesetName + " ruleset)"
		if o.Enforcement != "active" {
			want = "removed (disabled; its rules are not carried over)"
		}
		if extra := unmanagedRules(o, spec); len(extra) > 0 {
			var types []string
			for _, r := range extra {
				types = append(types, r.Type)
			}
			want = "folded into " + RulesetName + " (carrying " + strings.Join(types, ", ") + ") and removed"
		}
		got := "also active on this branch"
		if o.Enforcement != "active" {
			got = "present on this branch (" + o.Enforcement + ")"
		}
		changes = append(changes, SettingChange{Field: "ruleset " + o.Name, Want: want, Got: got})
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
		"-f", fmt.Sprintf("squash_merge_commit_title=%s", githubSquashTitle[m.SquashTitle]),
		"-f", fmt.Sprintf("squash_merge_commit_message=%s", githubSquashMessage[m.SquashMessage]),
	}
	if _, err := gh.Run(ctx, repoArgs...); err != nil {
		return err
	}

	bp := spec.Settings.BranchProtection
	mine, others, err := findRuleset(ctx, gh, owner, name, bp.Branch)
	if err != nil {
		return err
	}

	payload := desiredRuleset(spec)

	// Carry through everything gt does not model, from gt's own ruleset and
	// from the ones it is about to absorb: rule types gt has no opinion about,
	// and the bypass actors, which are facts about people rather than policy.
	// Dropping either would mean apply quietly removed a protection or an
	// exemption that nobody asked it to touch.
	sources := append([]liveRuleset(nil), others...)
	if mine != nil {
		sources = append([]liveRuleset{*mine}, sources...)
	}
	carried := map[string]json.RawMessage{}
	var bypass []json.RawMessage
	for i, src := range sources {
		// sources[0] is gt's own ruleset when it exists; its absorbed rules
		// survive even while it is paused. See unmanagedRulesOf.
		rules := unmanagedRules(src, spec)
		if i == 0 && mine != nil {
			rules = unmanagedRulesOf(src, spec)
		}
		for _, r := range rules {
			if _, seen := carried[r.Type]; !seen {
				carried[r.Type] = r.raw
			}
		}
		if len(bypass) == 0 && src.Enforcement == "active" {
			bypass = src.BypassActors
		}
	}
	if len(carried) > 0 {
		types := make([]string, 0, len(carried))
		for t := range carried {
			types = append(types, t)
		}
		sort.Strings(types)
		rules := payload["rules"].([]map[string]any)
		raw := make([]json.RawMessage, 0, len(rules)+len(types))
		for _, r := range rules {
			b, err := json.Marshal(r)
			if err != nil {
				return fmt.Errorf("encode rule: %w", err)
			}
			raw = append(raw, b)
		}
		for _, t := range types {
			raw = append(raw, carried[t])
		}
		payload["rules"] = raw
	}
	if len(bypass) > 0 {
		payload["bypass_actors"] = bypass
	}

	body, err := json.Marshal(payload)
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

	// Remove what gt just replaced. Safe now that anything gt does not model was
	// copied into the ruleset above: leaving them would put two objects on one
	// branch, which is the problem this subsystem exists to remove.
	for _, o := range others {
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

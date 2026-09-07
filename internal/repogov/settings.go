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
	"gopkg.in/yaml.v3"
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
	AllowAutoMerge           bool   `json:"allow_auto_merge"`
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

// Merge-queue tuning. Shared policy, so it lives here rather than in every
// .gt-repo.yaml — changing an opinion for the whole fleet stays a one-line edit
// in gt, and a repository cannot quietly pin an old value.
//
// The numbers are chosen against the fleet's actual CI: ci-gate takes about two
// minutes, and an already-validated tree skips every stage and costs seconds.
// That cheapness is what makes a queue affordable at all, and it is why the
// batch sizes are small — batching exists to amortise expensive CI, and
// amortising two minutes is not worth the ambiguity of a failed group nobody
// can attribute to one pull request.
const (
	// queueGroupingStrategy: every entry in a group must be green, not just
	// its head. HEADGREEN merges entries whose own checks never passed on the
	// grounds that the tip is green, which is precisely the "it built once,
	// somewhere" reasoning this whole change exists to remove.
	queueGroupingStrategy = "ALLGREEN"
	// queueMinEntriesToMerge: merge as soon as one entry is ready. Waiting to
	// accumulate a batch would add latency to every single-PR day, which is
	// most of them.
	queueMinEntriesToMerge = 1
	// queueMinEntriesToMergeWaitMinutes caps how long the queue may hold a
	// ready entry hoping for company. With a minimum of one it is nearly
	// always moot; it bounds the pathological case rather than shaping the
	// common one.
	queueMinEntriesToMergeWaitMinutes = 5
	// queueMaxEntriesToBuild / queueMaxEntriesToMerge bound a group. Five is
	// well above what any repository in the fleet merges concurrently, so it
	// is a ceiling rather than a throttle.
	queueMaxEntriesToBuild = 5
	queueMaxEntriesToMerge = 5
	// queueCheckResponseTimeoutMinutes is how long the queue waits for ci-gate
	// to report before ejecting the entry. Generous against a two-minute
	// pipeline on purpose: the cost of being too short is a pull request
	// thrown out of the queue on a slow-runner day, and the cost of being too
	// long is a few extra minutes on the rare run where a check never reports
	// at all.
	queueCheckResponseTimeoutMinutes = 15
)

// githubMergeMethod translates gt's merge vocabulary into the queue API's.
// Kept as a map for the same reason as the squash-title one: GitHub renaming a
// constant must not become a breaking change to every .gt-repo.yaml.
var githubMergeMethod = map[string]string{
	repospec.MergeMethodSquash: "SQUASH",
	repospec.MergeMethodRebase: "REBASE",
	repospec.MergeMethodMerge:  "MERGE",
}

// queueParameters is the merge_queue rule body.
//
// merge_method is derived from the repository's allowed methods rather than
// configured separately, because the queue merges on the repository's behalf:
// a queue landing something a human could not is the same policy hole twice.
// Validation has already refused the one combination that cannot work — a
// merge-commit queue under required_linear_history.
func queueParameters(spec repospec.Spec) map[string]any {
	return map[string]any{
		"merge_method":                      githubMergeMethod[spec.Settings.QueueMergeMethod()],
		"grouping_strategy":                 queueGroupingStrategy,
		"min_entries_to_merge":              queueMinEntriesToMerge,
		"min_entries_to_merge_wait_minutes": queueMinEntriesToMergeWaitMinutes,
		"max_entries_to_build":              queueMaxEntriesToBuild,
		"max_entries_to_merge":              queueMaxEntriesToMerge,
		"check_response_timeout_minutes":    queueCheckResponseTimeoutMinutes,
	}
}

// MergeQueueChangeField is the Field a deferral is reported under. Named so
// the command layer can pick it out of a change list and say the queue was not
// applied *after* the apply, where an aborted rollout is easiest to miss.
const MergeQueueChangeField = "merge queue"

// mergeQueueState is the mechanism gt has settled on for holding a pull request
// to the branch it will land on, once the spec, the platform and the rendered
// workflow have all been consulted.
//
// Exactly one is ever applied. GitHub offers two and advises against combining
// them, and the advice is not stylistic: strict checks block the merge button
// until the author rebases, and a queue then rebases and rebuilds the same pull
// request itself. Running both pays the rebase churn and the queue latency for
// a single guarantee. Making this a choice of one rather than two booleans is
// what makes that structural instead of a validation rule.
type mergeQueueState int

const (
	// mergeQueueOff: neither mechanism. The repository gave the guarantee up,
	// or has no CI for either to act on. gt owns both and removes them.
	mergeQueueOff mergeQueueState = iota
	// mergeQueueOn: the merge_queue rule.
	mergeQueueOn
	// mergeQueueStrict: strict_required_status_checks_policy, the fallback
	// where a queue is impossible. Weaker on ergonomics — somebody has to
	// rebase — and identical on the guarantee.
	mergeQueueStrict
	// mergeQueueDeferred: a queue is possible and wanted, and gt will not apply
	// one yet. The rule becomes unmanaged, so whatever is live is carried
	// through untouched — a transient API failure or a half-finished rollout
	// must never dismantle a queue that is already working.
	//
	// Strict is deliberately NOT applied in the meantime. The window is one
	// sync and one merge long, and switching it on and back off again would
	// turn every open pull request red twice for a gap that closes by itself.
	mergeQueueDeferred
)

// mergeQueueDecision is a mechanism plus, where gt chose it rather than being
// told, the sentence explaining why. The reason is reported as a setting change
// so nobody has to infer which mechanism a repository ended up with.
type mergeQueueDecision struct {
	State  mergeQueueState
	Reason string
	// Remedy is what the person reading the diff should do about it, where
	// there is anything to do.
	Remedy string
}

// resolveMergeQueue picks the mechanism for this repository.
//
// The order matters. The spec first, because an explicit choice is not gt's to
// second-guess; then whether there is a pipeline for either mechanism to act
// on; then the platform, which decides what is even possible; and the workflow
// check last, because it is the one whose answer changes as a rollout proceeds.
func resolveMergeQueue(
	ctx context.Context, gh GH, spec repospec.Spec, owner, name string,
) mergeQueueDecision {
	bp := spec.Settings.BranchProtection
	if bp.BaseFreshness == repospec.FreshnessNone {
		return mergeQueueDecision{State: mergeQueueOff}
	}

	// Neither mechanism means anything without a required check. A queue would
	// serialise merges and build nothing; strict would compare a pull request
	// against a base for the benefit of no check at all. Both are omitted for
	// the same reason the required-status-checks rule is.
	if !spec.Pipeline.CI.Enabled {
		return mergeQueueDecision{
			State:  mergeQueueOff,
			Reason: "pipeline.ci is disabled, so neither mechanism has a check to hold a pull request to",
		}
	}

	if bp.BaseFreshness == repospec.FreshnessStrict {
		return mergeQueueDecision{State: mergeQueueStrict}
	}

	// Established, not assumed — and established from the repository's own
	// facts rather than from a feature probe, because there is no probe that
	// answers this. See queueImpossible.
	impossible, err := queueImpossible(ctx, gh, owner, name)
	switch {
	case err != nil:
		// Unknown is not the same as unavailable. Falling back to strict on a
		// failed read would turn every open pull request red over a blip, so
		// the decision waits instead.
		return mergeQueueDecision{
			State:  mergeQueueDeferred,
			Reason: "could not read the repository to decide which mechanism it can have: " + firstLine(err.Error()),
			Remedy: "re-run, or pin the mechanism with branch_protection.base_freshness",
		}
	case impossible != "" && bp.BaseFreshness == repospec.FreshnessQueue:
		// Asked for by name. Substituting silently would leave somebody
		// believing they have a queue, so say so and change nothing.
		return mergeQueueDecision{
			State:  mergeQueueDeferred,
			Reason: "base_freshness is " + repospec.FreshnessQueue + ", but " + impossible,
			Remedy: "use base_freshness: " + repospec.FreshnessAuto + " to take the fallback, or " +
				repospec.FreshnessStrict + " to ask for it by name",
		}
	case impossible != "":
		return mergeQueueDecision{
			State:  mergeQueueStrict,
			Reason: impossible + ", so the guarantee is held by strict required checks instead",
		}
	}

	// The ordering gate, and the reason this function exists rather than a bare
	// boolean. A merge queue only works if the required check reports on
	// merge_group events. Add the rule to a repository whose orchestrator has
	// not been synced and every pull request enters the queue and sits there
	// forever, because ci-gate never runs — and the governance stage that would
	// have said "you are out of date" is itself inside the check that no longer
	// reports. Files first, settings second, enforced here rather than left to
	// whoever is doing the rollout.
	triggered, err := mergeGroupTriggered(ctx, gh, owner, name, bp.Branch)
	if err != nil {
		return mergeQueueDecision{
			State:  mergeQueueDeferred,
			Reason: "could not read " + orchestrationWorkflow + " on " + bp.Branch + ": " + firstLine(err.Error()),
			Remedy: "render it with 'gt repo sync' if the repository has none, " +
				"then re-run once it is readable on " + bp.Branch,
		}
	}
	if !triggered {
		return mergeQueueDecision{
			State: mergeQueueDeferred,
			Reason: orchestrationWorkflow + " on " + bp.Branch +
				" has no merge_group trigger, so ci-gate would never report for a queued pull request",
			Remedy: "run 'gt repo sync', then land the workflow change on " + bp.Branch +
				" before applying settings again",
		}
	}

	return mergeQueueDecision{State: mergeQueueOn}
}

// orchestrationWorkflow is the file whose merge_group trigger the queue depends
// on. Named here rather than rebuilt from parts, because the settings layer
// checks for it on GitHub while the render layer writes it locally, and the two
// must be talking about the same path.
const orchestrationWorkflow = WorkflowDir + "/ci-orchestration.yml"

// queueImpossible returns why GitHub will not give this repository a merge
// queue, or "" when it will.
//
// Read from ownership and visibility rather than from a feature probe, because
// no probe answers the question. GraphQL's `repository.mergeQueue(branch:)`
// resolves on a repository that then rejects the rule outright — it returns
// null both for "no queue configured" and for "no queue possible" — so
// believing it would have gt apply a rule GitHub refuses, on exactly the
// repositories that most need the guarantee.
//
// The rule below is measured, not documented. The same rule payload against
// four repositories:
//
//	user-owned, public                      422, "Invalid rule 'merge_queue': "
//	organization-owned, public, free plan    accepted
//	organization-owned, private, free plan   403, "Upgrade ... or make this repository public"
//	organization-owned, private, team plan   422, "Invalid rule 'merge_queue': "
//
// So: organization-owned and public. Private repositories are said to work on
// Enterprise Cloud, which there was nothing here to test against, so they are
// reported as impossible and take the strict fallback — a weaker experience
// carrying the identical guarantee. A repository that knows better says
// base_freshness: queue and gets the queue.
func queueImpossible(ctx context.Context, gh GH, owner, name string) (string, error) {
	raw, err := gh.Run(ctx, "api", fmt.Sprintf("repos/%s/%s", owner, name))
	if err != nil {
		return "", err
	}
	var live struct {
		Private bool `json:"private"`
		Owner   struct {
			Type string `json:"type"`
		} `json:"owner"`
	}
	if err := json.Unmarshal(raw, &live); err != nil {
		return "", fmt.Errorf("parse repository: %w", err)
	}
	if live.Owner.Type != "Organization" {
		return "GitHub offers merge queues only on organization-owned repositories, and " +
			owner + "/" + name + " belongs to a user account", nil
	}
	if live.Private {
		return "GitHub offers merge queues on a private repository only under Enterprise Cloud", nil
	}
	return "", nil
}

// mergeGroupTriggered reports whether the orchestration workflow *on the
// default branch* triggers on merge_group.
//
// The default branch, not the working tree, is the thing that matters: a
// locally synced file that has not landed yet is not what GitHub will run for a
// queued pull request. Checking the wrong one would let a repository pass this
// gate and still hang.
func mergeGroupTriggered(ctx context.Context, gh GH, owner, name, branch string) (bool, error) {
	raw, err := gh.Run(ctx, "api",
		fmt.Sprintf("repos/%s/%s/contents/%s?ref=%s", owner, name, orchestrationWorkflow, branch),
		"--header", "Accept: application/vnd.github.raw",
	)
	if err != nil {
		return false, err
	}
	return hasMergeGroupTrigger(raw)
}

// hasMergeGroupTrigger parses a workflow and reports whether merge_group is
// among its triggers.
//
// `on:` accepts three shapes — a mapping of events, a sequence of event names,
// and a bare scalar — and all three are handled rather than assumed, because
// getting this wrong in the permissive direction is what lets a pull request
// into a queue that can never build it.
func hasMergeGroupTrigger(workflow []byte) (bool, error) {
	const event = "merge_group"
	var parsed struct {
		// yaml.v3 reads `on` as the string key, not a YAML 1.1 boolean, so the
		// tag is enough.
		On yaml.Node `yaml:"on"`
	}
	if err := yaml.Unmarshal(workflow, &parsed); err != nil {
		return false, fmt.Errorf("parse %s: %w", orchestrationWorkflow, err)
	}
	switch parsed.On.Kind {
	case yaml.MappingNode:
		for i := 0; i < len(parsed.On.Content); i += 2 {
			if parsed.On.Content[i].Value == event {
				return true, nil
			}
		}
	case yaml.SequenceNode:
		for _, n := range parsed.On.Content {
			if n.Value == event {
				return true, nil
			}
		}
	case yaml.ScalarNode:
		return parsed.On.Value == event, nil
	}
	return false, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// desiredRuleset is the ruleset gt renders from the spec.
//
// Everything is in one object on purpose: the required check, the pull-request
// requirement, the merge methods, and the history guarantees. A repository
// either matches it or does not.
func desiredRuleset(spec repospec.Spec, mq mergeQueueState) map[string]any {
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
				// Never true alongside the merge_queue rule below: the two are
				// branches of one decision, not two switches.
				"strict_required_status_checks_policy": mq == mergeQueueStrict,
				"do_not_enforce_on_create":             false,
				"required_status_checks": []map[string]any{
					{"context": repospec.GateCheckJob},
				},
			},
		})
	}

	if mq == mergeQueueOn {
		rules = append(rules, map[string]any{
			"type":       "merge_queue",
			"parameters": queueParameters(spec),
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
		MergeMethod                  string `json:"merge_method"`
		GroupingStrategy             string `json:"grouping_strategy"`
		MinEntriesToMerge            int    `json:"min_entries_to_merge"`
		MaxEntriesToBuild            int    `json:"max_entries_to_build"`
		MaxEntriesToMerge            int    `json:"max_entries_to_merge"`
		MinEntriesToMergeWaitMinutes int    `json:"min_entries_to_merge_wait_minutes"`
		CheckResponseTimeoutMinutes  int    `json:"check_response_timeout_minutes"`
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
//
// merge_queue is the one member that is not fixed: gt owns it once it has
// settled the question — enforced or declined — and deliberately disowns it
// while the answer is "wanted, not yet possible". Disowned means carried
// through verbatim, so a deferral cannot tear down a queue that is already
// working; see mergeQueueDeferred.
func managedRuleTypes(_ repospec.Spec, mq mergeQueueState) map[string]bool {
	managed := make(map[string]bool, len(gtRuleTypes)+1)
	for t := range gtRuleTypes {
		managed[t] = true
	}
	// Managed under every settled mechanism, so falling back to strict removes
	// a queue rather than leaving both in place. Unmanaged only while the
	// decision is still open.
	if mq != mergeQueueDeferred {
		managed["merge_queue"] = true
	}
	return managed
}

// unmanagedRules returns the rules in `other` that gt does not render itself —
// code_quality and copilot_code_review being the ones in use today.
//
// They are not dropped and they do not force a second ruleset to survive:
// apply copies them verbatim into gt's own ruleset and then removes the old
// one, so the branch ends up governed by exactly one object with the same
// protections it had before.
func unmanagedRules(other liveRuleset, spec repospec.Spec, mq mergeQueueState) []liveRule {
	// See findRuleset: a disabled ruleset contributes nothing, because its rules
	// are not in force and copying them would enable them.
	if other.Enforcement != "active" {
		return nil
	}
	return unmanagedRulesOf(other, spec, mq)
}

// unmanagedRulesOf is unmanagedRules without the enforcement gate, for gt's
// OWN ruleset.
//
// That gate exists so absorbing a ruleset somebody switched off does not
// silently switch its rules back on. It must not apply to gt's own ruleset,
// which apply always re-activates: skipping it there meant a ruleset paused in
// the UI came back active minus every rule gt had previously absorbed into it,
// on a run whose only reported change was the enforcement flag.
func unmanagedRulesOf(other liveRuleset, spec repospec.Spec, mq mergeQueueState) []liveRule {
	managed := managedRuleTypes(spec, mq)
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
func rulesetChanges(spec repospec.Spec, live *liveRuleset, mq mergeQueueDecision) []SettingChange {
	bp := spec.Settings.BranchProtection
	want := desiredRuleset(spec, mq.State)

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
	// A deferred queue is not drift in either direction: gt is not adding the
	// rule and not removing one. Reporting it here would either nag for a rule
	// gt is deliberately withholding, or announce the removal of a live queue
	// that apply will in fact carry through. The deferral itself is reported
	// once, below.
	if mq.State == mergeQueueDeferred {
		delete(wantTypes, "merge_queue")
		delete(gotTypes, "merge_queue")
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
		case "merge_queue":
			// Only when gt owns the rule. Under a deferral the live parameters
			// are somebody else's, or gt's own from before the deferral, and
			// either way apply is about to write them back unchanged.
			if mq.State != mergeQueueOn {
				break
			}
			p := queueParameters(spec)
			add("ruleset.merge_queue.merge_method",
				fmt.Sprint(p["merge_method"]), r.Parameters.MergeMethod)
			add("ruleset.merge_queue.grouping_strategy",
				fmt.Sprint(p["grouping_strategy"]), r.Parameters.GroupingStrategy)
			add("ruleset.merge_queue.min_entries_to_merge",
				fmt.Sprint(p["min_entries_to_merge"]), fmt.Sprint(r.Parameters.MinEntriesToMerge))
			add("ruleset.merge_queue.min_entries_to_merge_wait_minutes",
				fmt.Sprint(p["min_entries_to_merge_wait_minutes"]),
				fmt.Sprint(r.Parameters.MinEntriesToMergeWaitMinutes))
			add("ruleset.merge_queue.max_entries_to_build",
				fmt.Sprint(p["max_entries_to_build"]), fmt.Sprint(r.Parameters.MaxEntriesToBuild))
			add("ruleset.merge_queue.max_entries_to_merge",
				fmt.Sprint(p["max_entries_to_merge"]), fmt.Sprint(r.Parameters.MaxEntriesToMerge))
			add("ruleset.merge_queue.check_response_timeout_minutes",
				fmt.Sprint(p["check_response_timeout_minutes"]),
				fmt.Sprint(r.Parameters.CheckResponseTimeoutMinutes))
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
			// Left alone during a deferral, for the same reason the queue
			// rule is: gt has not settled the mechanism yet, so it changes
			// neither.
			if mq.State != mergeQueueDeferred {
				add("ruleset.strict_required_status_checks_policy",
					fmt.Sprint(mq.State == mergeQueueStrict),
					fmt.Sprint(r.Parameters.StrictRequiredStatusChecksPolicy))
			}
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
	mq := resolveMergeQueue(ctx, gh, spec, owner, name)

	// Required by the queue, and only by the queue. Entering a queue goes
	// through the same machinery as auto-merge — `gh pr merge` on a queued
	// branch answers "Auto merge is not allowed for this repository" without
	// it — so a repository could hold a perfectly configured queue that nobody,
	// including gt's own Dependabot auto-merge job, can put anything into.
	//
	// Asserted only where the queue is the mechanism. gt has never managed this
	// toggle and turning it off elsewhere would silently break whatever a
	// repository is already using it for.
	if mq.State == mergeQueueOn && !live.AllowAutoMerge {
		changes = append(changes, SettingChange{
			Field: "allow_auto_merge", Want: "true (the merge queue is entered through it)", Got: "false",
		})
	}
	changes = append(changes, rulesetChanges(spec, mine, mq)...)

	// Reported whenever gt chose rather than being told, so nobody has to infer
	// which mechanism a repository ended up with — and every time, so a
	// repository stuck mid-rollout reads as incomplete rather than compliant.
	// Silence here is the failure this whole rule exists to prevent, one level
	// up: a repository that looks governed and is not.
	// Only a deferral is a change. An auto-chosen strict fallback is a settled,
	// correct outcome — reporting it every run would mean a user-owned
	// repository could never read as compliant, and the flip it causes is
	// already visible as strict_required_status_checks_policy above.
	if mq.State == mergeQueueDeferred {
		changes = append(changes, SettingChange{
			Field: MergeQueueChangeField,
			Want:  "deferred — " + mq.Remedy,
			Got:   mq.Reason,
		})
	}

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
		if extra := unmanagedRules(o, spec, mq.State); len(extra) > 0 {
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

	// Resolved before the repository PATCH, not after, because the mechanism
	// decides whether that PATCH must also enable auto-merge.
	mq := resolveMergeQueue(ctx, gh, spec, owner, name)

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
	// See SettingsDiff: the queue is entered through auto-merge, and only ever
	// turned on, never off.
	if mq.State == mergeQueueOn {
		repoArgs = append(repoArgs, "-F", "allow_auto_merge=true")
	}
	if _, err := gh.Run(ctx, repoArgs...); err != nil {
		return err
	}

	bp := spec.Settings.BranchProtection
	mine, others, err := findRuleset(ctx, gh, owner, name, bp.Branch)
	if err != nil {
		return err
	}

	payload := desiredRuleset(spec, mq.State)

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
		rules := unmanagedRules(src, spec, mq.State)
		if i == 0 && mine != nil {
			rules = unmanagedRulesOf(src, spec, mq.State)
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

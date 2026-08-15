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

// protection is the subset of the branch-protection API gt manages.
type protection struct {
	RequiredStatusChecks *struct {
		Strict   bool     `json:"strict"`
		Contexts []string `json:"contexts"`
	} `json:"required_status_checks"`
	RequiredPullRequestReviews *struct {
		RequiredApprovingReviewCount int `json:"required_approving_review_count"`
	} `json:"required_pull_request_reviews"`
}

// SettingsDiff reports what `SettingsApply` would change.
//
// Branch protection always requires exactly one check — gt's gate. The checks a
// repo actually cares about are declared in .gt-repo.yaml and enforced by the
// gate aggregating them, which is what keeps this list stable forever instead
// of needing an update every time a CI job is renamed.
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
	protRaw, err := gh.Run(ctx, "api", fmt.Sprintf("repos/%s/%s/branches/%s/protection", owner, name, bp.Branch))
	if err != nil {
		// Only a genuine 404 means "not protected", which is a state to
		// converge from. Any other failure — auth, rate limit, a mistyped
		// branch name — must surface: reporting it as "this branch has no
		// protection" would be actively misleading about a shared branch, and
		// returning early would silently skip the remaining diffs.
		if !isBranchNotProtected(err) {
			return nil, fmt.Errorf("read protection for branch %q: %w", bp.Branch, err)
		}
		changes = append(changes, SettingChange{
			Field: "branch_protection", Want: "configured", Got: "absent",
		})
		return changes, nil
	}
	var liveProt protection
	if err := json.Unmarshal(protRaw, &liveProt); err != nil {
		return nil, fmt.Errorf("parse branch protection: %w", err)
	}

	wantContexts := []string{repospec.GateCheckJob}
	gotContexts := []string{}
	gotStrict := false
	if liveProt.RequiredStatusChecks != nil {
		gotContexts = liveProt.RequiredStatusChecks.Contexts
		gotStrict = liveProt.RequiredStatusChecks.Strict
	}
	if !sameStrings(wantContexts, gotContexts) {
		changes = append(changes, SettingChange{
			Field: "required_status_checks.contexts",
			Want:  strings.Join(wantContexts, ", "),
			Got:   strings.Join(gotContexts, ", "),
		})
	}
	if gotStrict != bp.RequireUpToDate {
		changes = append(changes, SettingChange{
			Field: "required_status_checks.strict",
			Want:  fmt.Sprint(bp.RequireUpToDate), Got: fmt.Sprint(gotStrict),
		})
	}

	gotApprovals := 0
	if liveProt.RequiredPullRequestReviews != nil {
		gotApprovals = liveProt.RequiredPullRequestReviews.RequiredApprovingReviewCount
	}
	if gotApprovals != bp.RequiredApprovals {
		changes = append(changes, SettingChange{
			Field: "required_approving_review_count",
			Want:  fmt.Sprint(bp.RequiredApprovals), Got: fmt.Sprint(gotApprovals),
		})
	}

	return changes, nil
}

// SettingsApply pushes the desired merge settings and branch protection.
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
	// The protection endpoint replaces the whole object, so every field gt
	// manages must be sent on every call. Fields gt does not manage are set to
	// explicit nulls rather than omitted, because omitting them is what
	// silently clears them.
	payload := map[string]any{
		"required_status_checks": map[string]any{
			"strict":   bp.RequireUpToDate,
			"contexts": []string{repospec.GateCheckJob},
		},
		"enforce_admins": nil,
		"required_pull_request_reviews": map[string]any{
			"required_approving_review_count": bp.RequiredApprovals,
		},
		"restrictions": nil,
	}
	if bp.RequiredApprovals == 0 {
		// Zero approvals is expressed by dropping the review requirement
		// entirely; sending a count of 0 is rejected.
		payload["required_pull_request_reviews"] = nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode branch protection payload: %w", err)
	}

	_, err = gh.RunWithInput(ctx, body,
		"api", "--method", "PUT",
		fmt.Sprintf("repos/%s/%s/branches/%s/protection", owner, name, bp.Branch),
		"--input", "-",
		"--header", "Accept: application/vnd.github+json",
	)
	return err
}

// isBranchNotProtected distinguishes "this branch has no protection rule"
// from every other reason the protection endpoint can fail. gh surfaces the
// status line in its stderr, which is what ExecGH wraps into the error.
func isBranchNotProtected(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "http 404") || strings.Contains(msg, "branch not protected")
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

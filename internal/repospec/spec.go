// Package repospec parses and validates .gt-repo.yaml, the single committed
// file that declares a repository's governance: its Dependabot ecosystems, the
// checks the gate aggregates, conventional-commit enforcement, Dependabot
// auto-merge, and the GitHub settings gt applies.
//
// The file is the source of truth for everything gt renders. Shared policy
// (cooldown days, commit-message prefixes, schedules) deliberately lives in
// gt's embedded templates instead, so changing it is a one-line edit in gt
// rather than a change to every repo.
package repospec

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// FileName is the per-repo governance manifest, committed at the repo root.
const FileName = ".gt-repo.yaml"

// Spec is the parsed .gt-repo.yaml.
type Spec struct {
	// GTVersion records the gt release that last rendered this repo. Written by
	// sync; `check` warns when the running gt is newer.
	GTVersion string `yaml:"gt_version,omitempty" json:"gt_version,omitempty"`

	Dependabot          []DependabotEntry   `yaml:"dependabot" json:"dependabot"`
	DependabotAutoMerge DependabotAutoMerge `yaml:"dependabot_auto_merge" json:"dependabot_auto_merge"`
	Lydite              Lydite              `yaml:"lydite" json:"lydite"`
	Pipeline            Pipeline            `yaml:"pipeline" json:"pipeline"`
	ConventionalCommits ConventionalCommits `yaml:"conventional_commits" json:"conventional_commits"`
	Settings            Settings            `yaml:"settings" json:"settings"`
	Files               []string            `yaml:"files" json:"files"`
}

// DependabotEntry is one ecosystem/directory pair. Note carries the per-repo
// rationale that would otherwise live as a YAML comment; it is re-emitted as a
// comment above the rendered entry so this file stays the single source of
// truth.
type DependabotEntry struct {
	Ecosystem string `yaml:"ecosystem" json:"ecosystem"`
	Directory string `yaml:"directory" json:"directory"`
	Note      string `yaml:"note,omitempty" json:"note,omitempty"`
	// Groups collapses several dependencies into a single PR. It is per-entry
	// rather than shared policy because a group states which dependencies must
	// move in lockstep, and only the repository knows that.
	//
	// Lockstep dependencies left ungrouped are not a cosmetic problem. Auto-merge
	// lands one half of the pair alone, the default branch breaks, and the sibling
	// PR then fails that same broken check — so auto-merge will not land it
	// either. Recovering takes a human.
	Groups []DependabotGroup `yaml:"groups,omitempty" json:"groups,omitempty"`
	// Allow narrows which dependencies Dependabot will open PRs for.
	//
	// It exists because Dependabot's default is direct dependencies only, and
	// that default silently freezes any pin held indirectly. lydite's go-pin
	// module is the case: it has no .go files, so `go mod tidy` marks gosec and
	// govulncheck as `// indirect`, and without an allow rule neither is ever
	// bumped — in the security scanner, with nothing reporting it.
	Allow []DependabotAllow `yaml:"allow,omitempty" json:"allow,omitempty"`
}

// DependabotAllow is one allow rule. Either field may stand alone: a name with
// no type, a type with no name, or both together.
type DependabotAllow struct {
	DependencyName string `yaml:"dependency_name,omitempty" json:"dependency_name,omitempty"`
	// DependencyType is Dependabot's vocabulary: direct, indirect, all,
	// production, development.
	DependencyType string `yaml:"dependency_type,omitempty" json:"dependency_type,omitempty"`
}

// DependabotGroup is one named group inside an ecosystem entry. Note carries
// the rationale, re-emitted as a comment above the rendered group.
type DependabotGroup struct {
	Name     string   `yaml:"name" json:"name"`
	Patterns []string `yaml:"patterns" json:"patterns"`
	Note     string   `yaml:"note,omitempty" json:"note,omitempty"`
	// AppliesTo narrows a group to one kind of update. Dependabot's own default
	// is version-updates, which is the setting worth stating explicitly: it
	// keeps a security advisory on a grouped dependency arriving as its own
	// immediate PR rather than waiting to be batched with routine bumps.
	//
	// Empty renders nothing and leaves Dependabot on its default.
	AppliesTo string `yaml:"applies_to,omitempty" json:"applies_to,omitempty"`
}

// Dependabot allow-rule dependency types.
var DependencyTypes = []string{"direct", "indirect", "all", "production", "development"}

// Dependabot group scopes.
const (
	AppliesToVersion  = "version-updates"
	AppliesToSecurity = "security-updates"
)

type DependabotAutoMerge struct {
	Enabled      bool   `yaml:"enabled" json:"enabled"`
	Schedule     string `yaml:"schedule" json:"schedule"`
	MaxBump      string `yaml:"max_bump" json:"max_bump"`
	DeleteBranch bool   `yaml:"delete_branch" json:"delete_branch"`
	// GitHubApp mints the merge token from a GitHub App instead of using
	// GITHUB_TOKEN, reading APP_ID and APP_PRIVATE_KEY.
	//
	// It exists for one reason: GITHUB_TOKEN is a GitHub App installation token
	// without the `workflows` permission, and that permission is not grantable
	// — there is no `workflows:` key for a workflow's permissions block. So
	// merging any PR that touches .github/workflows/** is refused outright,
	// which is every github-actions bump Dependabot ever opens. Those pile up
	// forever without this.
	//
	// Off by default: it needs an App installed on the org, so it cannot be
	// assumed. Where it is unset, the job keeps working on GITHUB_TOKEN and
	// keeps reporting workflow-touching PRs as needing a human.
	GitHubApp bool `yaml:"github_app" json:"github_app"`
}

// Lydite is the shared code-quality and security gate. Every governed repo
// carries it — that is the convention — and it is disabled only where a repo
// already wires lydite into its own pipeline with coverage plumbing gt cannot
// generically reproduce.
type Lydite struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Dir scopes the scan to a subdirectory, forwarded as reusable-lydite.yml's
	// `dir` input — lydite reads its own config from that root, so gt has to
	// know it before lydite can find `.lydite/` there at all.
	Dir string `yaml:"dir,omitempty" json:"dir,omitempty"`
	// Coverage runs lydite's coverage gate. Default true.
	//
	// Off for a repository with nothing lydite can measure — boma is shell and
	// container-driven pytest, wardnet-infrastructure is YAML. Leaving it on
	// there is not harmless: the gate spends time resolving a baseline for
	// languages that do not exist, and every run reports a coverage result that
	// means nothing, which is the kind of number people start ignoring.
	Coverage bool `yaml:"coverage" json:"coverage"`
	// Relay is the comment/review relay identity forwarded as reusable-lydite.yml's
	// and reusable-lydite-clearance.yml's `relay` input. Empty posts PR comments
	// and clearance replies as `github-actions[bot]`.
	Relay string `yaml:"relay,omitempty" json:"relay,omitempty"`
	// ExemptDependencyBumps renders a `dependency-version-bump` entry into
	// .lydite/exemptions.yml, derived from the dependabot: block. Default true.
	//
	// Without an exemption lydite refers every change, so every Dependabot pull
	// request waits on `lydite/referral` until a human clears it, and the
	// nightly auto-merge lands none of them. The entry is conditional — lydite
	// passes a bump only when it stays within patch or minor and its licence
	// and SCA rows are green — so false is for a repository that wants every
	// bump read regardless.
	ExemptDependencyBumps bool `yaml:"exempt_dependency_bumps" json:"exempt_dependency_bumps"`
	// Exemptions are repository-specific entries rendered into
	// .lydite/exemptions.yml after the derived one. gt owns that file, so this
	// list is where a repository records anything else it has decided lydite
	// need not refer.
	Exemptions []LyditeExemption `yaml:"exemptions,omitempty" json:"exemptions,omitempty"`
}

// LyditeExemption is one entry of lydite's exemptions file, in lydite's own
// shape: a change merges unread only when a single entry covers every path it
// touched and that entry's condition holds.
type LyditeExemption struct {
	Name string `yaml:"name" json:"name"`
	// Reason is required by lydite: each entry is a claim about what is safe
	// to stop reading, and a list of bare globs is not reviewable.
	Reason string `yaml:"reason" json:"reason"`
	// Paths are lydite's anchored patterns, relative to the repository root
	// even when lydite.dir scopes the scan to a subdirectory. "*" stays within
	// one segment and "**" spans any number of them.
	Paths []string `yaml:"paths" json:"paths"`
	// Versions conditions the match; LyditeVersionsPatchAndMinor is the only
	// value lydite accepts, and empty is a match on paths alone.
	Versions string `yaml:"versions,omitempty" json:"versions,omitempty"`
}

// LyditeVersionsPatchAndMinor is the one condition lydite understands: every
// version the change moves stays within patch or minor, and the licence and
// SCA rows for every component ran and passed.
const LyditeVersionsPatchAndMinor = "patch-and-minor"

// DependencyBumpExemption names the entry gt derives from the dependabot:
// block. While lydite.exempt_dependency_bumps is on, no entry in
// lydite.exemptions may take the name: lydite rejects a file carrying it twice.
const DependencyBumpExemption = "dependency-version-bump"

// lyditeReasonPlaceholder is the literal lydite reserves for the reason of an
// entry it proposes and refuses in any exemption it loads. Catching it here
// keeps the failure at `gt repo check` rather than in every lydite run.
const lyditeReasonPlaceholder = "TODO(lydite):"

// Pipeline declares the CI and CD orchestration gt renders. Each listed stage
// becomes a job in the orchestrator calling a scaffolded workflow the
// repository owns.
//
// `uses:` cannot be computed at runtime, so the stage list is baked into the
// rendered orchestrator: changing it is a re-render, and therefore a
// workflow-file change. Rare by design.
type Pipeline struct {
	CI PipelineCI `yaml:"ci" json:"ci"`
	CD PipelineCD `yaml:"cd" json:"cd"`
}

type PipelineCI struct {
	Enabled bool     `yaml:"enabled" json:"enabled"`
	Stages  []string `yaml:"stages" json:"stages"`
	// StagePermissions grants a named stage scopes beyond the CI baseline.
	// See StagePermissions for why this exists and what it cannot do.
	StagePermissions StagePermissions `yaml:"stage_permissions,omitempty" json:"stage_permissions,omitempty"`
	// IndependentStages names stages that consume no sibling stage's output.
	// See the type for what it can and cannot express.
	IndependentStages []string `yaml:"independent_stages,omitempty" json:"independent_stages,omitempty"`
}

type PipelineCD struct {
	Enabled bool     `yaml:"enabled" json:"enabled"`
	Stages  []string `yaml:"stages" json:"stages"`
	// Tags are the push patterns that trigger delivery. Repos ship on
	// different ones — a single component on v*.*.*, or several independently
	// versioned components on their own prefixes.
	Tags []string `yaml:"tags" json:"tags"`
	// StagePermissions grants a named stage scopes beyond the CD baseline.
	StagePermissions StagePermissions `yaml:"stage_permissions,omitempty" json:"stage_permissions,omitempty"`
	// IndependentStages names stages that consume no sibling stage's output.
	IndependentStages []string `yaml:"independent_stages,omitempty" json:"independent_stages,omitempty"`
}

// StagePermissions maps a stage name to the scopes its orchestrator job is
// granted on top of the pipeline's baseline, as `scope: level`.
//
// This exists because a called workflow can only ever NARROW what its caller
// grants. A leaf that genuinely needs more — uploading SARIF to code scanning,
// pushing a build cache to a registry — cannot ask for it back, so without a
// way to widen the calling job the only options were to lose the capability
// silently or keep the repository off the pipeline entirely.
//
// It is deliberately additive and deliberately per-stage. The baseline stays
// the answer for almost every stage, a widened stage names the one scope it
// needs, and the rendered orchestrator shows exactly which stage holds what —
// so the grant is reviewable in the diff rather than implied by a template.
type StagePermissions map[string]map[string]string

// IndependentStages, in a pipeline spec, is the set of stages that consume no
// sibling stage's output and so need not wait for one.
//
// This is a statement about the repository, not a reordering knob. The stage
// graph stays fixed: nothing here can add a dependency, invent an edge, or
// change the order stages render in. The only thing it can do is remove a
// dependency the repository does not actually have, dropping that stage back
// to depending on preflight alone.
//
// The dependency on preflight is never removed, because preflight produces no
// artefact — it is the gate deciding whether a stage runs at all, and the
// rendered `if:` reads its outputs.
//
// wardnet is the case this was added for. Its end2end suite rebuilds the
// daemon from source inside Docker, so it consumes nothing the build stage
// produces, and under its previous hand-rolled pipeline the two ran
// concurrently. Making it wait for build added about eleven minutes to every
// daemon pull request while proving nothing — a real cost paid for an edge
// that does not exist.

type ConventionalCommits struct {
	Enabled bool     `yaml:"enabled" json:"enabled"`
	Scope   string   `yaml:"scope" json:"scope"`
	Types   []string `yaml:"types" json:"types"`
	Scopes  []string `yaml:"scopes,omitempty" json:"scopes,omitempty"`
}

type Settings struct {
	Merge            MergeSettings    `yaml:"merge" json:"merge"`
	BranchProtection BranchProtection `yaml:"branch_protection" json:"branch_protection"`
}

type MergeSettings struct {
	Squash              bool `yaml:"squash" json:"squash"`
	MergeCommit         bool `yaml:"merge_commit" json:"merge_commit"`
	Rebase              bool `yaml:"rebase" json:"rebase"`
	DeleteBranchOnMerge bool `yaml:"delete_branch_on_merge" json:"delete_branch_on_merge"`
	// SquashTitle decides what the squashed commit's subject line is.
	//
	// Default pr_title, and this is load-bearing rather than cosmetic. GitHub's
	// own default, commit_or_pr_title, uses the COMMIT subject when a PR has
	// exactly one commit — so a repository can enforce Conventional Commits on
	// PR titles and still land a non-conforming subject on the default branch,
	// which is the one place the convention was for.
	SquashTitle string `yaml:"squash_title" json:"squash_title"`
	// SquashMessage decides the squashed commit's body. Default blank: the
	// alternative, commit_messages, concatenates every WIP subject from the
	// branch into the permanent history of the default branch.
	SquashMessage string `yaml:"squash_message" json:"squash_message"`
}

// BranchProtection is the pull-request and history policy gt renders into its
// ruleset. Every field has an opinionated default, so a repository states only
// where it disagrees — and an explicit `false` still overrides, because Parse
// unmarshals over Default() rather than over a zero value.
type BranchProtection struct {
	Branch            string `yaml:"branch" json:"branch"`
	RequiredApprovals int    `yaml:"required_approvals" json:"required_approvals"`
	// BaseFreshness and RequireUpToDate are retired keys. Neither is
	// configurable: wherever a required check exists, gt requires a pull
	// request to be up to date with its base before it merges — see
	// settings.go's desiredRuleset.
	//
	// They stay tagged fields because Parse decodes with KnownFields, so
	// deleting them would turn every manifest still carrying either spelling
	// into a parse error. Parse clears both before validation and nothing
	// downstream reads them; a manifest that still holds one does not
	// round-trip, so `gt repo check` reports it as spec drift and `gt repo
	// sync` drops the key.
	BaseFreshness   string `yaml:"base_freshness,omitempty" json:"-"`
	RequireUpToDate *bool  `yaml:"require_up_to_date,omitempty" json:"-"`
	// DismissStaleReviews drops approvals when new commits land. Default true:
	// an approval is of a diff, and a review of code that has since changed is
	// a rubber stamp wearing a reviewer's name.
	DismissStaleReviews bool `yaml:"dismiss_stale_reviews" json:"dismiss_stale_reviews"`
	// RequireThreadResolution blocks merge on unresolved review threads.
	// Default true: an unresolved thread is a question nobody answered, and
	// merging over it silently decides the answer is "no".
	RequireThreadResolution bool `yaml:"require_thread_resolution" json:"require_thread_resolution"`
	// RequireCodeOwnerReview needs a CODEOWNERS file to mean anything, and gt
	// does not require repositories to have one. Default false.
	RequireCodeOwnerReview bool `yaml:"require_code_owner_review" json:"require_code_owner_review"`
	// RequireLastPushApproval demands someone other than the last pusher
	// approve. Default false: with required_approvals at 0 it has nothing to
	// act on, and turning it on without approvals would block every PR.
	RequireLastPushApproval bool `yaml:"require_last_push_approval" json:"require_last_push_approval"`
}

// dropRetiredKeys clears every retired key a manifest may carry, so the
// resolved spec holds none of them and re-serialising it omits them.
func (b *BranchProtection) dropRetiredKeys() {
	b.BaseFreshness = ""
	b.RequireUpToDate = nil
}

// Squash commit title sources. The GitHub values these map to are an
// implementation detail of settings.go.
const (
	SquashTitlePR       = "pr_title"
	SquashTitleCommitPR = "commit_or_pr_title"
)

// Squash commit body sources.
const (
	SquashMessageBlank   = "blank"
	SquashMessagePRBody  = "pr_body"
	SquashMessageCommits = "commit_messages"
)

// Conventional-commit enforcement scopes.
const (
	ScopePRTitle = "pr_title"
	ScopeCommits = "commits"
	ScopeBoth    = "both"
)

// Dependabot bump levels, ordered least to most disruptive.
const (
	BumpPatch = "patch"
	BumpMinor = "minor"
	BumpMajor = "major"
)

// CIStages and CDStages are the stage vocabularies, in the order the
// orchestrators wire them.
var (
	CIStages = []string{StagePreflight, "build", "test", "end2end"}
	CDStages = []string{StagePreflight, "publish", "deploy", "verify"}
)

// StagePreflight is the gate every other stage reads its run-<stage> output
// from. It is the one stage that never depends on a sibling, and the one that
// can never be declared independent.
const StagePreflight = "preflight"

// PermissionScopes is the GITHUB_TOKEN scope vocabulary, and PermissionLevels
// the values each may take.
//
// Enumerated rather than passed through so a typo is a spec error at sync time
// instead of a scope GitHub silently ignores — which would render a job that
// looks widened but is not, and fail only later at the API call it was meant
// to permit.
var (
	PermissionScopes = []string{
		"actions", "attestations", "checks", "contents", "deployments",
		"discussions", "id-token", "issues", "models", "packages", "pages",
		"pull-requests", "repository-projects", "security-events", "statuses",
	}
	PermissionLevels = []string{"read", "write", "none"}
)

// GateCheckJob is the aggregating job, and so the single check branch
// protection requires. It is a plain job in a repo-owned workflow, so its
// check name carries no "<caller> / " prefix.
const GateCheckJob = "ci-gate"

// LyditeReferralContext is the commit-status context lydite's referral step
// publishes its verdict under, and the second check branch protection requires
// wherever lydite is enabled. A "refer" verdict never fails the job, so this
// status is the only thing that holds a referred pull request; a `/lydite
// clear` comment flips it without re-running anything.
//
// It mirrors internal/clearance.Context in the separate lydite/lydite module,
// which nothing here can import. The two are not kept in sync mechanically, so
// a rename on either side needs a matching change on the other.
//
// Required unscoped by integration_id: anything with write access to the repo
// can post a status under this context and satisfy the check without going
// through /lydite clear. Not fixable by adding one yet, either — as of this
// writing lydite/lydite publishes this status and handles /lydite clear with
// the plain per-run GITHUB_TOKEN, not any GitHub App identity, so there is no
// App to scope the check to. Tracked as pedromvgomes/gt#71, blocked on
// lydite/lydite (or lydite/actions) authenticating that path as an App first.
const LyditeReferralContext = "lydite/referral"

// Ecosystems gt can render a Dependabot entry for. Keys match Dependabot's
// package-ecosystem values.
var Ecosystems = []string{
	"cargo",
	"docker",
	"github-actions",
	"gomod",
	"npm",
	"pip",
	"terraform",
}

// Renderable file keys accepted in `files:`.
var FileKeys = []string{
	"sync",
	"dependabot-auto-merge",
	"codeowners",
	"editorconfig",
	"pr-template",
}

// Default returns the spec applied before unmarshalling, so an omitted key
// keeps a sensible value rather than a zero one.
func Default() Spec {
	return Spec{
		DependabotAutoMerge: DependabotAutoMerge{
			Enabled:      true,
			Schedule:     "0 1 * * *",
			MaxBump:      BumpMinor,
			DeleteBranch: true,
		},
		ConventionalCommits: ConventionalCommits{
			Enabled: true,
			Scope:   ScopePRTitle,
			// The full Conventional Commits 1.0.0 set. `build` and `ci` are
			// not optional here: Dependabot is configured to prefix its PR
			// titles with them, so dropping either would make every dependency
			// PR fail the title check it is subject to.
			Types: []string{
				"feat", "fix", "docs", "style", "refactor",
				"perf", "test", "build", "ci", "chore", "revert",
			},
		},
		Settings: Settings{
			Merge: MergeSettings{
				Squash:              true,
				MergeCommit:         false,
				Rebase:              false,
				DeleteBranchOnMerge: true,
				SquashTitle:         SquashTitlePR,
				SquashMessage:       SquashMessageBlank,
			},
			BranchProtection: BranchProtection{
				Branch: "main",
				// The two opinions worth holding: an approval is of a diff, and
				// an unresolved thread is an unanswered question.
				DismissStaleReviews:     true,
				RequireThreadResolution: true,
				// Off, because both need something gt does not require: a
				// CODEOWNERS file, and a nonzero approval count.
				RequireCodeOwnerReview:  false,
				RequireLastPushApproval: false,
			},
		},
		Lydite: Lydite{Enabled: true, Coverage: true, ExemptDependencyBumps: true},
		Pipeline: Pipeline{
			CI: PipelineCI{Enabled: true, Stages: append([]string(nil), CIStages...)},
			CD: PipelineCD{
				Enabled: true,
				Stages:  append([]string(nil), CDStages...),
				Tags:    []string{"v*.*.*"},
			},
		},
		Files: []string{"sync", "dependabot-auto-merge"},
	}
}

// Path returns the manifest path for a repository working tree.
func Path(workdir string) string {
	return filepath.Join(workdir, FileName)
}

// Load reads and validates the manifest from a repository working tree.
func Load(workdir string) (Spec, error) {
	return Read(Path(workdir))
}

// Exists reports whether a repository has opted into governance at all.
// Commands that may legitimately run against an ungoverned repository — the
// post-clone setup template, for one — check this instead of treating a
// missing manifest as a failure.
func Exists(workdir string) bool {
	_, err := os.Stat(Path(workdir))
	return err == nil
}

// Read reads and validates a manifest at an explicit path.
func Read(path string) (Spec, error) {
	// #nosec G304 -- reads .gt-repo.yaml from the repository root gt was pointed at.
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Spec{}, fmt.Errorf("%s not found; run 'gt repo init' to create it", path)
		}
		return Spec{}, fmt.Errorf("read %s: %w", path, err)
	}
	return Parse(data, path)
}

// Parse unmarshals and validates manifest bytes. path is used only for error
// messages.
//
// Decoding is strict, recursively: a key no struct in the tree claims is an
// error naming the field, not a line quietly dropped. A misspelled or retired
// key that parses is a manifest whose author believes it is in force, and the
// spec it produces is the default one.
func Parse(data []byte, path string) (Spec, error) {
	spec := Default()
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	// An empty document decodes to io.EOF, and leaves the defaults standing.
	if err := dec.Decode(&spec); err != nil && !errors.Is(err, io.EOF) {
		return Spec{}, fmt.Errorf("parse %s: %w", path, err)
	}
	spec.Settings.BranchProtection.dropRetiredKeys()
	if err := Validate(spec); err != nil {
		return Spec{}, fmt.Errorf("%s: %w", path, err)
	}
	return spec, nil
}

// Validate reports the first problem that would make a spec unrenderable.
// Errors name the offending field and the accepted values, matching the style
// of config.Validate.
func Validate(s Spec) error {
	if err := validateDependabot(s.Dependabot); err != nil {
		return err
	}
	if err := validateAutoMerge(s.DependabotAutoMerge); err != nil {
		return err
	}
	if err := validateConventionalCommits(s.ConventionalCommits); err != nil {
		return err
	}
	if err := validateSettings(s.Settings); err != nil {
		return err
	}
	if err := validateLydite(s.Lydite); err != nil {
		return err
	}
	if err := validatePipeline(s.Pipeline); err != nil {
		return err
	}
	return validateFiles(s.Files)
}

func validateDependabot(entries []DependabotEntry) error {
	seen := map[string]bool{}
	for i, e := range entries {
		if strings.TrimSpace(e.Ecosystem) == "" {
			return fmt.Errorf("dependabot[%d]: ecosystem is required", i)
		}
		if !contains(Ecosystems, e.Ecosystem) {
			return fmt.Errorf("dependabot[%d]: unknown ecosystem %q (accepted: %s)",
				i, e.Ecosystem, strings.Join(Ecosystems, ", "))
		}
		if strings.TrimSpace(e.Directory) == "" {
			return fmt.Errorf("dependabot[%d]: directory is required (use \"/\" for the repo root)", i)
		}
		if !strings.HasPrefix(e.Directory, "/") {
			return fmt.Errorf("dependabot[%d]: directory %q must start with \"/\"", i, e.Directory)
		}
		key := e.Ecosystem + "\x00" + e.Directory
		if seen[key] {
			return fmt.Errorf("dependabot[%d]: duplicate entry for %s at %s", i, e.Ecosystem, e.Directory)
		}
		seen[key] = true
		if err := validateGroups(i, e.Groups); err != nil {
			return err
		}
		if err := validateAllow(i, e.Allow); err != nil {
			return err
		}
	}
	return nil
}

func validateAllow(entry int, rules []DependabotAllow) error {
	for j, r := range rules {
		name := strings.TrimSpace(r.DependencyName)
		typ := strings.TrimSpace(r.DependencyType)
		// A rule with neither field matches nothing, which reads as "allow
		// nothing" and would stop every update for the ecosystem. Refuse it
		// rather than render a config that silently switches updates off.
		if name == "" && typ == "" {
			return fmt.Errorf("dependabot[%d].allow[%d]: needs dependency_name, dependency_type, or both", entry, j)
		}
		if typ != "" && !contains(DependencyTypes, typ) {
			return fmt.Errorf("dependabot[%d].allow[%d]: dependency_type %q is not one of %s",
				entry, j, typ, strings.Join(DependencyTypes, ", "))
		}
	}
	return nil
}

func validateGroups(entry int, groups []DependabotGroup) error {
	seen := map[string]bool{}
	for j, g := range groups {
		name := strings.TrimSpace(g.Name)
		if name == "" {
			return fmt.Errorf("dependabot[%d].groups[%d]: name is required", entry, j)
		}
		if seen[name] {
			return fmt.Errorf("dependabot[%d].groups[%d]: duplicate group name %q", entry, j, name)
		}
		seen[name] = true
		// A group with no patterns is not inert — Dependabot would match every
		// dependency in the ecosystem into it, collapsing all updates into one
		// PR. Silently doing that on a typo is worse than refusing.
		if len(g.Patterns) == 0 {
			return fmt.Errorf("dependabot[%d].groups[%d] (%s): at least one pattern is required", entry, j, name)
		}
		for k, p := range g.Patterns {
			if strings.TrimSpace(p) == "" {
				return fmt.Errorf("dependabot[%d].groups[%d] (%s): pattern[%d] is empty", entry, j, name, k)
			}
		}
		switch g.AppliesTo {
		case "", AppliesToVersion, AppliesToSecurity:
		default:
			return fmt.Errorf("dependabot[%d].groups[%d] (%s): applies_to %q is not one of %s, %s",
				entry, j, name, g.AppliesTo, AppliesToVersion, AppliesToSecurity)
		}
	}
	return nil
}

func validateAutoMerge(a DependabotAutoMerge) error {
	if !a.Enabled {
		return nil
	}
	if !contains([]string{BumpPatch, BumpMinor, BumpMajor}, a.MaxBump) {
		return fmt.Errorf("dependabot_auto_merge.max_bump must be one of patch, minor, major (got %q)", a.MaxBump)
	}
	if err := validateCron(a.Schedule); err != nil {
		return fmt.Errorf("dependabot_auto_merge.schedule: %w", err)
	}
	return nil
}

func validateConventionalCommits(c ConventionalCommits) error {
	if !c.Enabled {
		return nil
	}
	if !contains([]string{ScopePRTitle, ScopeCommits, ScopeBoth}, c.Scope) {
		return fmt.Errorf("conventional_commits.scope must be one of pr_title, commits, both (got %q)", c.Scope)
	}
	if len(c.Types) == 0 {
		return fmt.Errorf("conventional_commits.types cannot be empty when enabled")
	}
	for i, t := range c.Types {
		if strings.TrimSpace(t) == "" {
			return fmt.Errorf("conventional_commits.types[%d]: type cannot be empty", i)
		}
	}
	for i, s := range c.Scopes {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("conventional_commits.scopes[%d]: scope cannot be empty", i)
		}
	}
	return nil
}

func validatePipeline(p Pipeline) error {
	if err := validateStages("pipeline.ci.stages", p.CI.Enabled, p.CI.Stages, CIStages); err != nil {
		return err
	}
	if err := validateStages("pipeline.cd.stages", p.CD.Enabled, p.CD.Stages, CDStages); err != nil {
		return err
	}
	if err := validateStagePermissions(
		"pipeline.ci.stage_permissions", p.CI.Enabled, p.CI.StagePermissions, p.CI.Stages,
	); err != nil {
		return err
	}
	if err := validateStagePermissions(
		"pipeline.cd.stage_permissions", p.CD.Enabled, p.CD.StagePermissions, p.CD.Stages,
	); err != nil {
		return err
	}
	if err := validateIndependentStages(
		"pipeline.ci.independent_stages", p.CI.Enabled, p.CI.IndependentStages, p.CI.Stages,
	); err != nil {
		return err
	}
	if err := validateIndependentStages(
		"pipeline.cd.independent_stages", p.CD.Enabled, p.CD.IndependentStages, p.CD.Stages,
	); err != nil {
		return err
	}
	if p.CD.Enabled && len(p.CD.Tags) == 0 {
		return fmt.Errorf("pipeline.cd.tags cannot be empty when CD is enabled; nothing would ever trigger it")
	}
	return nil
}

func validateStages(field string, enabled bool, got, known []string) error {
	if !enabled {
		return nil
	}
	if len(got) == 0 {
		return fmt.Errorf("%s cannot be empty when the pipeline is enabled", field)
	}
	seen := map[string]bool{}
	for i, st := range got {
		if !contains(known, st) {
			return fmt.Errorf("%s[%d]: unknown stage %q (accepted: %s)", field, i, st, strings.Join(known, ", "))
		}
		if seen[st] {
			return fmt.Errorf("%s[%d]: duplicate stage %q", field, i, st)
		}
		seen[st] = true
	}
	return nil
}

// validateStagePermissions rejects a grant that could not take effect: one
// naming a stage the pipeline does not run, or a scope or level GitHub does
// not define. Each of those would otherwise render a job that reads as
// widened but is not.
func validateStagePermissions(
	field string, enabled bool, perms StagePermissions, stages []string,
) error {
	if len(perms) == 0 {
		return nil
	}
	if !enabled {
		return fmt.Errorf("%s: cannot grant permissions while the pipeline is disabled", field)
	}
	// Sorted so the same spec always reports the same error first.
	for _, stage := range sortedKeys(perms) {
		if !contains(stages, stage) {
			return fmt.Errorf(
				"%s: stage %q is not enabled (enabled: %s)",
				field, stage, strings.Join(stages, ", "))
		}
		scopes := perms[stage]
		if len(scopes) == 0 {
			return fmt.Errorf("%s.%s: no permissions listed; drop the entry instead", field, stage)
		}
		for _, scope := range sortedKeys(scopes) {
			if !contains(PermissionScopes, scope) {
				return fmt.Errorf(
					"%s.%s: unknown permission scope %q (accepted: %s)",
					field, stage, scope, strings.Join(PermissionScopes, ", "))
			}
			if level := scopes[scope]; !contains(PermissionLevels, level) {
				return fmt.Errorf(
					"%s.%s.%s: invalid level %q (accepted: %s)",
					field, stage, scope, level, strings.Join(PermissionLevels, ", "))
			}
		}
	}
	return nil
}

// validateIndependentStages rejects a declaration that cannot mean anything:
// one naming a stage the pipeline does not run, a duplicate, or `preflight`,
// which has no sibling dependency to drop in the first place.
func validateIndependentStages(field string, enabled bool, got, stages []string) error {
	if len(got) == 0 {
		return nil
	}
	if !enabled {
		return fmt.Errorf("%s: cannot name stages while the pipeline is disabled", field)
	}
	seen := map[string]bool{}
	for i, stage := range got {
		if !contains(stages, stage) {
			return fmt.Errorf(
				"%s[%d]: stage %q is not enabled (enabled: %s)",
				field, i, stage, strings.Join(stages, ", "))
		}
		if seen[stage] {
			return fmt.Errorf("%s[%d]: duplicate stage %q", field, i, stage)
		}
		if stage == StagePreflight {
			return fmt.Errorf(
				"%s[%d]: %q has no stage dependency to drop; remove it",
				field, i, stage)
		}
		seen[stage] = true
	}
	return nil
}

// validateLydite rejects an exemption lydite itself would refuse to load.
// lydite parses the file on every run, so an entry that passed here and failed
// there would break the lydite job on every pull request rather than once, at
// sync time.
func validateLydite(l Lydite) error {
	if len(l.Exemptions) == 0 {
		return nil
	}
	if !l.Enabled {
		return fmt.Errorf("lydite.exemptions: cannot declare exemptions while lydite is disabled")
	}
	seen := map[string]bool{}
	for i, e := range l.Exemptions {
		where := fmt.Sprintf("lydite.exemptions[%d]", i)
		name := strings.TrimSpace(e.Name)
		if name == "" {
			return fmt.Errorf("%s: name is required", where)
		}
		if name != e.Name {
			return fmt.Errorf("%s: name %q has leading or trailing whitespace", where, e.Name)
		}
		if l.ExemptDependencyBumps && name == DependencyBumpExemption {
			return fmt.Errorf("%s: name %q is the entry gt derives from dependabot:; "+
				"set lydite.exempt_dependency_bumps: false to write your own", where, name)
		}
		if seen[name] {
			return fmt.Errorf("%s: duplicate name %q", where, name)
		}
		seen[name] = true
		if strings.TrimSpace(e.Reason) == "" {
			return fmt.Errorf("%s (%s): reason is required — it is what makes the entry reviewable", where, name)
		}
		if strings.Contains(e.Reason, lyditeReasonPlaceholder) {
			return fmt.Errorf("%s (%s): reason still carries lydite's placeholder %q", where, name, lyditeReasonPlaceholder)
		}
		// An entry with no paths covers only a change that touches nothing,
		// so it can never fire; lydite refuses it.
		if len(e.Paths) == 0 {
			return fmt.Errorf("%s (%s): at least one path pattern is required", where, name)
		}
		for j, p := range e.Paths {
			if err := validateExemptionPattern(p); err != nil {
				return fmt.Errorf("%s (%s).paths[%d]: %w", where, name, j, err)
			}
		}
		if e.Versions != "" && e.Versions != LyditeVersionsPatchAndMinor {
			return fmt.Errorf("%s (%s): versions %q is not a condition lydite knows (accepted: %s)",
				where, name, e.Versions, LyditeVersionsPatchAndMinor)
		}
	}
	return nil
}

// validateExemptionPattern accepts what lydite's pattern syntax can match.
// Patterns are anchored and compared against repository-root-relative paths
// that never start with "/", so a leading slash or an empty segment yields a
// pattern that silently matches nothing.
func validateExemptionPattern(p string) error {
	if strings.TrimSpace(p) == "" {
		return fmt.Errorf("pattern is empty")
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("pattern %q must be relative to the repository root, without a leading \"/\"", p)
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == "" {
			return fmt.Errorf("pattern %q has an empty path segment", p)
		}
		if segment == "**" {
			continue
		}
		if _, err := path.Match(segment, ""); err != nil {
			return fmt.Errorf("bad path pattern %q: %w", p, err)
		}
	}
	return nil
}

func validateSettings(s Settings) error {
	m := s.Merge
	if !m.Squash && !m.MergeCommit && !m.Rebase {
		return fmt.Errorf("settings.merge: at least one merge method must be enabled")
	}
	if strings.TrimSpace(s.BranchProtection.Branch) == "" {
		return fmt.Errorf("settings.branch_protection.branch is required")
	}
	if s.BranchProtection.RequiredApprovals < 0 {
		return fmt.Errorf("settings.branch_protection.required_approvals cannot be negative")
	}
	switch m.SquashTitle {
	case SquashTitlePR, SquashTitleCommitPR:
	default:
		return fmt.Errorf("settings.merge.squash_title %q is not one of %s, %s",
			m.SquashTitle, SquashTitlePR, SquashTitleCommitPR)
	}
	switch m.SquashMessage {
	case SquashMessageBlank, SquashMessagePRBody, SquashMessageCommits:
	default:
		return fmt.Errorf("settings.merge.squash_message %q is not one of %s, %s, %s",
			m.SquashMessage, SquashMessageBlank, SquashMessagePRBody, SquashMessageCommits)
	}
	return nil
}

func validateFiles(files []string) error {
	seen := map[string]bool{}
	for i, f := range files {
		if !contains(FileKeys, f) {
			return fmt.Errorf("files[%d]: unknown file %q (accepted: %s)", i, f, strings.Join(FileKeys, ", "))
		}
		if seen[f] {
			return fmt.Errorf("files[%d]: duplicate file %q", i, f)
		}
		seen[f] = true
	}
	return nil
}

// validateCron does a shape check on a 5-field cron expression. GitHub rejects
// malformed schedules by silently never running the workflow, so catching it
// here is worth more than the precision a full parser would add.
func validateCron(expr string) error {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return fmt.Errorf("expected 5 space-separated fields, got %d (%q)", len(fields), expr)
	}
	return nil
}

// WantsFile reports whether the spec asks gt to render the given file key.
func (s Spec) WantsFile(key string) bool {
	return contains(s.Files, key)
}

// EnforcesCommits reports whether individual commit subjects must be linted,
// as opposed to the PR title alone.
func (c ConventionalCommits) EnforcesCommits() bool {
	return c.Enabled && (c.Scope == ScopeCommits || c.Scope == ScopeBoth)
}

// EnforcesPRTitle reports whether the PR title must be linted.
func (c ConventionalCommits) EnforcesPRTitle() bool {
	return c.Enabled && (c.Scope == ScopePRTitle || c.Scope == ScopeBoth)
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// sortedKeys returns a map's keys in a stable order, so validation reports the
// same first error for the same spec and rendering is byte-identical run to
// run. Go map iteration order is randomised, which would otherwise make a
// re-render a spurious diff.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

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
	"fmt"
	"os"
	"path/filepath"
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
	Bulwark             Bulwark             `yaml:"bulwark" json:"bulwark"`
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
}

// DependabotGroup is one named group inside an ecosystem entry. Note carries
// the rationale, re-emitted as a comment above the rendered group.
type DependabotGroup struct {
	Name     string   `yaml:"name" json:"name"`
	Patterns []string `yaml:"patterns" json:"patterns"`
	Note     string   `yaml:"note,omitempty" json:"note,omitempty"`
}

type DependabotAutoMerge struct {
	Enabled      bool   `yaml:"enabled" json:"enabled"`
	Schedule     string `yaml:"schedule" json:"schedule"`
	MaxBump      string `yaml:"max_bump" json:"max_bump"`
	DeleteBranch bool   `yaml:"delete_branch" json:"delete_branch"`
}

// Bulwark is the shared code-quality and security gate. Every governed repo
// carries it — that is the convention — and it is disabled only where a repo
// already wires bulwark into its own pipeline with coverage plumbing gt cannot
// generically reproduce.
type Bulwark struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Dir scopes bulwark to a subdirectory. It stays an input rather than
	// moving into .bulwark.yml because that file lives *at* the scan root, so
	// bulwark must know the root before it can read its own config.
	Dir string `yaml:"dir,omitempty" json:"dir,omitempty"`
}

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
	// MergeQueue adds the merge_group trigger. Without it a queued PR waits
	// forever for a required check that never reports. Merge queues need an
	// organization-owned repository, so this is off by default.
	MergeQueue bool `yaml:"merge_queue" json:"merge_queue"`
}

type PipelineCD struct {
	Enabled bool     `yaml:"enabled" json:"enabled"`
	Stages  []string `yaml:"stages" json:"stages"`
	// Tags are the push patterns that trigger delivery. Repos ship on
	// different ones — a single component on v*.*.*, or several independently
	// versioned components on their own prefixes.
	Tags []string `yaml:"tags" json:"tags"`
}

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
}

type BranchProtection struct {
	Branch            string `yaml:"branch" json:"branch"`
	RequiredApprovals int    `yaml:"required_approvals" json:"required_approvals"`
	RequireUpToDate   bool   `yaml:"require_up_to_date" json:"require_up_to_date"`
}

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
	CIStages = []string{"preflight", "build", "test", "end2end"}
	CDStages = []string{"preflight", "publish", "deploy", "verify"}
)

// GateCheckJob is the aggregating job, and so the single check branch
// protection requires. It is a plain job in a repo-owned workflow, so its
// check name carries no "<caller> / " prefix.
const GateCheckJob = "ci-gate"

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
			},
			BranchProtection: BranchProtection{
				Branch: "main",
				// False by design. Requiring branches to be up to date turns
				// every other open PR red on each merge; the validated-tree
				// attestation proves the same property after the fact, without
				// anyone having to rebase.
				RequireUpToDate: false,
			},
		},
		Bulwark: Bulwark{Enabled: true},
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
func Parse(data []byte, path string) (Spec, error) {
	spec := Default()
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return Spec{}, fmt.Errorf("parse %s: %w", path, err)
	}
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

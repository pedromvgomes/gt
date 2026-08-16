package tests

import (
	"strings"
	"testing"

	"github.com/pedromvgomes/gt/internal/repospec"
)

func TestParseAppliesDefaults(t *testing.T) {
	spec, err := repospec.Parse([]byte("dependabot:\n  - ecosystem: gomod\n    directory: /\n"), "t.yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !spec.DependabotAutoMerge.Enabled || spec.DependabotAutoMerge.MaxBump != repospec.BumpMinor {
		t.Errorf("auto-merge defaults = %+v", spec.DependabotAutoMerge)
	}
	if !spec.Settings.Merge.Squash || spec.Settings.Merge.MergeCommit {
		t.Errorf("merge defaults should be squash-only, got %+v", spec.Settings.Merge)
	}
	if spec.Settings.BranchProtection.Branch != "main" {
		t.Errorf("branch = %q, want main", spec.Settings.BranchProtection.Branch)
	}
}

func TestValidateRejectsBadSpecs(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*repospec.Spec)
		wantSub string
	}{
		{
			name: "unknown ecosystem",
			mutate: func(s *repospec.Spec) {
				s.Dependabot = []repospec.DependabotEntry{{Ecosystem: "maven", Directory: "/"}}
			},
			wantSub: "unknown ecosystem",
		},
		{
			name:    "missing directory",
			mutate:  func(s *repospec.Spec) { s.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod"}} },
			wantSub: "directory is required",
		},
		{
			name: "relative directory",
			mutate: func(s *repospec.Spec) {
				s.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "web"}}
			},
			wantSub: `must start with "/"`,
		},
		{
			name: "duplicate ecosystem/directory",
			mutate: func(s *repospec.Spec) {
				s.Dependabot = []repospec.DependabotEntry{
					{Ecosystem: "gomod", Directory: "/"},
					{Ecosystem: "gomod", Directory: "/"},
				}
			},
			wantSub: "duplicate entry",
		},
		{
			name: "group without a name",
			mutate: func(s *repospec.Spec) {
				s.Dependabot = []repospec.DependabotEntry{{
					Ecosystem: "gomod", Directory: "/",
					Groups: []repospec.DependabotGroup{{Patterns: []string{"x*"}}},
				}}
			},
			wantSub: "name is required",
		},
		{
			// A patternless group is not inert: Dependabot matches every
			// dependency in the ecosystem into it, collapsing all updates into
			// a single PR. Failing loudly beats doing that on a typo.
			name: "group without patterns",
			mutate: func(s *repospec.Spec) {
				s.Dependabot = []repospec.DependabotEntry{{
					Ecosystem: "gomod", Directory: "/",
					Groups: []repospec.DependabotGroup{{Name: "everything"}},
				}}
			},
			wantSub: "at least one pattern is required",
		},
		{
			name: "duplicate group name",
			mutate: func(s *repospec.Spec) {
				s.Dependabot = []repospec.DependabotEntry{{
					Ecosystem: "gomod", Directory: "/",
					Groups: []repospec.DependabotGroup{
						{Name: "dup", Patterns: []string{"a*"}},
						{Name: "dup", Patterns: []string{"b*"}},
					},
				}}
			},
			wantSub: "duplicate group name",
		},
		{
			name: "invalid applies_to",
			mutate: func(s *repospec.Spec) {
				s.Dependabot = []repospec.DependabotEntry{{
					Ecosystem: "gomod", Directory: "/",
					Groups: []repospec.DependabotGroup{{
						Name: "g", Patterns: []string{"a*"}, AppliesTo: "everything",
					}},
				}}
			},
			wantSub: "applies_to",
		},
		{
			name:    "invalid conventional-commit scope",
			mutate:  func(s *repospec.Spec) { s.ConventionalCommits.Scope = "title" },
			wantSub: "conventional_commits.scope",
		},
		{
			name:    "invalid max_bump",
			mutate:  func(s *repospec.Spec) { s.DependabotAutoMerge.MaxBump = "patchy" },
			wantSub: "max_bump",
		},
		{
			name:    "malformed cron",
			mutate:  func(s *repospec.Spec) { s.DependabotAutoMerge.Schedule = "0 1 * *" },
			wantSub: "expected 5 space-separated fields",
		},
		{
			name:    "no merge method",
			mutate:  func(s *repospec.Spec) { s.Settings.Merge = repospec.MergeSettings{} },
			wantSub: "at least one merge method",
		},
		{
			name:    "unknown file key",
			mutate:  func(s *repospec.Spec) { s.Files = []string{"makefile"} },
			wantSub: "unknown file",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := repospec.Default()
			tc.mutate(&spec)
			err := repospec.Validate(spec)
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("Validate() = %v, want error containing %q", err, tc.wantSub)
			}
		})
	}
}

func TestConventionalCommitScopeHelpers(t *testing.T) {
	tests := []struct {
		scope       string
		enabled     bool
		wantTitle   bool
		wantCommits bool
		description string
	}{
		{repospec.ScopePRTitle, true, true, false, "pr_title lints the title only"},
		{repospec.ScopeCommits, true, false, true, "commits lints subjects only"},
		{repospec.ScopeBoth, true, true, true, "both lints title and subjects"},
		{repospec.ScopeBoth, false, false, false, "disabled lints nothing"},
	}
	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			c := repospec.ConventionalCommits{Enabled: tc.enabled, Scope: tc.scope}
			if got := c.EnforcesPRTitle(); got != tc.wantTitle {
				t.Errorf("EnforcesPRTitle() = %v, want %v", got, tc.wantTitle)
			}
			if got := c.EnforcesCommits(); got != tc.wantCommits {
				t.Errorf("EnforcesCommits() = %v, want %v", got, tc.wantCommits)
			}
		})
	}
}

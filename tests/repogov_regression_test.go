package tests

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pedromvgomes/gt/internal/git"
	"github.com/pedromvgomes/gt/internal/repogov"
	"github.com/pedromvgomes/gt/internal/repospec"
	"gopkg.in/yaml.v3"
)

// A reusable workflow can only narrow the caller's token, never widen it, so
// every permission the called jobs need must be granted by the rendered
// caller. Getting this wrong fails silently: the auto-merge job simply never
// merges, and the gate's aggregation simply reads nothing.
func TestRenderedCallersGrantPermissionsTheCalledJobsNeed(t *testing.T) {
	spec := repospec.Default()
	files := renderMap(t, testInput(spec))

	tests := []struct {
		path string
		want map[string]string
	}{
		{
			// Merging PRs and deleting branches needs writes.
			path: ".github/workflows/dependabot-auto-merge.yml",
			want: map[string]string{"contents": "write", "pull-requests": "write"},
		},
		{
			// Pushing a branch and opening a PR needs writes.
			path: ".github/workflows/gt-sync.yml",
			want: map[string]string{"contents": "write", "pull-requests": "write"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			content, ok := files[tc.path]
			if !ok {
				t.Fatalf("%s was not rendered", tc.path)
			}
			var wf struct {
				Permissions map[string]string `yaml:"permissions"`
			}
			if err := yaml.Unmarshal(content, &wf); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			for scope, level := range tc.want {
				if got := wf.Permissions[scope]; got != level {
					t.Errorf("permissions.%s = %q, want %q\n%s", scope, got, level, content)
				}
			}
		})
	}
}

// gt renders callers named gate.yml and dependabot-auto-merge.yml. If gt's own
// reusable workflows used those same filenames, gt governing itself would
// overwrite its definitions with thin callers pointing at themselves.
func TestCallersReferenceDistinctlyNamedUpstreamWorkflows(t *testing.T) {
	files := renderMap(t, testInput(repospec.Default()))

	for path, content := range files {
		if !strings.HasPrefix(path, repogov.WorkflowDir+"/") {
			continue
		}
		var wf struct {
			Jobs map[string]struct {
				Uses string `yaml:"uses"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(content, &wf); err != nil {
			t.Fatalf("unmarshal %s: %v", path, err)
		}
		for id, job := range wf.Jobs {
			if job.Uses == "" {
				continue
			}
			// A `./` reference is a pipeline stage the repository owns, not an
			// upstream workflow; only the latter is under test here.
			if strings.HasPrefix(job.Uses, "./") {
				continue
			}
			// The upstream path must differ from the rendered path, or the
			// caller would reference itself once gt governs its own repo.
			upstream := job.Uses
			if i := strings.Index(upstream, "@"); i >= 0 {
				upstream = upstream[:i]
			}
			upstream = strings.TrimPrefix(upstream, repogov.Upstream+"/")
			if upstream == path {
				t.Errorf("%s job %q calls itself (%s)", path, id, job.Uses)
			}
			if !strings.Contains(upstream, "reusable-") {
				t.Errorf("%s job %q references %q, expected a reusable-* upstream workflow", path, id, upstream)
			}
		}
	}
}

// The mirror image of the test below, and the one onboarding depends on: a
// repository adopting gt usually already has a CODEOWNERS. gt knows that path,
// so without the marker check the very first sync would delete a file gt never
// wrote — silently, and before anyone had a chance to opt in.
func TestPreExistingFilesGtDidNotWriteAreLeftAlone(t *testing.T) {
	root := t.TempDir()

	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/"}}
	spec.Files = []string{"sync"} // codeowners deliberately not declared
	if err := repogov.SaveSpec(root, spec); err != nil {
		t.Fatalf("SaveSpec() error = %v", err)
	}

	codeowners := filepath.Join(root, ".github", "CODEOWNERS")
	if err := os.MkdirAll(filepath.Dir(codeowners), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const handWritten = "# The repository's own, predating gt.\n* @wardnet/maintainers\n"
	if err := os.WriteFile(codeowners, []byte(handWritten), 0o644); err != nil {
		t.Fatalf("write CODEOWNERS: %v", err)
	}

	report, err := repogov.Check(testOptions(root))
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	for _, r := range report.Results {
		if r.Path == ".github/CODEOWNERS" && r.Status == repogov.StatusOrphaned {
			t.Error("a hand-written CODEOWNERS was reported as an orphan gt may delete")
		}
	}

	if _, _, err := repogov.Sync(testOptions(root)); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	got, err := os.ReadFile(codeowners)
	if err != nil {
		t.Fatalf("CODEOWNERS was removed by sync: %v", err)
	}
	if string(got) != handWritten {
		t.Errorf("CODEOWNERS was rewritten:\ngot:  %q\nwant: %q", got, handWritten)
	}
}

// Orphan removal keys off the marker, so a managed template that lost it would
// become undeletable — gt would render it, stop recognising it, and leave it
// running forever once the spec dropped it.
func TestEveryManagedFileCarriesTheMarker(t *testing.T) {
	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/"}}
	spec.Files = repospec.FileKeys

	files, err := repogov.Render(testInput(spec))
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	seen := 0
	for _, f := range files {
		if f.Mode != repogov.ModeManaged {
			continue
		}
		seen++
		if !strings.Contains(string(f.Content), repogov.ManagedMarker) {
			t.Errorf("managed file %s does not carry %q, so gt could never remove it as an orphan",
				f.Path, repogov.ManagedMarker)
		}
	}
	if seen == 0 {
		t.Fatal("no managed files rendered; the test proves nothing")
	}
}

// Dropping an entry from files:, or removing the last dependabot ecosystem,
// must not leave the previously rendered file on disk still running while
// check reports the repository compliant.
func TestOrphanedFilesAreDetectedAndRemoved(t *testing.T) {
	root := t.TempDir()

	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/"}}
	spec.Files = []string{"sync", "codeowners"}
	if err := repogov.SaveSpec(root, spec); err != nil {
		t.Fatalf("SaveSpec() error = %v", err)
	}
	if _, _, err := repogov.Sync(testOptions(root)); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	codeowners := filepath.Join(root, ".github", "CODEOWNERS")
	if _, err := os.Stat(codeowners); err != nil {
		t.Fatalf("CODEOWNERS should exist after the first sync: %v", err)
	}

	// Opt out of CODEOWNERS and of Dependabot entirely.
	spec.Files = []string{"sync"}
	spec.Dependabot = nil
	if err := repogov.SaveSpec(root, spec); err != nil {
		t.Fatalf("SaveSpec() error = %v", err)
	}

	report, err := repogov.Check(testOptions(root))
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if report.Clean() {
		t.Fatal("Check() reported compliant while orphaned files were still on disk")
	}
	orphans := map[string]bool{}
	for _, r := range repogov.Drifted(report.Results) {
		if r.Status == repogov.StatusOrphaned {
			orphans[r.Path] = true
		}
	}
	for _, want := range []string{".github/CODEOWNERS", ".github/dependabot.yml"} {
		if !orphans[want] {
			t.Errorf("%s was not reported as orphaned; orphans = %v", want, orphans)
		}
	}

	if _, _, err := repogov.Sync(testOptions(root)); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if _, err := os.Stat(codeowners); !os.IsNotExist(err) {
		t.Error("CODEOWNERS still present after sync removed the orphan")
	}

	after, err := repogov.Check(testOptions(root))
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !after.Clean() {
		t.Fatalf("Check() not clean after removing orphans: %v", repogov.Drifted(after.Results))
	}
}

// A --skip-workflows run has not brought the workflow files up to date, so it
// must not stamp the spec as rendered by this gt version — that stamp is the
// only remaining signal those files are behind.
func TestSkipWorkflowsDoesNotStampVersion(t *testing.T) {
	root := t.TempDir()
	spec := repospec.Default()
	spec.GTVersion = "v0.1.0"
	spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/"}}
	if err := repogov.SaveSpec(root, spec); err != nil {
		t.Fatalf("SaveSpec() error = %v", err)
	}

	opts := testOptions(root)
	opts.SkipWorkflows = true
	if _, _, err := repogov.Sync(opts); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	loaded, err := repospec.Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.GTVersion != "v0.1.0" {
		t.Errorf("gt_version = %q, want it left at v0.1.0 by a --skip-workflows sync", loaded.GTVersion)
	}

	report, err := repogov.Check(testOptions(root))
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !report.VersionStale {
		t.Error("staleness warning was suppressed even though workflow files were never rendered")
	}
}

// Reporting a transient auth or network failure as "this branch has no
// protection" would be actively misleading about a shared branch.
func TestSettingsDiffSurfacesNonNotFoundProtectionErrors(t *testing.T) {
	gh := &fakeGH{
		responses: map[string]string{"repos/pedromvgomes/demo": compliantRepoJSON},
		errors:    map[string]error{"protection": errForbidden{}},
	}
	_, err := repogov.SettingsDiff(context.Background(), gh, repospec.Default(), "pedromvgomes", "demo")
	if err == nil {
		t.Fatal("SettingsDiff() = nil, want the 403 surfaced rather than read as 'not protected'")
	}
	if !strings.Contains(err.Error(), "protection") {
		t.Errorf("error %q does not mention branch protection", err)
	}
}

type errForbidden struct{}

func (errForbidden) Error() string { return "gh: HTTP 403: Resource not accessible by integration" }

// The workflows consume `gt repo config --json`. Its whole reason for existing
// is that an explicitly-false value must survive: the yq expression it replaced
// used `//`, the alternative operator, which treats `false` as absent and so
// silently restored the default.
func TestSpecJSONPreservesExplicitFalse(t *testing.T) {
	spec, err := repospec.Parse([]byte(
		"conventional_commits:\n  enabled: false\ndependabot_auto_merge:\n  delete_branch: false\n"), "t.yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var got struct {
		ConventionalCommits struct {
			Enabled bool     `json:"enabled"`
			Types   []string `json:"types"`
			Scope   string   `json:"scope"`
		} `json:"conventional_commits"`
		DependabotAutoMerge struct {
			Enabled      bool `json:"enabled"`
			DeleteBranch bool `json:"delete_branch"`
		} `json:"dependabot_auto_merge"`
		Pipeline struct {
			CI struct {
				Enabled bool     `json:"enabled"`
				Stages  []string `json:"stages"`
			} `json:"ci"`
		} `json:"pipeline"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if got.ConventionalCommits.Enabled {
		t.Error("conventional_commits.enabled came back true after being set to false")
	}
	if got.DependabotAutoMerge.DeleteBranch {
		t.Error("dependabot_auto_merge.delete_branch came back true after being set to false")
	}
	// An untouched sibling keeps its default rather than becoming the zero value.
	if !got.DependabotAutoMerge.Enabled {
		t.Error("dependabot_auto_merge.enabled lost its default")
	}
	// The gate reads types from here; an empty list would make it fall back to
	// the action's own defaults instead of gt's.
	if len(got.ConventionalCommits.Types) == 0 {
		t.Error("conventional_commits.types is empty; the gate would not get gt's defaults")
	}
	if !got.Pipeline.CI.Enabled || len(got.Pipeline.CI.Stages) == 0 {
		t.Error("pipeline.ci lost its defaults")
	}
}

// In a gt-managed bare layout, setup.Context.WorkDir always points at the
// default-branch checkout, because that is what post-clone setup templates
// operate on. Governance must instead act on the worktree the user is standing
// in — otherwise `gt repo sync` from a feature worktree writes its changes into
// the main checkout, leaving the worktree untouched and dirtying a tree the
// user was not working in.
func TestResolveWorkDirUsesTheCurrentWorktree(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	runner := git.ExecRunner{}

	mustGit := func(dir string, args ...string) {
		t.Helper()
		if _, err := runner.Run(ctx, dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	mustGit(root, "init", "-q", "-b", "main", ".")
	mustGit(root, "config", "user.email", "t@example.com")
	mustGit(root, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustGit(root, "add", "-A")
	mustGit(root, "commit", "-qm", "init")

	linked := filepath.Join(t.TempDir(), "feature")
	mustGit(root, "worktree", "add", "-q", "-b", "feature/x", linked)

	for name, dir := range map[string]string{"main checkout": root, "linked worktree": linked} {
		t.Run(name, func(t *testing.T) {
			got, err := repogov.ResolveWorkDir(ctx, runner, dir)
			if err != nil {
				t.Fatalf("ResolveWorkDir() error = %v", err)
			}
			// macOS temp dirs are symlinked (/var -> /private/var), so compare
			// resolved paths rather than the raw strings.
			wantResolved, _ := filepath.EvalSymlinks(dir)
			gotResolved, _ := filepath.EvalSymlinks(got)
			if gotResolved != wantResolved {
				t.Errorf("ResolveWorkDir() = %q, want %q", gotResolved, wantResolved)
			}
		})
	}
}

func TestResolveWorkDirOutsideARepository(t *testing.T) {
	if _, err := repogov.ResolveWorkDir(context.Background(), git.ExecRunner{}, t.TempDir()); err == nil {
		t.Error("ResolveWorkDir() outside a repo = nil, want error")
	}
}

// A PR-title check that does not re-run on `edited` leaves a corrected title
// red until an unrelated push — the failure mode tumika's pr-title.yml calls
// out as load-bearing. When gt enforces the title, the caller must ask for it.
func TestOrchestratorListensForTitleEditsOnlyWhenTitleIsEnforced(t *testing.T) {
	tests := []struct {
		name      string
		scope     string
		enabled   bool
		wantTypes bool
	}{
		{"pr_title enforces the title", repospec.ScopePRTitle, true, true},
		{"both enforces the title", repospec.ScopeBoth, true, true},
		{"commits does not touch the title", repospec.ScopeCommits, true, false},
		{"disabled enforces nothing", repospec.ScopePRTitle, false, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := repospec.Default()
			spec.ConventionalCommits.Enabled = tc.enabled
			spec.ConventionalCommits.Scope = tc.scope

			content := renderMap(t, testInput(spec))[".github/workflows/ci-orchestration.yml"]
			var wf struct {
				On struct {
					PullRequest struct {
						Types []string `yaml:"types"`
					} `yaml:"pull_request"`
				} `yaml:"on"`
			}
			if err := yaml.Unmarshal(content, &wf); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			var hasEdited bool
			for _, ty := range wf.On.PullRequest.Types {
				if ty == "edited" {
					hasEdited = true
				}
			}
			if hasEdited != tc.wantTypes {
				t.Errorf("edited trigger = %v, want %v\n%s", hasEdited, tc.wantTypes, content)
			}
			// Narrowing types must never drop the defaults the gate relies on.
			if tc.wantTypes {
				for _, required := range []string{"opened", "synchronize", "reopened"} {
					found := false
					for _, ty := range wf.On.PullRequest.Types {
						if ty == required {
							found = true
						}
					}
					if !found {
						t.Errorf("types %v dropped the default %q", wf.On.PullRequest.Types, required)
					}
				}
			}
		})
	}
}

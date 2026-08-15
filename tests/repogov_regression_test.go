package tests

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
			// `gh pr checks` needs pull-requests: read; without it the gate
			// reads nothing and waits out the full timeout.
			path: ".github/workflows/gate.yml",
			want: map[string]string{"pull-requests": "read", "checks": "read", "contents": "read"},
		},
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

// Dropping an entry from files:, or removing the last dependabot ecosystem,
// must not leave the previously rendered file on disk still running while
// check reports the repository compliant.
func TestOrphanedFilesAreDetectedAndRemoved(t *testing.T) {
	root := t.TempDir()

	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/"}}
	spec.Files = []string{"gate", "codeowners"}
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
	spec.Files = []string{"gate"}
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

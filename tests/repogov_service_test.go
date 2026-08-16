package tests

import (
	"os"
	"strings"
	"testing"

	"github.com/pedromvgomes/gt/internal/repogov"
	"github.com/pedromvgomes/gt/internal/repospec"
)

func testOptions(workdir string) repogov.Options {
	return repogov.Options{
		WorkDir:   workdir,
		RepoOwner: "pedromvgomes",
		RepoName:  "demo",
		GTVersion: "v0.6.0",
	}
}

// The spec `init` writes must be readable by `check`. If the round trip lost
// or mangled a field, init would produce a repo that immediately fails its own
// verification.
func TestSpecRoundTrip(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module demo\n")
	writeWorkflow(t, root, "ci.yml", `
name: CI
on: pull_request
jobs:
  build:
    name: Build
    runs-on: ubuntu-latest
`)

	spec, err := repogov.Init(testOptions(root))
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if err := repogov.SaveSpec(root, spec); err != nil {
		t.Fatalf("SaveSpec() error = %v", err)
	}

	loaded, err := repospec.Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.GTVersion != spec.GTVersion {
		t.Errorf("gt_version = %q, want %q", loaded.GTVersion, spec.GTVersion)
	}
	if len(loaded.Dependabot) != len(spec.Dependabot) {
		t.Errorf("dependabot entries = %d, want %d", len(loaded.Dependabot), len(spec.Dependabot))
	}
	if loaded.ConventionalCommits.Scope != spec.ConventionalCommits.Scope {
		t.Errorf("scope = %q, want %q", loaded.ConventionalCommits.Scope, spec.ConventionalCommits.Scope)
	}

	// The header explains how the file is meant to be edited; losing it on
	// rewrite would strip that guidance on the first sync.
	raw, err := os.ReadFile(repospec.Path(root))
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	if !strings.Contains(string(raw), "gt repo sync") {
		t.Errorf("saved spec lost its header:\n%s", raw)
	}
}

// check -> sync -> check must converge, and the second check must be clean.
func TestCheckSyncConverges(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module demo\n")

	spec, err := repogov.Init(testOptions(root))
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if err := repogov.SaveSpec(root, spec); err != nil {
		t.Fatalf("SaveSpec() error = %v", err)
	}

	before, err := repogov.Check(testOptions(root))
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if before.Clean() {
		t.Fatal("Check() reported clean before any files were rendered")
	}

	if _, _, err := repogov.Sync(testOptions(root)); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	after, err := repogov.Check(testOptions(root))
	if err != nil {
		t.Fatalf("Check() after sync error = %v", err)
	}
	if !after.Clean() {
		t.Fatalf("Check() after sync is not clean: %v", repogov.Drifted(after.Results))
	}

	// A second sync must be a no-op; anything else means rendering is not
	// deterministic and the weekly job would open a PR every week forever.
	_, written, err := repogov.Sync(testOptions(root))
	if err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	if len(written) != 0 {
		t.Errorf("second Sync() wrote %v, want nothing", written)
	}
}

// Sync stamps the running gt version so `check` can report when a repo was
// rendered by older policy.
func TestSyncStampsVersionAndReportsStaleness(t *testing.T) {
	root := t.TempDir()
	spec := repospec.Default()
	spec.GTVersion = "v0.1.0"
	spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/"}}
	if err := repogov.SaveSpec(root, spec); err != nil {
		t.Fatalf("SaveSpec() error = %v", err)
	}

	report, err := repogov.Check(testOptions(root))
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !report.VersionStale {
		t.Error("Check() did not flag a spec rendered by an older gt")
	}

	if _, _, err := repogov.Sync(testOptions(root)); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	loaded, err := repospec.Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.GTVersion != "v0.6.0" {
		t.Errorf("gt_version = %q after sync, want v0.6.0", loaded.GTVersion)
	}
}

// A repo with no manifest must say so actionably rather than failing obscurely.
func TestCheckWithoutSpecPointsAtInit(t *testing.T) {
	_, err := repogov.Check(testOptions(t.TempDir()))
	if err == nil {
		t.Fatal("Check() without a spec = nil, want error")
	}
	if !strings.Contains(err.Error(), "gt repo init") {
		t.Errorf("error %q does not suggest 'gt repo init'", err)
	}
}

// The weekly in-repo sync runs with --skip-workflows, so it must leave workflow
// files untouched even when they have drifted.
func TestSyncSkipWorkflowsLeavesWorkflowFilesAlone(t *testing.T) {
	root := t.TempDir()
	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/"}}
	if err := repogov.SaveSpec(root, spec); err != nil {
		t.Fatalf("SaveSpec() error = %v", err)
	}

	opts := testOptions(root)
	opts.SkipWorkflows = true
	if _, _, err := repogov.Sync(opts); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	if _, err := os.Stat(root + "/.github/workflows/ci-orchestration.yml"); !os.IsNotExist(err) {
		t.Error("ci-orchestration.yml was written despite --skip-workflows")
	}
	if _, err := os.Stat(root + "/.github/dependabot.yml"); err != nil {
		t.Errorf("dependabot.yml should still be written: %v", err)
	}
}

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

// The "v" prefix is not drift between two conventions we control: goreleaser
// stamps main.version as "1.0.0" while tags, docs and hand-built binaries all
// say "v1.0.0". Comparing raw strings called every repository stale the moment
// a release binary looked at it, and told its owner to re-sync something that
// was already byte-identical.
//
// Warning wrongly is worse than not warning — the signal only means something
// if it fires when something is actually true.
func TestVersionStaleIgnoresTheVPrefix(t *testing.T) {
	tests := []struct {
		name      string
		spec, run string
		wantStale bool
	}{
		{"release binary against a v-prefixed spec", "v1.0.0", "1.0.0", false},
		{"v-prefixed binary against a bare spec", "1.0.0", "v1.0.0", false},
		{"identical", "v1.0.0", "v1.0.0", false},
		{"surrounding whitespace", " v1.0.0 ", "1.0.0", false},
		{"genuinely older", "v1.0.0", "1.1.0", true},
		{"genuinely newer", "v1.2.0", "1.1.0", true},
		{"unrendered spec never warns", "", "1.0.0", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			spec := repospec.Default()
			spec.GTVersion = tt.spec
			spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/"}}
			if err := repogov.SaveSpec(root, spec); err != nil {
				t.Fatalf("SaveSpec() error = %v", err)
			}
			opts := testOptions(root)
			opts.GTVersion = tt.run
			report, err := repogov.Check(opts)
			if err != nil {
				t.Fatalf("Check() error = %v", err)
			}
			if report.VersionStale != tt.wantStale {
				t.Errorf("VersionStale = %v for spec %q vs running %q, want %v",
					report.VersionStale, tt.spec, tt.run, tt.wantStale)
			}
		})
	}
}

// `check` warning "sync to re-render with current policy" is only useful if
// sync then does something. A repository whose rendering did not change across
// the releases between has every managed file byte-correct and a spec that
// round-trips, so the two older conditions both say "nothing to write" while
// gt_version still records the older gt — and the warning repeats forever.
func TestNeedsWriteCoversAStaleVersionStampAlone(t *testing.T) {
	for name, tc := range map[string]struct {
		report        repogov.Report
		skipWorkflows bool
		want          bool
	}{
		"nothing stale":                {report: repogov.Report{}, want: false},
		"version alone":                {report: repogov.Report{VersionStale: true}, want: true},
		"spec alone":                   {report: repogov.Report{SpecStale: true}, want: true},
		"drifted file":                 {report: repogov.Report{Results: []repogov.Result{{Status: repogov.StatusDrifted}}}, want: true},
		"version alone, skipWorkflows": {report: repogov.Report{VersionStale: true}, skipWorkflows: true, want: false},
		// Skipping workflows withholds the version stamp, not the rest of the
		// write: a spec still restating defaults is slimmed on the weekly
		// in-repo run, which is what rolls that out across the fleet.
		"spec stale, skipWorkflows": {report: repogov.Report{SpecStale: true, VersionStale: true}, skipWorkflows: true, want: true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tc.report.NeedsWrite(tc.skipWorkflows); got != tc.want {
				t.Errorf("NeedsWrite(%v) = %v, want %v", tc.skipWorkflows, got, tc.want)
			}
		})
	}
}

// The end-to-end shape of the same bug: a repository that is compliant in every
// other respect must still be brought to the running version by one sync, and
// must be clean on the next check.
func TestSyncClearsAStaleVersionOnAnOtherwiseCompliantRepo(t *testing.T) {
	root := t.TempDir()
	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/"}}
	if err := repogov.SaveSpec(root, spec); err != nil {
		t.Fatalf("SaveSpec() error = %v", err)
	}
	// Render everything, so only the stamp can be out of date afterwards.
	if _, _, err := repogov.Sync(testOptions(root)); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	loaded, err := repospec.Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	loaded.GTVersion = "v0.5.0"
	if err := repogov.SaveSpec(root, loaded); err != nil {
		t.Fatalf("SaveSpec() error = %v", err)
	}

	report, err := repogov.Check(testOptions(root))
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if len(repogov.Drifted(report.Results)) != 0 || report.SpecStale {
		t.Fatalf("setup is not the case under test: drifted=%d specStale=%v",
			len(repogov.Drifted(report.Results)), report.SpecStale)
	}
	if !report.NeedsWrite(false) {
		t.Fatal("NeedsWrite(false) = false on a stale version stamp, so sync would write nothing and the warning could never clear")
	}

	if _, _, err := repogov.Sync(testOptions(root)); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	after, err := repogov.Check(testOptions(root))
	if err != nil {
		t.Fatalf("second Check() error = %v", err)
	}
	if after.VersionStale {
		t.Error("VersionStale still set after sync; the warning would repeat forever")
	}
}

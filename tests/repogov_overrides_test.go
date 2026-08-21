package tests

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pedromvgomes/gt/internal/repogov"
	"github.com/pedromvgomes/gt/internal/repospec"
)

// A written spec must contain only what differs from gt's defaults.
//
// This is the property the whole design rests on: a repository that pins a
// default has stopped tracking it, so changing an opinion in gt would reach
// none of the repositories that already spell out the old value.
func TestSaveSpecOmitsDefaults(t *testing.T) {
	root := t.TempDir()
	spec := repospec.Default()
	spec.GTVersion = "v1.2.0"
	spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/"}}

	if err := repogov.SaveSpec(root, spec); err != nil {
		t.Fatalf("SaveSpec() error = %v", err)
	}
	raw := readSpecFile(t, root)

	// Nothing that merely restates a default survives.
	for _, key := range []string{
		"conventional_commits:", "settings:", "merge:", "branch_protection:",
		"dependabot_auto_merge:", "bulwark:", "files:", "squash_title:",
		"dismiss_stale_reviews:", "require_thread_resolution:", "max_bump:",
	} {
		if strings.Contains(raw, key) {
			t.Errorf("saved spec still pins the default %q:\n%s", key, raw)
		}
	}
	// What is genuinely the repository's own still does.
	for _, key := range []string{"gt_version:", "dependabot:", "ecosystem: gomod"} {
		if !strings.Contains(raw, key) {
			t.Errorf("saved spec dropped %q, which is not a default:\n%s", key, raw)
		}
	}
}

// Omitting defaults must not change meaning. Parse layers the file over
// Default(), so a pruned file and a fully spelled-out one have to resolve to
// the same spec — otherwise slimming the fleet's files would silently
// reconfigure it.
func TestSavedSpecResolvesIdentically(t *testing.T) {
	root := t.TempDir()

	want := repospec.Default()
	want.GTVersion = "v1.2.0"
	want.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/"}}
	// A representative override of each shape: a bool flipped off, a string,
	// and a list that differs from the default list.
	want.Bulwark.Coverage = false
	want.Settings.BranchProtection.Branch = "trunk"
	want.Pipeline.CI.Stages = []string{"preflight", "test"}

	if err := repogov.SaveSpec(root, want); err != nil {
		t.Fatalf("SaveSpec() error = %v", err)
	}
	got, err := repospec.Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip changed the spec:\n got = %#v\nwant = %#v", got, want)
	}

	raw := readSpecFile(t, root)
	for _, key := range []string{"coverage: false", "branch: trunk"} {
		if !strings.Contains(raw, key) {
			t.Errorf("override %q was pruned as if it were a default:\n%s", key, raw)
		}
	}
}

// An explicit false against a default of true is an override, not an absence.
// Pruning by Go zero value rather than by the default would silently turn
// every one of these back on.
func TestExplicitFalseSurvivesPruning(t *testing.T) {
	root := t.TempDir()
	spec := repospec.Default()
	spec.GTVersion = "v1.2.0"
	spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/"}}
	spec.DependabotAutoMerge.Enabled = false
	spec.ConventionalCommits.Enabled = false

	if err := repogov.SaveSpec(root, spec); err != nil {
		t.Fatalf("SaveSpec() error = %v", err)
	}
	loaded, err := repospec.Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.DependabotAutoMerge.Enabled {
		t.Error("dependabot_auto_merge.enabled: false was pruned and came back true")
	}
	if loaded.ConventionalCommits.Enabled {
		t.Error("conventional_commits.enabled: false was pruned and came back true")
	}
}

// A file that spells out defaults is drift: check has to say so, and sync has
// to fix it. Without this, a repository whose managed files all match would
// short-circuit before the spec was ever rewritten, and the redundant keys
// would be unfixable.
func TestCheckReportsAndSyncFixesRestatedDefaults(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module demo\n")

	spec := repospec.Default()
	spec.GTVersion = "v0.6.0"
	spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/"}}
	if err := repogov.SaveSpec(root, spec); err != nil {
		t.Fatalf("SaveSpec() error = %v", err)
	}
	if _, _, err := repogov.Sync(testOptions(root)); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	// Put the fully spelled-out form back, as every onboarded repo has it,
	// keeping the header so the only difference is the restated defaults.
	slim := readSpecFile(t, root)
	header, _, ok := strings.Cut(slim, "\ngt_version:")
	if !ok {
		t.Fatalf("cannot find the header in the written spec:\n%s", slim)
	}
	var buf strings.Builder
	if err := repogov.WriteSpecTo(&buf, spec); err != nil {
		t.Fatalf("WriteSpecTo() error = %v", err)
	}
	fat := header + "\n" + buf.String()
	writeFile(t, root, repospec.FileName, fat)
	if fat == slim {
		t.Fatal("resolved and pruned encodings are identical; this test proves nothing")
	}

	report, err := repogov.Check(testOptions(root))
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !report.SpecStale {
		t.Error("Check() did not flag a spec that restates defaults")
	}
	if report.Clean() {
		t.Error("Check() reported a repository with a stale spec as compliant")
	}

	if _, _, err := repogov.Sync(testOptions(root)); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	after, err := repogov.Check(testOptions(root))
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if after.SpecStale {
		t.Error("Sync() left the spec still restating defaults")
	}
}

// The weekly in-repo sync runs with --skip-workflows, so if that path skipped
// the rewrite this would never reach the fleet on its own.
func TestSkipWorkflowsStillSlimsTheSpecButDoesNotStamp(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module demo\n")

	spec := repospec.Default()
	spec.GTVersion = "v0.1.0"
	spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/"}}
	var buf strings.Builder
	if err := repogov.WriteSpecTo(&buf, spec); err != nil {
		t.Fatalf("WriteSpecTo() error = %v", err)
	}
	writeFile(t, root, repospec.FileName, buf.String())

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
		t.Errorf("--skip-workflows stamped gt_version = %q, want it left at v0.1.0", loaded.GTVersion)
	}
	if strings.Contains(readSpecFile(t, root), "branch_protection:") {
		t.Error("--skip-workflows left the spec restating defaults")
	}
}

func readSpecFile(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, repospec.FileName))
	if err != nil {
		t.Fatalf("read %s: %v", repospec.FileName, err)
	}
	return string(raw)
}

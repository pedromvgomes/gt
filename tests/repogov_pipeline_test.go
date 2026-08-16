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

// workflowJobs parses the jobs of a rendered workflow.
type renderedJob struct {
	Name    string   `yaml:"name"`
	Needs   []string `yaml:"needs"`
	If      string   `yaml:"if"`
	Uses    string   `yaml:"uses"`
	Secrets string   `yaml:"secrets"`
}

func workflowJobs(t *testing.T, content []byte) map[string]renderedJob {
	t.Helper()
	var wf struct {
		Jobs map[string]renderedJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(content, &wf); err != nil {
		t.Fatalf("unmarshal workflow: %v\n%s", err, content)
	}
	return wf.Jobs
}

func pipelineFiles(t *testing.T, spec repospec.Spec) map[string][]byte {
	t.Helper()
	return renderMap(t, testInput(spec))
}

// Every stage and fixed job must be in the gate's needs. One missing means a
// failure in that stage never blocks the merge — the exact failure branch
// protection exists to prevent.
func TestGateWaitsOnEveryJob(t *testing.T) {
	files := pipelineFiles(t, repospec.Default())
	jobs := workflowJobs(t, files[".github/workflows/ci-orchestration.yml"])

	gate, ok := jobs[repospec.GateCheckJob]
	if !ok {
		t.Fatalf("no %s job; jobs = %v", repospec.GateCheckJob, jobs)
	}
	waited := map[string]bool{}
	for _, n := range gate.Needs {
		waited[n] = true
	}
	for name := range jobs {
		if name == repospec.GateCheckJob {
			continue
		}
		if !waited[name] {
			t.Errorf("%s is not in %s's needs — a failure there would not block the merge",
				name, repospec.GateCheckJob)
		}
	}
}

// A stage that is not enabled must drop out of the needs of the ones after it,
// rather than leaving a dangling reference that would make the workflow
// invalid.
func TestDisabledStageCollapsesOutOfDependencies(t *testing.T) {
	spec := repospec.Default()
	spec.Pipeline.CI.Stages = []string{"build", "test"} // no preflight, no end2end

	files := pipelineFiles(t, spec)
	jobs := workflowJobs(t, files[".github/workflows/ci-orchestration.yml"])

	if _, ok := jobs["preflight"]; ok {
		t.Error("preflight was rendered despite not being enabled")
	}
	if _, ok := jobs["end2end"]; ok {
		t.Error("end2end was rendered despite not being enabled")
	}

	for name, job := range jobs {
		for _, n := range job.Needs {
			if _, ok := jobs[n]; !ok {
				t.Errorf("%s needs %q, which is not a job in this workflow", name, n)
			}
		}
		// With no preflight there is nothing to emit run-<stage>, so gating on
		// it would be a condition that can never be true.
		if strings.Contains(job.If, "needs.preflight") {
			t.Errorf("%s gates on preflight output but preflight is not enabled: %s", name, job.If)
		}
	}
}

// bulwark consumes the coverage the test stage uploads. If it did not run
// after tests, the artifact would not exist yet and it would fall back to
// running the whole suite again — the double-run this design removes.
func TestBulwarkRunsAfterTests(t *testing.T) {
	files := pipelineFiles(t, repospec.Default())
	jobs := workflowJobs(t, files[".github/workflows/ci-orchestration.yml"])

	bulwark, ok := jobs["bulwark"]
	if !ok {
		t.Fatal("bulwark job was not rendered")
	}
	var afterTest bool
	for _, n := range bulwark.Needs {
		if n == "test" {
			afterTest = true
		}
	}
	if !afterTest {
		t.Errorf("bulwark needs = %v, want it to include test", bulwark.Needs)
	}
}

func TestBulwarkOmittedWhenDisabled(t *testing.T) {
	spec := repospec.Default()
	spec.Bulwark.Enabled = false
	jobs := workflowJobs(t, pipelineFiles(t, spec)[".github/workflows/ci-orchestration.yml"])
	if _, ok := jobs["bulwark"]; ok {
		t.Error("bulwark was rendered despite being disabled")
	}
	if gate := jobs[repospec.GateCheckJob]; strings.Contains(strings.Join(gate.Needs, ","), "bulwark") {
		t.Errorf("gate still waits on bulwark: %v", gate.Needs)
	}
}

// The stage stubs are the repository's own files. They must be scaffolds, or a
// sync would overwrite a real build script with an empty one.
func TestStageStubsAreScaffolds(t *testing.T) {
	files, err := repogov.Render(testInput(repospec.Default()))
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	wantScaffold := map[string]bool{
		".github/workflows/ci-preflight.yml": true,
		".github/workflows/ci-build.yml":     true,
		".github/workflows/ci-test.yml":      true,
		".github/workflows/ci-end2end.yml":   true,
		".github/workflows/cd-preflight.yml": true,
		".github/workflows/cd-publish.yml":   true,
		".github/workflows/cd-deploy.yml":    true,
		".github/workflows/cd-verify.yml":    true,
	}
	seen := map[string]bool{}
	for _, f := range files {
		if wantScaffold[f.Path] {
			seen[f.Path] = true
			if f.Mode != repogov.ModeScaffold {
				t.Errorf("%s mode = %q, want scaffold — a sync would erase the repo's own pipeline",
					f.Path, f.Mode)
			}
			continue
		}
		// The orchestrators are gt's, and must stay managed.
		if strings.HasSuffix(f.Path, "-orchestration.yml") && f.Mode != repogov.ModeManaged {
			t.Errorf("%s mode = %q, want managed", f.Path, f.Mode)
		}
	}
	for path := range wantScaffold {
		if !seen[path] {
			t.Errorf("%s was not rendered", path)
		}
	}
}

// Deferred from the file-mode work: an unreferenced scaffold holds the
// repository's own work, so dropping a stage must never delete it.
func TestDroppingAStageDoesNotDeleteItsWork(t *testing.T) {
	root := t.TempDir()

	spec := repospec.Default()
	if err := repogov.SaveSpec(root, spec); err != nil {
		t.Fatalf("SaveSpec() error = %v", err)
	}
	if _, _, err := repogov.Sync(testOptions(root)); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	// The repository fills in the stage.
	e2e := filepath.Join(root, ".github", "workflows", "ci-end2end.yml")
	realWork := []byte("on:\n  workflow_call:\njobs:\n  end2end:\n    runs-on: ubuntu-latest\n    steps:\n      - run: make e2e\n")
	if err := os.WriteFile(e2e, realWork, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Then drops the stage from the pipeline.
	spec.Pipeline.CI.Stages = []string{"preflight", "build", "test"}
	if err := repogov.SaveSpec(root, spec); err != nil {
		t.Fatalf("SaveSpec() error = %v", err)
	}

	report, err := repogov.Check(testOptions(root))
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	for _, r := range repogov.Drifted(report.Results) {
		if r.Path == ".github/workflows/ci-end2end.yml" && r.Status == repogov.StatusOrphaned {
			t.Fatal("a dropped stage was marked orphaned; sync would delete the repository's own work")
		}
	}

	if _, _, err := repogov.Sync(testOptions(root)); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	after, err := os.ReadFile(e2e)
	if err != nil {
		t.Fatalf("the dropped stage's file was deleted: %v", err)
	}
	if string(after) != string(realWork) {
		t.Fatalf("the dropped stage's file was rewritten:\n%s", after)
	}
}

func TestCDTriggersOnConfiguredTags(t *testing.T) {
	spec := repospec.Default()
	spec.Pipeline.CD.Tags = []string{"daemon-v*", "sdk-v*"}

	content := pipelineFiles(t, spec)[".github/workflows/cd-orchestration.yml"]
	var wf struct {
		On struct {
			Push struct {
				Tags []string `yaml:"tags"`
			} `yaml:"push"`
		} `yaml:"on"`
	}
	if err := yaml.Unmarshal(content, &wf); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(wf.On.Push.Tags) != 2 || wf.On.Push.Tags[0] != "daemon-v*" {
		t.Errorf("tags = %v, want the configured patterns", wf.On.Push.Tags)
	}
}

// merge_group is what lets a queued PR report its required check. Without the
// trigger the queue waits forever.
func TestMergeQueueTriggerOnlyWhenEnabled(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		spec := repospec.Default()
		spec.Pipeline.CI.MergeQueue = enabled

		content := string(pipelineFiles(t, spec)[".github/workflows/ci-orchestration.yml"])
		got := strings.Contains(content, "merge_group:")
		if got != enabled {
			t.Errorf("merge_group present = %v, want %v", got, enabled)
		}
	}
}

func TestPipelineNotRenderedWhenDisabled(t *testing.T) {
	spec := repospec.Default()
	spec.Pipeline.CI.Enabled = false
	spec.Pipeline.CD.Enabled = false

	for path := range pipelineFiles(t, spec) {
		if strings.Contains(path, "ci-") || strings.Contains(path, "cd-") {
			t.Errorf("%s was rendered despite the pipeline being disabled", path)
		}
	}
}

func TestPipelineValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*repospec.Spec)
		wantSub string
	}{
		{"unknown ci stage", func(s *repospec.Spec) {
			s.Pipeline.CI.Stages = []string{"build", "lint"}
		}, "unknown stage"},
		{"duplicate stage", func(s *repospec.Spec) {
			s.Pipeline.CI.Stages = []string{"build", "build"}
		}, "duplicate stage"},
		{"empty stages", func(s *repospec.Spec) {
			s.Pipeline.CI.Stages = nil
		}, "cannot be empty"},
		{"cd with no tags", func(s *repospec.Spec) {
			s.Pipeline.CD.Tags = nil
		}, "tags cannot be empty"},
		{"cd stage in ci", func(s *repospec.Spec) {
			s.Pipeline.CI.Stages = []string{"publish"}
		}, "unknown stage"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := repospec.Default()
			tc.mutate(&spec)
			err := repospec.Validate(spec)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("Validate() = %v, want an error containing %q", err, tc.wantSub)
			}
		})
	}
}

// A `uses:` pointing at a workflow gt does not ship fails at resolution time,
// before any job starts — the same silent shape as the missing @v0 tag, and
// just as hard to read from the run page. Renaming a reusable workflow without
// updating the renderer must fail here instead.
func TestEveryUpstreamReferenceExists(t *testing.T) {
	// tests/ sits alongside .github/ in the repository root.
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/"}}
	spec.Files = repospec.FileKeys

	seen := 0
	for path, content := range pipelineFiles(t, spec) {
		for _, line := range strings.Split(string(content), "\n") {
			line = strings.TrimSpace(line)
			if !strings.Contains(line, repogov.Upstream+"/") {
				continue
			}
			ref := line[strings.Index(line, repogov.Upstream+"/"):]
			ref = strings.TrimPrefix(ref, repogov.Upstream+"/")
			if i := strings.Index(ref, "@"); i >= 0 {
				ref = ref[:i]
			}
			seen++
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(ref))); err != nil {
				t.Errorf("%s references %s, which gt does not ship: %v", path, ref, err)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no upstream references found; the test is not checking anything")
	}
}

// A called reusable workflow may only NARROW the caller's token. If the caller
// grants less than the called workflow declares, the run fails at parse time —
// no job starts, ci-gate never reports, and branch protection blocks every PR.
//
// The earlier permissions test could not catch this: it only inspected
// workflow-level `permissions` on the thin callers, not the per-job grants the
// orchestrator makes to gt's own reusable workflows.
func TestOrchestratorJobsGrantWhatTheCalledWorkflowsDeclare(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	type jobPerms struct {
		Uses        string            `yaml:"uses"`
		Permissions map[string]string `yaml:"permissions"`
	}

	// Permission strength, so "the caller granted at least this" is checkable.
	rank := map[string]int{"none": 0, "read": 1, "write": 2}

	for _, path := range []string{
		".github/workflows/ci-orchestration.yml",
		".github/workflows/cd-orchestration.yml",
	} {
		content := pipelineFiles(t, repospec.Default())[path]
		var wf struct {
			Jobs map[string]jobPerms `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(content, &wf); err != nil {
			t.Fatalf("unmarshal %s: %v", path, err)
		}

		for id, job := range wf.Jobs {
			if !strings.Contains(job.Uses, repogov.Upstream+"/") {
				continue
			}
			ref := job.Uses[strings.Index(job.Uses, repogov.Upstream+"/"):]
			ref = strings.TrimPrefix(ref, repogov.Upstream+"/")
			if i := strings.Index(ref, "@"); i >= 0 {
				ref = ref[:i]
			}

			called, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(ref)))
			if err != nil {
				t.Fatalf("%s: cannot read %s: %v", path, ref, err)
			}
			var cw struct {
				Jobs map[string]jobPerms `yaml:"jobs"`
			}
			if err := yaml.Unmarshal(called, &cw); err != nil {
				t.Fatalf("unmarshal %s: %v", ref, err)
			}

			for calledID, calledJob := range cw.Jobs {
				for scope, level := range calledJob.Permissions {
					granted := job.Permissions[scope]
					if rank[granted] < rank[level] {
						t.Errorf(
							"%s job %q grants %s:%s but %s job %q declares %s:%s — "+
								"the run fails at parse time and no job starts",
							path, id, scope, orNone(granted), ref, calledID, scope, level)
					}
				}
			}
		}
	}
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// A stage skipped by preflight must not take bulwark down with it: GitHub skips
// a job whose needs were skipped, and ci-gate counts skipped as a pass — so the
// security gate would silently vanish while the required check stayed green.
func TestBulwarkSurvivesASkippedTestStage(t *testing.T) {
	jobs := workflowJobs(t, pipelineFiles(t, repospec.Default())[".github/workflows/ci-orchestration.yml"])
	bulwark, ok := jobs["bulwark"]
	if !ok {
		t.Fatal("bulwark job was not rendered")
	}
	if !strings.Contains(bulwark.If, "!cancelled()") {
		t.Errorf("bulwark if = %q, want it to survive a skipped dependency", bulwark.If)
	}
}

// With CI disabled nothing renders ci-orchestration.yml, so requiring the gate
// context would block every PR on a check that can never report.
func TestBranchProtectionOnlyRequiresTheGateWhenCIIsEnabled(t *testing.T) {
	gh := &fakeGH{responses: map[string]string{
		"branches/main/protection": `{"required_status_checks":{"strict":false,"contexts":[]}}`,
		"repos/pedromvgomes/demo":  compliantRepoJSON,
	}}
	spec := repospec.Default()
	spec.Pipeline.CI.Enabled = false

	changes, err := repogov.SettingsDiff(context.Background(), gh, spec, "pedromvgomes", "demo")
	if err != nil {
		t.Fatalf("SettingsDiff() error = %v", err)
	}
	for _, c := range changes {
		if strings.Contains(c.Field, "contexts") {
			t.Errorf("would require %q with CI disabled — no workflow can report it", c.Want)
		}
	}
}

// The `uses:` major is derived from whichever binary runs sync, so a stale gt
// would silently repoint every caller at an older major. That is the one way
// this design regresses without anyone noticing.
func TestSyncRefusesToDowngradeThePin(t *testing.T) {
	root := t.TempDir()
	spec := repospec.Default()
	spec.GTVersion = "v1.0.0"
	if err := repogov.SaveSpec(root, spec); err != nil {
		t.Fatalf("SaveSpec() error = %v", err)
	}

	opts := testOptions(root) // stamped v0.6.0
	_, _, err := repogov.Sync(opts)
	if err == nil {
		t.Fatal("Sync() = nil, want a refusal to downgrade v1 -> v0")
	}
	for _, want := range []string{"v1.0.0", "gt update", repospec.FileName} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	// And nothing was written.
	if _, statErr := os.Stat(filepath.Join(root, ".github", "workflows", "ci-orchestration.yml")); !os.IsNotExist(statErr) {
		t.Error("sync wrote files despite refusing")
	}
}

// Same major is fine however far apart the minors: the pin is unaffected.
func TestSyncAllowsOlderMinorWithinTheSameMajor(t *testing.T) {
	root := t.TempDir()
	spec := repospec.Default()
	spec.GTVersion = "v0.9.0"
	if err := repogov.SaveSpec(root, spec); err != nil {
		t.Fatalf("SaveSpec() error = %v", err)
	}
	if _, _, err := repogov.Sync(testOptions(root)); err != nil {
		t.Fatalf("Sync() error = %v, want it permitted within the same major", err)
	}
}

// A newer gt adopting an older repo is the normal upgrade path.
func TestSyncAllowsUpgrade(t *testing.T) {
	root := t.TempDir()
	spec := repospec.Default()
	spec.GTVersion = "v0.6.0"
	if err := repogov.SaveSpec(root, spec); err != nil {
		t.Fatalf("SaveSpec() error = %v", err)
	}
	opts := testOptions(root)
	opts.GTVersion = "v1.0.0"
	if _, _, err := repogov.Sync(opts); err != nil {
		t.Fatalf("Sync() error = %v, want an upgrade to be permitted", err)
	}
}

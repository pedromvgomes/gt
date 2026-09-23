package tests

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pedromvgomes/gt/internal/repogov"
	"github.com/pedromvgomes/gt/internal/repospec"
	"gopkg.in/yaml.v3"
)

// workflowJobs parses the jobs of a rendered workflow.
type renderedJob struct {
	Name  string   `yaml:"name"`
	Needs []string `yaml:"needs"`
	If    string   `yaml:"if"`
	Uses  string   `yaml:"uses"`
	// `secrets:` is either the string "inherit" or a map of named secrets, so it
	// has to be decoded loosely and inspected by the tests that care.
	Secrets     any               `yaml:"secrets"`
	With        map[string]any    `yaml:"with"`
	Permissions map[string]string `yaml:"permissions"`
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
		// lydite-baseline is the one exception, by design: it runs only on a
		// push to the default branch, where there is nothing left to merge and
		// the gate is informational. A baseline that failed to record must not
		// paint a commit already on main red.
		if name == repospec.GateCheckJob || name == "lydite-baseline" {
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

// The lydite job forwards to lydite's own pipeline, which runs whatever suites
// it needs itself and consumes nothing this orchestrator produces. Waiting on
// the test stage would only delay it behind work it does not use — and couple
// the security gate to a stage a repository is free to turn off.
func TestLyditeJobDoesNotWaitOnTestStage(t *testing.T) {
	for name, stages := range map[string][]string{
		"with a test stage":    {"preflight", "build", "test"},
		"without a test stage": {"preflight", "build"},
	} {
		t.Run(name, func(t *testing.T) {
			spec := repospec.Default()
			spec.Pipeline.CI.Stages = stages
			jobs := workflowJobs(t, pipelineFiles(t, spec)[".github/workflows/ci-orchestration.yml"])

			lydite, ok := jobs["lydite"]
			if !ok {
				t.Fatal("lydite job was not rendered")
			}
			if !reflect.DeepEqual(lydite.Needs, []string{"attest"}) {
				t.Errorf("lydite needs = %v, want [attest] alone", lydite.Needs)
			}
		})
	}
}

func TestLyditeOmittedWhenDisabled(t *testing.T) {
	spec := repospec.Default()
	spec.Lydite.Enabled = false
	jobs := workflowJobs(t, pipelineFiles(t, spec)[".github/workflows/ci-orchestration.yml"])
	if _, ok := jobs["lydite"]; ok {
		t.Error("lydite was rendered despite lydite being disabled")
	}
	if gate := jobs[repospec.GateCheckJob]; strings.Contains(strings.Join(gate.Needs, ","), "lydite") {
		t.Errorf("gate still waits on lydite: %v", gate.Needs)
	}
}

// A repository with lydite off has no referral to clear, so there is nothing
// for this workflow to listen for.
func TestLyditeClearanceOmittedWhenDisabled(t *testing.T) {
	spec := repospec.Default()
	spec.Lydite.Enabled = false
	files := pipelineFiles(t, spec)
	if _, ok := files[".github/workflows/gt-lydite-clearance.yml"]; ok {
		t.Error("gt-lydite-clearance.yml was rendered despite lydite being disabled")
	}
}

// This must be its own workflow, triggered on issue_comment alone: folded
// into ci-orchestration.yml's pull_request/push/merge_group triggers, every
// other job there (build, lint, the whole pipeline) would run on every PR
// comment too.
func TestLyditeClearanceTriggersOnlyOnIssueComment(t *testing.T) {
	content := pipelineFiles(t, repospec.Default())[".github/workflows/gt-lydite-clearance.yml"]
	var wf struct {
		On map[string]any `yaml:"on"`
	}
	if err := yaml.Unmarshal(content, &wf); err != nil {
		t.Fatalf("unmarshal gt-lydite-clearance.yml: %v", err)
	}
	if _, ok := wf.On["issue_comment"]; !ok {
		t.Errorf("on = %v, want issue_comment", wf.On)
	}
	for trigger := range wf.On {
		if trigger != "issue_comment" {
			t.Errorf("on carries %q; a comment-triggered workflow must not also run every other job on pull_request/push/merge_group",
				trigger)
		}
	}
}

// Mirrors TestLyditeJobCallsGtsOwnReusableWorkflow: gt's own repo calls its
// local copy so a PR touching the clearance logic exercises it directly,
// every other repo pins the moving major tag.
func TestLyditeClearanceCallsGtsOwnReusableWorkflow(t *testing.T) {
	gtFiles := renderMap(t, repogov.Input{
		Spec: repospec.Default(), RepoOwner: "pedromvgomes", RepoName: "gt", GTVersion: "v0.6.0",
	})
	clearance := workflowJobs(t, gtFiles[".github/workflows/gt-lydite-clearance.yml"])["clearance"]
	if clearance.Uses != "./.github/workflows/reusable-lydite-clearance.yml" {
		t.Errorf("gt's own uses = %q, want the local reusable-lydite-clearance.yml", clearance.Uses)
	}

	otherFiles := pipelineFiles(t, repospec.Default())
	otherClearance := workflowJobs(t, otherFiles[".github/workflows/gt-lydite-clearance.yml"])["clearance"]
	want := "pedromvgomes/gt/.github/workflows/reusable-lydite-clearance.yml@v0"
	if otherClearance.Uses != want {
		t.Errorf("uses = %q, want %q", otherClearance.Uses, want)
	}
}

// dir has to reach the clearance job the same way it reaches the scan job:
// both read .lydite/exemptions.yml from the same root.
func TestLyditeClearanceForwardsDir(t *testing.T) {
	spec := repospec.Default()
	spec.Lydite.Dir = "source"
	jobs := workflowJobs(t, pipelineFiles(t, spec)[".github/workflows/gt-lydite-clearance.yml"])
	clearance, ok := jobs["clearance"]
	if !ok {
		t.Fatal("clearance job was not rendered")
	}
	if got := clearance.With["dir"]; got != "source" {
		t.Errorf("with.dir = %#v, want the spec's lydite.dir", got)
	}

	def := workflowJobs(t, pipelineFiles(t, repospec.Default())[".github/workflows/gt-lydite-clearance.yml"])["clearance"]
	if def.With != nil {
		t.Errorf("with = %v, want no block when lydite.dir is the default", def.With)
	}
}

// The orchestrator calls gt's own reusable workflow, not lydite/actions
// directly. That indirection is what lets gt repoint every governed repository
// at a new lydite pipeline by editing one hand-authored file, instead of
// re-rendering and merging a workflow in each consumer.
func TestLyditeJobCallsGtsOwnReusableWorkflow(t *testing.T) {
	jobs := workflowJobs(t, pipelineFiles(t, repospec.Default())[".github/workflows/ci-orchestration.yml"])
	lydite, ok := jobs["lydite"]
	if !ok {
		t.Fatal("lydite job was not rendered")
	}
	if !strings.Contains(lydite.Uses, "reusable-lydite.yml") {
		t.Errorf("lydite uses = %q, want gt's own reusable-lydite.yml", lydite.Uses)
	}
	if strings.Contains(lydite.Uses, "lydite/actions") {
		t.Errorf("lydite uses = %q, want the call to go through gt so the pipeline can be repointed in one file",
			lydite.Uses)
	}
}

// The `with:` keys have to be the ones reusable-lydite.yml declares: a key it
// does not accept fails the workflow on every governed repository at once.
func TestLyditeJobForwardsTheScanDirAndCoverageGate(t *testing.T) {
	spec := repospec.Default()
	spec.Lydite.Dir = "source"
	spec.Lydite.Coverage = false
	jobs := workflowJobs(t, pipelineFiles(t, spec)[".github/workflows/ci-orchestration.yml"])

	lydite, ok := jobs["lydite"]
	if !ok {
		t.Fatal("lydite job was not rendered")
	}
	if got := lydite.With["dir"]; got != "source" {
		t.Errorf("with.dir = %#v, want the spec's lydite.dir", got)
	}
	if got := lydite.With["coverage"]; got != false {
		t.Errorf("with.coverage = %#v, want false from lydite.coverage", got)
	}

	// With the defaults there is nothing to say: the callee already defaults to
	// the repository root and to running the coverage gate, and a `with:` block
	// restating a default is noise in every rendered file.
	def := workflowJobs(t, pipelineFiles(t, repospec.Default())[".github/workflows/ci-orchestration.yml"])["lydite"]
	if len(def.With) != 0 {
		t.Errorf("with = %#v for the default spec, want nothing restated", def.With)
	}
}

// The outer with: gate has to open on relay alone, not just on dir/coverage —
// a spec that sets only relay must still reach reusable-lydite.yml's relay
// input.
func TestLyditeJobForwardsRelay(t *testing.T) {
	spec := repospec.Default()
	spec.Lydite.Relay = "lydite[bot]"
	jobs := workflowJobs(t, pipelineFiles(t, spec)[".github/workflows/ci-orchestration.yml"])

	lydite, ok := jobs["lydite"]
	if !ok {
		t.Fatal("lydite job was not rendered")
	}
	if got := lydite.With["relay"]; got != "lydite[bot]" {
		t.Errorf("with.relay = %#v, want the spec's lydite.relay", got)
	}
}

// The outer with: gate has to open on relay alone, not just on dir — a spec
// that sets only relay must still reach reusable-lydite-clearance.yml's relay
// input.
func TestLyditeClearanceForwardsRelay(t *testing.T) {
	spec := repospec.Default()
	spec.Lydite.Relay = "lydite[bot]"
	jobs := workflowJobs(t, pipelineFiles(t, spec)[".github/workflows/gt-lydite-clearance.yml"])

	clearance, ok := jobs["clearance"]
	if !ok {
		t.Fatal("clearance job was not rendered")
	}
	if got := clearance.With["relay"]; got != "lydite[bot]" {
		t.Errorf("with.relay = %#v, want the spec's lydite.relay", got)
	}
}

func TestLyditeBaselineOmittedWhenDisabled(t *testing.T) {
	spec := repospec.Default()
	spec.Lydite.Enabled = false
	jobs := workflowJobs(t, pipelineFiles(t, spec)[".github/workflows/ci-orchestration.yml"])
	if _, ok := jobs["lydite-baseline"]; ok {
		t.Error("lydite-baseline was rendered despite lydite being disabled")
	}
}

// A repo with nothing lydite can measure has no coverage gate to feed, so the
// job that records a baseline for one — and holds contents: write to do it —
// has no reason to run either.
func TestLyditeBaselineOmittedWhenCoverageOff(t *testing.T) {
	spec := repospec.Default()
	spec.Lydite.Coverage = false
	jobs := workflowJobs(t, pipelineFiles(t, spec)[".github/workflows/ci-orchestration.yml"])
	if _, ok := jobs["lydite-baseline"]; ok {
		t.Error("lydite-baseline was rendered despite coverage being off")
	}
}

// Mirrors TestLyditeJobCallsGtsOwnReusableWorkflow: gt's own repo calls its
// local copy so a PR touching the baseline logic exercises it directly, every
// other repo pins the moving major tag.
func TestLyditeBaselineCallsGtsOwnReusableWorkflow(t *testing.T) {
	gtFiles := renderMap(t, repogov.Input{
		Spec: repospec.Default(), RepoOwner: "pedromvgomes", RepoName: "gt", GTVersion: "v0.6.0",
	})
	own := workflowJobs(t, gtFiles[".github/workflows/ci-orchestration.yml"])["lydite-baseline"]
	if own.Uses != "./.github/workflows/reusable-lydite-baseline.yml" {
		t.Errorf("gt's own uses = %q, want the local reusable-lydite-baseline.yml", own.Uses)
	}

	other := workflowJobs(t, pipelineFiles(t, repospec.Default())[".github/workflows/ci-orchestration.yml"])["lydite-baseline"]
	want := "pedromvgomes/gt/.github/workflows/reusable-lydite-baseline.yml@v0"
	if other.Uses != want {
		t.Errorf("uses = %q, want %q", other.Uses, want)
	}
}

// Recording a baseline writes back to the repository, so this is the one job
// in the orchestrator granted more than contents: read.
func TestLyditeBaselineCanWriteContents(t *testing.T) {
	jobs := workflowJobs(t, pipelineFiles(t, repospec.Default())[".github/workflows/ci-orchestration.yml"])
	baseline, ok := jobs["lydite-baseline"]
	if !ok {
		t.Fatal("lydite-baseline job was not rendered")
	}
	if got := baseline.Permissions["contents"]; got != "write" {
		t.Errorf("permissions.contents = %q, want write — a baseline the job cannot persist is no baseline",
			got)
	}
}

// dir has to reach the baseline the same way it reaches the scan: the gate
// compares against a baseline measured from the same root.
func TestLyditeBaselineForwardsDir(t *testing.T) {
	spec := repospec.Default()
	spec.Lydite.Dir = "source"
	baseline := workflowJobs(t, pipelineFiles(t, spec)[".github/workflows/ci-orchestration.yml"])["lydite-baseline"]
	if got := baseline.With["dir"]; got != "source" {
		t.Errorf("with.dir = %#v, want the spec's lydite.dir", got)
	}

	def := workflowJobs(t, pipelineFiles(t, repospec.Default())[".github/workflows/ci-orchestration.yml"])["lydite-baseline"]
	if len(def.With) != 0 {
		t.Errorf("with = %#v for the default spec, want nothing restated", def.With)
	}
}

// A baseline records what is on the default branch. The push trigger is
// already filtered to that branch and no other trigger reports as a push, so
// the event name alone is the whole condition.
func TestLyditeBaselineRunsOnlyOnPush(t *testing.T) {
	baseline := workflowJobs(t, pipelineFiles(t, repospec.Default())[".github/workflows/ci-orchestration.yml"])["lydite-baseline"]
	if baseline.If != "github.event_name == 'push'" {
		t.Errorf("if = %q, want the push-only guard", baseline.If)
	}
}

// The baseline waits on attest, not on lydite. A referral verdict gating the
// record would leave the default branch without a baseline exactly when a
// referral is outstanding — the state the record exists to avoid.
func TestLyditeBaselineDoesNotWaitOnTheReferral(t *testing.T) {
	baseline := workflowJobs(t, pipelineFiles(t, repospec.Default())[".github/workflows/ci-orchestration.yml"])["lydite-baseline"]
	if !reflect.DeepEqual(baseline.Needs, []string{"attest"}) {
		t.Errorf("lydite-baseline needs = %v, want [attest] alone", baseline.Needs)
	}
}

// On a push to the default branch there is nothing left to merge, so the gate
// is informational there. A baseline that failed to record must not turn it
// red on a commit already on main.
func TestLyditeBaselineIsNotRequiredByTheGate(t *testing.T) {
	jobs := workflowJobs(t, pipelineFiles(t, repospec.Default())[".github/workflows/ci-orchestration.yml"])
	for _, n := range jobs[repospec.GateCheckJob].Needs {
		if n == "lydite-baseline" {
			t.Errorf("%s waits on lydite-baseline: %v", repospec.GateCheckJob, jobs[repospec.GateCheckJob].Needs)
		}
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

// merge_group is what lets a queued PR report its required check, and it is
// rendered unconditionally — including where the queue is switched off.
//
// The order is the point. `gt repo settings apply` will not add the merge_queue
// rule until this trigger is already on the default branch, so the trigger has
// to arrive first and cannot be gated on the setting that comes second. An
// unused trigger on a repository with no queue costs nothing; a missing one on
// a repository with a queue hangs every pull request on a check that never
// reports.
func TestMergeGroupTriggerAlwaysRendered(t *testing.T) {
	for _, queued := range []bool{false, true} {
		spec := repospec.Default()
		spec.Settings.BranchProtection.BaseFreshness = repospec.FreshnessQueue
		if !queued {
			spec.Settings.BranchProtection.BaseFreshness = repospec.FreshnessStrict
		}

		content := string(pipelineFiles(t, spec)[".github/workflows/ci-orchestration.yml"])
		if !strings.Contains(content, "merge_group:") {
			t.Errorf("base_freshness queue = %v: no merge_group trigger rendered", queued)
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
		{"permissions for a stage that is not enabled", func(s *repospec.Spec) {
			s.Pipeline.CI.Stages = []string{"preflight", "build"}
			s.Pipeline.CI.StagePermissions = repospec.StagePermissions{
				"end2end": {"packages": "write"},
			}
		}, "is not enabled"},
		{"unknown permission scope", func(s *repospec.Spec) {
			s.Pipeline.CI.StagePermissions = repospec.StagePermissions{
				"build": {"code-scanning": "write"},
			}
		}, "unknown permission scope"},
		{"invalid permission level", func(s *repospec.Spec) {
			s.Pipeline.CI.StagePermissions = repospec.StagePermissions{
				"build": {"security-events": "readwrite"},
			}
		}, "invalid level"},
		{"permissions with an empty scope map", func(s *repospec.Spec) {
			s.Pipeline.CI.StagePermissions = repospec.StagePermissions{"build": {}}
		}, "no permissions listed"},
		{"permissions while ci is disabled", func(s *repospec.Spec) {
			s.Pipeline.CI.Enabled = false
			s.Pipeline.CI.StagePermissions = repospec.StagePermissions{
				"build": {"security-events": "write"},
			}
		}, "pipeline is disabled"},
		{"independent stage that is not enabled", func(s *repospec.Spec) {
			s.Pipeline.CI.Stages = []string{"preflight", "build"}
			s.Pipeline.CI.IndependentStages = []string{"end2end"}
		}, "is not enabled"},
		{"independent preflight", func(s *repospec.Spec) {
			s.Pipeline.CI.IndependentStages = []string{"preflight"}
		}, "no stage dependency to drop"},
		{"duplicate independent stage", func(s *repospec.Spec) {
			s.Pipeline.CI.IndependentStages = []string{"end2end", "end2end"}
		}, "duplicate stage"},
		{"independent stages while ci is disabled", func(s *repospec.Spec) {
			s.Pipeline.CI.Enabled = false
			s.Pipeline.CI.IndependentStages = []string{"end2end"}
		}, "pipeline is disabled"},
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
		".github/workflows/gt-lydite-clearance.yml",
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

// attest being skipped must not take lydite down with it: GitHub skips a job
// whose needs were skipped, and ci-gate counts skipped as a pass — so the
// security gate would silently vanish while the required check stayed green.
// lydite needs only attest now, not a test stage — there is nothing else it
// could be dragged down by.
func TestLyditeSurvivesASkippedDependency(t *testing.T) {
	jobs := workflowJobs(t, pipelineFiles(t, repospec.Default())[".github/workflows/ci-orchestration.yml"])
	lydite, ok := jobs["lydite"]
	if !ok {
		t.Fatal("lydite job was not rendered")
	}
	if !strings.Contains(lydite.If, "!cancelled()") {
		t.Errorf("lydite if = %q, want it to survive a skipped dependency", lydite.If)
	}
}

// With CI disabled nothing renders ci-orchestration.yml, so requiring the gate
// context would block every PR on a check that can never report.
func TestBranchProtectionOnlyRequiresTheGateWhenCIIsEnabled(t *testing.T) {
	gh := alignedGH(t)
	spec := repospec.Default()
	spec.Pipeline.CI.Enabled = false

	changes, err := repogov.SettingsDiff(context.Background(), gh, spec, "pedromvgomes", "demo")
	if err != nil {
		t.Fatalf("SettingsDiff() error = %v", err)
	}
	// Removing the rule is right; asking for the gate is not.
	for _, c := range changes {
		if strings.Contains(c.Want, repospec.GateCheckJob) {
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

// gt scaffolds no lydite configuration at all. lydite runs its own suites and
// reads its own `.lydite/` config from the scan root, so a gt-written file
// here would be configuration a repository carries and nobody can explain.
//
// Asserted across every combination the spec can produce, so a conditional
// scaffold cannot slip back in for only the combinations that hit its guard.
func TestLyditeConfigIsNeverScaffolded(t *testing.T) {
	for name, mutate := range map[string]func(*repospec.Spec){
		"defaults":        func(*repospec.Spec) {},
		"no test stage":   func(s *repospec.Spec) { s.Pipeline.CI.Stages = []string{"preflight", "build"} },
		"lydite disabled": func(s *repospec.Spec) { s.Lydite.Enabled = false },
		"ci disabled":     func(s *repospec.Spec) { s.Pipeline.CI.Enabled = false },
		"coverage off":    func(s *repospec.Spec) { s.Lydite.Coverage = false },
		"coverage off, no test": func(s *repospec.Spec) {
			s.Lydite.Coverage = false
			s.Pipeline.CI.Stages = []string{"preflight", "build"}
		},
		"scan dir": func(s *repospec.Spec) { s.Lydite.Dir = "source" },
	} {
		t.Run(name, func(t *testing.T) {
			spec := repospec.Default()
			mutate(&spec)
			files, err := repogov.Render(testInput(spec))
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			for _, f := range files {
				if strings.HasSuffix(f.Path, ".bulwark.yml") {
					t.Errorf("%s was scaffolded; lydite reads its own config from the scan root", f.Path)
				}
			}
		})
	}
}

// reusable-lydite.yml declares no `secrets:` input, and a caller passing a
// secret a reusable workflow does not accept is an error actionlint reports on
// every rendered consumer at once.
//
// The repo-owned stages keep `inherit`, and correctly: those are local `./…`
// calls inside the same repository, where inherit is the whole point.
func TestLyditeJobForwardsNoSecrets(t *testing.T) {
	jobs := workflowJobs(t, pipelineFiles(t, repospec.Default())[".github/workflows/ci-orchestration.yml"])

	lydite, ok := jobs["lydite"]
	if !ok {
		t.Fatal("no lydite job rendered")
	}
	if lydite.Secrets != nil {
		t.Errorf("lydite secrets = %#v, want none — its callee accepts no secrets", lydite.Secrets)
	}

	// A local stage is a different case and must keep inheriting.
	if build, ok := jobs["build"]; ok {
		if build.Secrets != "inherit" {
			t.Errorf("build stage secrets = %#v, want inherit for a same-repo call", build.Secrets)
		}
	}
}

// attest leaves a note on the pull request when it skips the pipeline, which
// needs write access. A skipped job looks the same whether the work was
// unnecessary or something broke, and that ambiguity lands on a reviewer.
//
// The permission is easy to lose to a well-meaning tightening, and losing it
// fails silently: the note is best-effort, so the pipeline stays green and the
// explanation just stops appearing.
func TestAttestCanCommentOnThePullRequest(t *testing.T) {
	content := pipelineFiles(t, repospec.Default())[".github/workflows/ci-orchestration.yml"]

	var wf struct {
		Jobs map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(content, &wf); err != nil {
		t.Fatalf("unmarshal ci-orchestration.yml: %v", err)
	}
	got := wf.Jobs["attest"].Permissions["pull-requests"]
	if got != "write" {
		t.Errorf("attest pull-requests = %q, want write — it cannot explain a skip without it", got)
	}
}

// Without target_url the skip note can only assert that an earlier run
// validated the tree; with it, the reader can go and look.
func TestGateRecordsTheRunThatValidatedTheTree(t *testing.T) {
	content := string(pipelineFiles(t, repospec.Default())[".github/workflows/ci-orchestration.yml"])
	if !strings.Contains(content, "target_url=") {
		t.Error("ci-gate does not record target_url on the attestation, so a skip cannot link the run that earned it")
	}
	if !strings.Contains(content, "actions/runs/") {
		t.Error("target_url does not point at the run")
	}
}

// lydite keys its coverage baseline by TREE: a pull request records its own
// measurement, and a squash merge lands a commit carrying that same tree, so
// the number is already there when main needs it. Forcing the coverage chain
// to run again on every push to manufacture a merge-base baseline would buy
// nothing.
//
// So no stage is exempt from the attestation: an already-validated push skips
// every one of them, which is what attest is for. lydite-baseline is not a
// stage — it exists only on a push to the default branch and re-validates
// nothing, so its push condition is its whole trigger rather than an exemption
// from one.
func TestAttestSkipsEveryStageOnAnAlreadyValidatedPush(t *testing.T) {
	jobs := workflowJobs(t, pipelineFiles(t, repospec.Default())[".github/workflows/ci-orchestration.yml"])

	for name, job := range jobs {
		if name == "attest" || name == repospec.GateCheckJob || name == "lydite-baseline" {
			continue
		}
		if strings.Contains(job.If, "github.event_name == 'push'") {
			t.Errorf("%s still carries the push exemption (%q); lydite keys baselines by tree, "+
				"so forcing it to run on every merge buys nothing", name, job.If)
		}
	}
}

// A repository with nothing lydite can measure turns the coverage gate off.
// Left on, it resolves a baseline for languages that do not exist and reports a
// number that means nothing — and a number that means nothing is one people
// stop reading, which costs more than the minutes it wastes.
func TestLyditeCoverageCanBeTurnedOff(t *testing.T) {
	spec := repospec.Default()
	spec.Lydite.Coverage = false
	content := string(pipelineFiles(t, spec)[".github/workflows/ci-orchestration.yml"])
	if !strings.Contains(content, "coverage: false") {
		t.Errorf("coverage was not disabled in the lydite stage:\n%s", content)
	}
}

// On by default, and NOT passed explicitly when on: the reusable workflow
// already defaults to true, and a `with:` block that exists only to restate a
// default is noise in every rendered file.
func TestLyditeCoverageOnByDefault(t *testing.T) {
	if !repospec.Default().Lydite.Coverage {
		t.Fatal("coverage defaults to off; a repo would silently lose its gate on sync")
	}
	content := string(pipelineFiles(t, repospec.Default())[".github/workflows/ci-orchestration.yml"])
	if strings.Contains(content, "coverage: false") {
		t.Error("coverage disabled with the default spec")
	}
}

// stagePermissions reads the rendered `permissions:` block of one orchestrator
// job, which is the grant its called stage workflow can narrow from.
func stagePermissions(t *testing.T, content []byte, job string) map[string]string {
	t.Helper()
	var wf struct {
		Jobs map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(content, &wf); err != nil {
		t.Fatalf("unmarshal workflow: %v\n%s", err, content)
	}
	j, ok := wf.Jobs[job]
	if !ok {
		t.Fatalf("no %q job in rendered workflow:\n%s", job, content)
	}
	return j.Permissions
}

// The baseline is the contract every existing repo already renders. A change to
// stage permissions that shifted it would rewrite all seventeen governed repos
// on their next sync, so pin it.
func TestStagesDefaultToTheBaselinePermissions(t *testing.T) {
	files := pipelineFiles(t, repospec.Default())

	for _, stage := range repospec.CIStages {
		got := stagePermissions(t, files[".github/workflows/ci-orchestration.yml"], stage)
		want := map[string]string{"contents": "read", "packages": "read"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ci stage %q permissions = %v, want %v", stage, got, want)
		}
	}
	for _, stage := range repospec.CDStages {
		got := stagePermissions(t, files[".github/workflows/cd-orchestration.yml"], stage)
		want := map[string]string{"contents": "write", "packages": "write"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("cd stage %q permissions = %v, want %v", stage, got, want)
		}
	}
}

// The reason this feature exists: a leaf can only narrow what its caller
// grants, so a stage hosting a job that uploads SARIF or pushes a build cache
// needs the calling job widened. Without this the capability is lost silently.
func TestStagePermissionsWidenOnlyTheNamedStage(t *testing.T) {
	spec := repospec.Default()
	spec.Pipeline.CI.StagePermissions = repospec.StagePermissions{
		"build":   {"security-events": "write"},
		"end2end": {"packages": "write"},
	}

	content := pipelineFiles(t, spec)[".github/workflows/ci-orchestration.yml"]

	// A scope the baseline withholds entirely is added.
	build := stagePermissions(t, content, "build")
	wantBuild := map[string]string{
		"contents": "read", "packages": "read", "security-events": "write",
	}
	if !reflect.DeepEqual(build, wantBuild) {
		t.Errorf("build permissions = %v, want %v", build, wantBuild)
	}

	// A scope the baseline already sets is raised, not duplicated.
	end2end := stagePermissions(t, content, "end2end")
	wantEnd2End := map[string]string{"contents": "read", "packages": "write"}
	if !reflect.DeepEqual(end2end, wantEnd2End) {
		t.Errorf("end2end permissions = %v, want %v", end2end, wantEnd2End)
	}

	// Every other stage stays exactly where it was. A grant that leaked across
	// stages would hand write scopes to jobs nobody reviewed for them.
	for _, stage := range []string{"preflight", "test"} {
		got := stagePermissions(t, content, stage)
		want := map[string]string{"contents": "read", "packages": "read"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("stage %q permissions = %v, want the untouched baseline %v", stage, got, want)
		}
	}
}

// CD carries the same mechanism, so a delivery stage needing OIDC does not
// become a second gt change.
func TestStagePermissionsApplyToCD(t *testing.T) {
	spec := repospec.Default()
	spec.Pipeline.CD.Enabled = true
	spec.Pipeline.CD.StagePermissions = repospec.StagePermissions{
		"publish": {"id-token": "write"},
	}

	content := pipelineFiles(t, spec)[".github/workflows/cd-orchestration.yml"]
	got := stagePermissions(t, content, "publish")
	want := map[string]string{"contents": "write", "packages": "write", "id-token": "write"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("publish permissions = %v, want %v", got, want)
	}
}

// Map iteration in Go is randomised, so an unsorted render would produce a
// spurious diff on every sync and make `gt repo check` flap.
func TestStagePermissionsRenderDeterministically(t *testing.T) {
	spec := repospec.Default()
	spec.Pipeline.CI.StagePermissions = repospec.StagePermissions{
		"build": {
			"security-events": "write",
			"id-token":        "write",
			"attestations":    "write",
			"checks":          "write",
		},
	}

	first := pipelineFiles(t, spec)[".github/workflows/ci-orchestration.yml"]
	for i := range 20 {
		again := pipelineFiles(t, spec)[".github/workflows/ci-orchestration.yml"]
		if !bytes.Equal(first, again) {
			t.Fatalf("render %d differs from the first:\n%s\n---\n%s", i, first, again)
		}
	}
}

// The reason this exists: wardnet's end2end suite rebuilds the daemon from
// source and consumes nothing build produces, so waiting on build added ~11
// minutes to every daemon PR to prove an edge that does not exist.
func TestIndependentStageDropsSiblingDependenciesButNotPreflight(t *testing.T) {
	spec := repospec.Default()
	spec.Pipeline.CI.IndependentStages = []string{"end2end"}

	jobs := workflowJobs(t, pipelineFiles(t, spec)[".github/workflows/ci-orchestration.yml"])

	e2e, ok := jobs["end2end"]
	if !ok {
		t.Fatalf("no end2end job; jobs = %v", jobs)
	}
	want := []string{"attest", "preflight"}
	if !reflect.DeepEqual(e2e.Needs, want) {
		t.Errorf("end2end needs = %v, want %v", e2e.Needs, want)
	}

	// preflight must survive, or the gating condition references the outputs
	// of a job this one does not wait for and is never true.
	if !strings.Contains(e2e.If, "needs.preflight.outputs.run-end2end") {
		t.Errorf("end2end lost its preflight gate; if = %q", e2e.If)
	}

	// Independence is per-stage: test still consumes build's artefacts.
	if test, ok := jobs["test"]; !ok {
		t.Error("no test job")
	} else if !contains(test.Needs, "build") {
		t.Errorf("test needs = %v, want it to still wait on build", test.Needs)
	}
}

// Detaching a stage must not drop it from the gate. If it did, a failing
// end2end would stop blocking the merge — the exact defect branch protection
// exists to prevent, arriving as a side effect of a performance tweak.
func TestIndependentStageIsStillGated(t *testing.T) {
	spec := repospec.Default()
	spec.Pipeline.CI.IndependentStages = []string{"end2end"}

	jobs := workflowJobs(t, pipelineFiles(t, spec)[".github/workflows/ci-orchestration.yml"])
	gate, ok := jobs[repospec.GateCheckJob]
	if !ok {
		t.Fatalf("no %s job", repospec.GateCheckJob)
	}
	if !contains(gate.Needs, "end2end") {
		t.Errorf("%s needs = %v, want end2end among them", repospec.GateCheckJob, gate.Needs)
	}
}

// Declaring nothing must render exactly what it rendered before, so this
// cannot quietly reshape the seventeen repos that do not use it.
func TestStagesKeepTheirDependenciesByDefault(t *testing.T) {
	jobs := workflowJobs(t, pipelineFiles(t, repospec.Default())[".github/workflows/ci-orchestration.yml"])

	for _, tc := range []struct{ stage, dep string }{
		{"test", "build"},
		{"end2end", "build"},
		{"build", "preflight"},
	} {
		j, ok := jobs[tc.stage]
		if !ok {
			t.Fatalf("no %s job", tc.stage)
		}
		if !contains(j.Needs, tc.dep) {
			t.Errorf("%s needs = %v, want %q among them", tc.stage, j.Needs, tc.dep)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// A grant may only raise a scope. Honouring a lowering would render the
// "Widened beyond the baseline" comment directly above a scope that had just
// been taken away — telling a reviewer the opposite of what happened — and
// would strip `contents` from a delivery stage, breaking checkout.
func TestStagePermissionsRejectNarrowingTheBaseline(t *testing.T) {
	spec := repospec.Default()
	spec.Pipeline.CD.Enabled = true
	spec.Pipeline.CD.StagePermissions = repospec.StagePermissions{
		"deploy": {"contents": "none"},
	}

	if _, err := repogov.Render(testInput(spec)); err == nil {
		t.Fatal("Render() = nil error, want a refusal to narrow the baseline")
	} else if !strings.Contains(err.Error(), "narrower than") {
		t.Fatalf("Render() error = %v, want it to say the grant is narrower", err)
	}
}

// Widened must mean widened. A grant that restates a baseline value changes
// nothing, so the stage must not advertise itself as widened.
func TestStagePermissionsRestatingTheBaselineIsNotWidening(t *testing.T) {
	spec := repospec.Default()
	spec.Pipeline.CI.StagePermissions = repospec.StagePermissions{
		"build": {"contents": "read", "packages": "read"},
	}

	content := pipelineFiles(t, spec)[".github/workflows/ci-orchestration.yml"]
	i := strings.Index(string(content), "  build:")
	if i < 0 {
		t.Fatal("no build job")
	}
	seg := string(content)[i : i+1200]
	if strings.Contains(seg, "Widened beyond") {
		t.Errorf("a grant restating the baseline reported itself as widened:\n%s", seg)
	}
}

// `models` is a real GITHUB_TOKEN scope. The vocabulary is an allowlist, so
// omitting one makes the feature unusable for that scope rather than merely
// unvalidated.
func TestModelsIsAnAcceptedScope(t *testing.T) {
	spec := repospec.Default()
	spec.Pipeline.CI.StagePermissions = repospec.StagePermissions{
		"test": {"models": "read"},
	}
	if err := repospec.Validate(spec); err != nil {
		t.Fatalf("Validate() rejected the models scope: %v", err)
	}
}

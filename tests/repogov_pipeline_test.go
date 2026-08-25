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
	Secrets any `yaml:"secrets"`
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

// bulwark decides who produces coverage from .bulwark.yml, not from an action
// input — those were removed when the setting moved into the file. gt
// scaffolds the file so the choice is written down rather than inherited.
//
// It must scaffold `run`, not `report`. `report` says a report already exists,
// and at the moment this file is created ci-test is a no-op stub that uploads
// nothing — bulwark then exits with an error rather than shrugging at the
// missing file. Onboarding gt itself proved that: the bulwark stage failed on
// `open : no such file or directory` on a pipeline where every stage was a
// stub. The repository flips it to `report` in the commit that makes ci-test
// actually produce coverage.
func TestBulwarkConfigScaffoldedWhenTestsProduceCoverage(t *testing.T) {
	files, err := repogov.Render(testInput(repospec.Default()))
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	var found *repogov.File
	for i := range files {
		if files[i].Path == ".bulwark.yml" {
			found = &files[i]
		}
	}
	if found == nil {
		t.Fatal(".bulwark.yml was not scaffolded")
	}
	if found.Mode != repogov.ModeScaffold {
		t.Errorf("mode = %q, want scaffold — its contents are bulwark's, not gt's", found.Mode)
	}
	if !strings.Contains(string(found.Content), "source: run") {
		t.Errorf("scaffold does not set coverage.source: run:\n%s", found.Content)
	}
	// Guards the direction specifically: `report` here fails the security gate
	// on every repo whose ci-test is still a scaffold, which is all of them on
	// the day they onboard.
	if strings.Contains(string(found.Content), "source: report") {
		t.Errorf("scaffold sets coverage.source: report, which fails until ci-test uploads coverage:\n%s", found.Content)
	}
}

// With no test stage nothing produces a report, so declaring `report` would
// tell bulwark to look for something that is never made.
func TestBulwarkConfigNotScaffoldedWithoutATestStage(t *testing.T) {
	for name, mutate := range map[string]func(*repospec.Spec){
		"no test stage":    func(s *repospec.Spec) { s.Pipeline.CI.Stages = []string{"preflight", "build"} },
		"bulwark disabled": func(s *repospec.Spec) { s.Bulwark.Enabled = false },
		"ci disabled":      func(s *repospec.Spec) { s.Pipeline.CI.Enabled = false },
	} {
		t.Run(name, func(t *testing.T) {
			spec := repospec.Default()
			mutate(&spec)
			if _, ok := pipelineFiles(t, spec)[".bulwark.yml"]; ok {
				t.Error(".bulwark.yml was scaffolded when nothing produces a report")
			}
		})
	}
}

// bulwark reads the file from its scan root, so a repo scanning a subdirectory
// needs it there rather than at the repository root.
func TestBulwarkConfigFollowsTheScanDir(t *testing.T) {
	spec := repospec.Default()
	spec.Bulwark.Dir = "source"
	if _, ok := pipelineFiles(t, spec)["source/.bulwark.yml"]; !ok {
		t.Error("expected source/.bulwark.yml for a repo scanning a subdirectory")
	}
}

// The bulwark stage must name its secrets rather than inherit them.
//
// GitHub documents `secrets: inherit` as working for reusable workflows "in the
// same organization or enterprise", and gt lives under a different owner than
// most repositories that call it. With inherit, an organization secret never
// arrived: bulwark skipped its Codecov upload, because that step is guarded on
// a non-empty token, and fell back to token-less semgrep. Nothing failed — the
// gate stayed green while coverage history quietly stopped being recorded,
// which is the worst shape a regression can take.
//
// The repo-owned stages keep `inherit`, and correctly: those are local `./…`
// calls inside the same repository, where inherit is the whole point.
func TestBulwarkNamesItsSecretsInsteadOfInheriting(t *testing.T) {
	jobs := workflowJobs(t, pipelineFiles(t, repospec.Default())[".github/workflows/ci-orchestration.yml"])

	bulwark, ok := jobs["bulwark"]
	if !ok {
		t.Fatal("no bulwark job rendered")
	}
	if bulwark.Secrets == "inherit" {
		t.Fatal("bulwark inherits secrets; an org secret will not reach gt across owners")
	}
	named, ok := bulwark.Secrets.(map[string]any)
	if !ok {
		t.Fatalf("bulwark secrets = %#v, want a map of named secrets", bulwark.Secrets)
	}
	for _, want := range []string{"CODECOV_TOKEN", "SEMGREP_APP_TOKEN"} {
		v, present := named[want]
		if !present {
			t.Errorf("bulwark does not pass %s; the feature it enables silently stops working", want)
			continue
		}
		// It must forward the caller's value, not a literal.
		if s, _ := v.(string); !strings.Contains(s, "secrets."+want) {
			t.Errorf("%s = %q, want it to forward ${{ secrets.%s }}", want, s, want)
		}
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

// bulwark takes its coverage baseline from a run on the merge-base, so gt used
// to force the whole coverage chain to run on every push to manufacture one.
// bulwark v1.8.0 keys baselines by TREE instead: a pull request records its own
// measurement, and a squash merge lands a commit carrying that same tree, so the
// number is already there when main needs it.
//
// Verified on gt's own bulwark-state before this was reverted — the six most
// recent main commits all resolve to tree-keyed entries reading ~76%, where the
// commit-keyed entry they replaced read 0.7%.
//
// So nothing is exempt from the attestation any more: an already-validated push
// skips every stage, which is what attest was for.
func TestAttestSkipsEveryStageOnAnAlreadyValidatedPush(t *testing.T) {
	jobs := workflowJobs(t, pipelineFiles(t, repospec.Default())[".github/workflows/ci-orchestration.yml"])

	for name, job := range jobs {
		if name == "attest" || name == repospec.GateCheckJob {
			continue
		}
		if strings.Contains(job.If, "github.event_name == 'push'") {
			t.Errorf("%s still carries the push exemption (%q); bulwark keys baselines by tree now, "+
				"so forcing it to run on every merge buys nothing", name, job.If)
		}
	}
}

// A repository with nothing bulwark can measure turns the coverage gate off.
// Left on, it resolves a baseline for languages that do not exist and reports a
// number that means nothing — and a number that means nothing is one people
// stop reading, which costs more than the minutes it wastes.
func TestBulwarkCoverageCanBeTurnedOff(t *testing.T) {
	spec := repospec.Default()
	spec.Bulwark.Coverage = false
	content := string(pipelineFiles(t, spec)[".github/workflows/ci-orchestration.yml"])
	if !strings.Contains(content, "coverage: false") {
		t.Errorf("coverage was not disabled in the bulwark stage:\n%s", content)
	}
}

// On by default, and NOT passed explicitly when on: the reusable workflow
// already defaults to true, and a `with:` block that exists only to restate a
// default is noise in every rendered file.
func TestBulwarkCoverageOnByDefault(t *testing.T) {
	if !repospec.Default().Bulwark.Coverage {
		t.Fatal("coverage defaults to off; a repo would silently lose its gate on sync")
	}
	content := string(pipelineFiles(t, repospec.Default())[".github/workflows/ci-orchestration.yml"])
	if strings.Contains(content, "coverage: false") {
		t.Error("coverage disabled with the default spec")
	}
}

// .bulwark.yml exists to declare where coverage comes from. With the coverage
// gate off there is no such question, and scaffolding a file to answer it is
// how a repository ends up carrying configuration nobody can explain.
func TestBulwarkConfigNotScaffoldedWhenCoverageIsOff(t *testing.T) {
	spec := repospec.Default()
	spec.Bulwark.Coverage = false
	if _, ok := pipelineFiles(t, spec)[".bulwark.yml"]; ok {
		t.Error(".bulwark.yml was scaffolded for a repo with the coverage gate off")
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

package repogov

import (
	"fmt"
	"strings"

	"github.com/pedromvgomes/gt/internal/repospec"
)

// CoverageArtifact is the name ci-test uploads coverage under and the bulwark
// stage looks for. A convention rather than configuration: it is the entire
// contract that stops bulwark re-running a suite the repo already ran.
const CoverageArtifact = "gt-coverage"

// AttestContext is the commit-status context carrying the validated tree SHA.
const AttestContext = "gt/validated-tree"

// attestGuard skips a stage when the tree already carries a passing
// attestation, so re-running it would test identical content twice.
const attestGuard = "needs.attest.outputs.validated != 'true'"

// baselineGuard is attestGuard with an exemption for pushes to the default
// branch, and it exists because bulwark's coverage gate cannot work without it.
//
// bulwark takes its baseline from a run ON the merge-base — "the current commit
// *is* the baseline — so record what was just measured and stop". That path is
// the whole reason `coverage.source: report` is usable: a repository whose
// coverage comes from a multi-job pipeline can never reconstruct a historical
// baseline, because bulwark's fallback measures a bare worktree with none of
// the toolchain or staged reports the pipeline provides.
//
// Skipping the pipeline on an already-validated push removed that run, so every
// baseline came from the fallback instead. On gt that meant main measured at
// 0.7% against pull requests measured at 76.2% — two numbers produced by
// different methods, compared to each other. A gate nothing can fail.
//
// So the stages that produce and consume coverage always run on a push, and
// the attestation only skips them on a pull request. end2end is exempt from the
// exemption: it contributes no coverage, and it is the expensive one.
const baselineGuard = "(" + attestGuard + " || github.event_name == 'push')"

// coverageChain is the stages a push to the default branch must run whatever
// the attestation says, because bulwark records its baseline from that run.
//
// It is the dependency chain down to the coverage producer, not a taste: a job
// whose `needs` was skipped is skipped too, so test cannot run on its own —
// preflight and build have to come with it.
var coverageChain = map[string]bool{
	"preflight": true,
	"build":     true,
	"test":      true,
}

// stageGuard returns the condition for one stage. Only the CI stages that feed
// the coverage baseline get the push exemption; end2end and every CD stage keep
// the plain attestation guard, which is where the saving now comes from.
func stageGuard(name, guard string) string {
	if guard == attestGuard && coverageChain[name] {
		return baselineGuard
	}
	return guard
}

// stageJob is one rendered orchestrator job.
type stageJob struct {
	Name string
	// Needs is the comma-joined `needs:` list.
	Needs string
	// If is the job's condition, already combined.
	If string
}

// dependsOn describes how a stage is wired, given which stages exist.
//
// The graph is fixed on purpose: a central pipeline whose order every
// repository can rearrange is not a central pipeline. What varies is only
// which stages are present, so a missing stage collapses out of the `needs:`
// of the ones after it rather than breaking them.
type stageWiring struct {
	// after are the stages this one runs after, most-specific first. Only
	// those actually enabled are kept.
	after []string
	// gated marks stages preflight can skip via a run-<stage> output.
	gated bool
}

var ciWiring = map[string]stageWiring{
	"preflight": {},
	"build":     {after: []string{"preflight"}, gated: true},
	"test":      {after: []string{"build", "preflight"}, gated: true},
	"end2end":   {after: []string{"build", "preflight"}, gated: true},
}

var cdWiring = map[string]stageWiring{
	"preflight": {},
	"publish":   {after: []string{"preflight"}, gated: true},
	"deploy":    {after: []string{"publish", "preflight"}, gated: true},
	"verify":    {after: []string{"deploy", "publish", "preflight"}, gated: true},
}

// buildStages resolves the enabled stages into rendered jobs.
//
// root is the job every stage ultimately depends on — `attest` for CI,
// `verify-attestation` for CD — so nothing runs before the pipeline has
// decided whether it needs to run at all.
func buildStages(
	enabled []string, order []string, wiring map[string]stageWiring, root, guard string,
) ([]stageJob, error) {
	present := map[string]bool{}
	for _, s := range enabled {
		present[s] = true
	}

	hasPreflight := present["preflight"]
	jobs := make([]stageJob, 0, len(enabled))

	// Iterate the canonical order, not the user's, so the rendered file reads
	// the same regardless of how the list was written.
	for _, name := range order {
		if !present[name] {
			continue
		}
		w, ok := wiring[name]
		if !ok {
			return nil, fmt.Errorf("no wiring defined for stage %q", name)
		}

		needs := []string{root}
		for _, dep := range w.after {
			if present[dep] {
				needs = append(needs, dep)
			}
		}

		var conds []string
		if g := stageGuard(name, guard); g != "" {
			conds = append(conds, g)
		}
		// Only gate on preflight's output when preflight actually exists;
		// referencing outputs of an absent job would never be true.
		if w.gated && hasPreflight {
			conds = append(conds, fmt.Sprintf("needs.preflight.outputs.run-%s != 'false'", name))
		}

		jobs = append(jobs, stageJob{
			Name:  name,
			Needs: strings.Join(needs, ", "),
			If:    strings.Join(conds, " && "),
		})
	}
	return jobs, nil
}

// gateNeeds is everything the aggregating job waits on: every stage plus the
// fixed jobs. Missing one would let a failure through unnoticed, which is the
// failure mode branch protection exists to prevent.
func gateNeeds(root string, stages []stageJob, fixed ...string) string {
	names := []string{root}
	for _, s := range stages {
		names = append(names, s.Name)
	}
	names = append(names, fixed...)
	return strings.Join(names, ", ")
}

// pipelineScaffolds returns the scaffold specs for the enabled stages.
func pipelineScaffolds(spec repospec.Spec) []fileSpec {
	var out []fileSpec

	add := func(prefix, stage string, gated []string) {
		path := fmt.Sprintf("%s/%s-%s.yml", WorkflowDir, prefix, stage)
		tmpl := "templates/scaffolds/stage.yml.tmpl"
		switch {
		case stage == "preflight":
			tmpl = "templates/scaffolds/preflight.yml.tmpl"
		case prefix == "ci" && stage == "test":
			tmpl = "templates/scaffolds/test.yml.tmpl"
		}
		out = append(out, fileSpec{
			key:      prefix + "-" + stage,
			tmpl:     tmpl,
			path:     path,
			workflow: true,
			mode:     ModeScaffold,
			wanted:   func(repospec.Spec) bool { return true },
			data: func(Input, templateData) (any, error) {
				return scaffoldData{Prefix: prefix, Stage: stage, Gated: gated}, nil
			},
		})
	}

	if spec.Pipeline.CI.Enabled {
		gated := gatedStages(spec.Pipeline.CI.Stages, ciWiring)
		for _, st := range spec.Pipeline.CI.Stages {
			add("ci", st, gated)
		}
	}
	// bulwark reads .bulwark.yml from its scan root. Scaffolded rather than
	// managed: its contents are bulwark's business, but coverage.source has to
	// say `report` or bulwark re-runs the suite ci-test already ran — and the
	// default is `run`, so forgetting the file is silently expensive rather
	// than loudly broken.
	if spec.Bulwark.Enabled && spec.Pipeline.CI.Enabled && contains(spec.Pipeline.CI.Stages, "test") {
		path := ".bulwark.yml"
		if spec.Bulwark.Dir != "" {
			path = spec.Bulwark.Dir + "/.bulwark.yml"
		}
		out = append(out, fileSpec{
			key:    "bulwark-config",
			tmpl:   "templates/scaffolds/bulwark-config.yml.tmpl",
			path:   path,
			mode:   ModeScaffold,
			wanted: func(repospec.Spec) bool { return true },
			data:   func(Input, templateData) (any, error) { return struct{}{}, nil },
		})
	}

	if spec.Pipeline.CD.Enabled {
		gated := gatedStages(spec.Pipeline.CD.Stages, cdWiring)
		for _, st := range spec.Pipeline.CD.Stages {
			add("cd", st, gated)
		}
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// gatedStages lists the enabled stages preflight can skip, which is what its
// scaffold declares as outputs.
func gatedStages(enabled []string, wiring map[string]stageWiring) []string {
	var out []string
	for _, s := range enabled {
		if w, ok := wiring[s]; ok && w.gated {
			out = append(out, s)
		}
	}
	return out
}

// scaffoldData is the template input for a stage stub.
type scaffoldData struct {
	Prefix string
	Stage  string
	Gated  []string
}

// Name is the workflow name, e.g. ci-build.
func (s scaffoldData) Name() string { return s.Prefix + "-" + s.Stage }

// purpose is the phrase the stub uses to describe what belongs in it.
var stagePurpose = map[string]string{
	"build":   "build steps",
	"test":    "test suite",
	"end2end": "end-to-end tests",
	"publish": "artifact publishing",
	"deploy":  "deployment steps",
	"verify":  "post-deployment checks",
}

func (s scaffoldData) Purpose() string {
	if p, ok := stagePurpose[s.Stage]; ok {
		return p
	}
	return s.Stage + " steps"
}

func (s scaffoldData) Orchestrator() string { return s.Prefix + "-orchestration.yml" }
func (s scaffoldData) Job() string          { return s.Stage }
func (s scaffoldData) CoverageArtifact() string {
	return CoverageArtifact
}

// ciData is the ci-orchestration template input.
type ciData struct {
	Branch          string
	GateJob         string
	PRTitleEnforced bool
	MergeQueue      bool
	Bulwark         bool
	BulwarkDir      string
	AttestContext   string
	CheckoutRef     string
	SkipGuard       string
	// BulwarkGuard exempts a push from the attestation skip, because bulwark
	// records the coverage baseline from exactly that run.
	BulwarkGuard string
	CIStages     []stageJob
	GateNeeds    string
	BulwarkNeeds string

	AttestWorkflowRef              string
	BulwarkWorkflowRef             string
	ConventionalCommitsWorkflowRef string
	GovernanceWorkflowRef          string
}

// cdData is the cd-orchestration template input.
type cdData struct {
	CDStages          []stageJob
	CDTags            []string
	GateNeeds         string
	AttestWorkflowRef string
}

// checkoutRef is the SHA-pinned checkout action the gate uses to read the tree
// it validated. Third-party actions are pinned because a tag their owner
// controls can be repointed at code we never reviewed.
const checkoutRef = "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1"

func buildCIData(in Input, shared templateData) (ciData, error) {
	stages, err := buildStages(
		in.Spec.Pipeline.CI.Stages, repospec.CIStages, ciWiring, "attest", attestGuard)
	if err != nil {
		return ciData{}, err
	}

	fixed := []string{"conventional-commits", "governance"}
	// bulwark runs after tests so it can consume the coverage they uploaded
	// rather than running the suite again.
	bulwarkNeeds := []string{"attest"}
	if in.Spec.Bulwark.Enabled {
		for _, s := range stages {
			if s.Name == "test" {
				bulwarkNeeds = append(bulwarkNeeds, "test")
			}
		}
		fixed = append(fixed, "bulwark")
	}

	major := MajorTag(in.GTVersion)
	return ciData{
		Branch:          in.Spec.Settings.BranchProtection.Branch,
		GateJob:         repospec.GateCheckJob,
		PRTitleEnforced: in.Spec.ConventionalCommits.EnforcesPRTitle(),
		MergeQueue:      in.Spec.Pipeline.CI.MergeQueue,
		Bulwark:         in.Spec.Bulwark.Enabled,
		BulwarkDir:      in.Spec.Bulwark.Dir,
		AttestContext:   AttestContext,
		CheckoutRef:     checkoutRef,
		SkipGuard:       attestGuard,
		CIStages:        stages,
		GateNeeds:       gateNeeds("attest", stages, fixed...),
		BulwarkNeeds:    strings.Join(bulwarkNeeds, ", "),
		BulwarkGuard:    baselineGuard,

		AttestWorkflowRef:              workflowRef("attest.yml", major, in.RepoOwner, in.RepoName),
		BulwarkWorkflowRef:             workflowRef("bulwark.yml", major, in.RepoOwner, in.RepoName),
		ConventionalCommitsWorkflowRef: workflowRef("conventional-commits.yml", major, in.RepoOwner, in.RepoName),
		GovernanceWorkflowRef:          workflowRef("governance.yml", major, in.RepoOwner, in.RepoName),
	}, nil
}

func buildCDData(in Input, shared templateData) (cdData, error) {
	const root = "verify-attestation"
	stages, err := buildStages(
		in.Spec.Pipeline.CD.Stages, repospec.CDStages, cdWiring, root, "")
	if err != nil {
		return cdData{}, err
	}
	major := MajorTag(in.GTVersion)
	return cdData{
		CDStages:          stages,
		CDTags:            in.Spec.Pipeline.CD.Tags,
		GateNeeds:         gateNeeds(root, stages),
		AttestWorkflowRef: workflowRef("attest.yml", major, in.RepoOwner, in.RepoName),
	}, nil
}

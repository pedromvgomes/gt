package repogov

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/pedromvgomes/gt/internal/repospec"
	"gopkg.in/yaml.v3"
)

// Options is the resolved repository context a governance run operates on.
type Options struct {
	// WorkDir is the repository working tree holding .gt-repo.yaml.
	WorkDir   string
	RepoOwner string
	RepoName  string
	// GTVersion is the running gt version, stamped into rendered files.
	GTVersion string
	// SkipWorkflows excludes .github/workflows/** from the run. The weekly
	// in-repo sync sets it because GITHUB_TOKEN cannot write those files.
	SkipWorkflows bool
}

// Report is the outcome of a check or a dry-run sync.
type Report struct {
	Spec     repospec.Spec
	Results  []Result
	Findings []Finding
	// VersionStale is set when the spec records an older gt than the one
	// running, meaning re-rendering may produce different output.
	VersionStale bool
}

// Clean reports whether the repository is fully compliant.
func (r Report) Clean() bool {
	return len(Drifted(r.Results)) == 0 && len(r.Findings) == 0
}

// Check renders the spec, diffs it against the working tree, and lints the
// workflow triggers the gate depends on.
func Check(opts Options) (Report, error) {
	spec, err := repospec.Load(opts.WorkDir)
	if err != nil {
		return Report{}, err
	}
	return checkSpec(spec, opts)
}

func checkSpec(spec repospec.Spec, opts Options) (Report, error) {
	files, err := Render(Input{
		Spec:      spec,
		RepoOwner: opts.RepoOwner,
		RepoName:  opts.RepoName,
		GTVersion: opts.GTVersion,
	})
	if err != nil {
		return Report{}, err
	}
	results, err := Diff(opts.WorkDir, files, opts.SkipWorkflows)
	if err != nil {
		return Report{}, err
	}
	findings, err := Lint(opts.WorkDir, spec)
	if err != nil {
		return Report{}, err
	}
	return Report{
		Spec:         spec,
		Results:      results,
		Findings:     findings,
		VersionStale: spec.GTVersion != "" && spec.GTVersion != opts.GTVersion,
	}, nil
}

// Sync renders and writes any drifted files, then records the gt version that
// produced them. It returns the report as it was *before* writing, so callers
// can report what changed.
func Sync(opts Options) (Report, []string, error) {
	report, err := Check(opts)
	if err != nil {
		return Report{}, nil, err
	}
	written, err := apply(report, opts)
	return report, written, err
}

func apply(report Report, opts Options) ([]string, error) {
	written, err := Write(opts.WorkDir, report.Results)
	if err != nil {
		return written, err
	}
	// A --skip-workflows run did not render the workflow files, so it has not
	// brought the repo up to this gt version and must not claim it has.
	// Stamping here would suppress the staleness warning that is the only
	// remaining signal those files are behind.
	if opts.SkipWorkflows {
		return written, nil
	}

	// Stamp the version only once the files it describes are actually on disk,
	// so an interrupted sync never claims to be newer than it is.
	if report.Spec.GTVersion != opts.GTVersion {
		spec := report.Spec
		spec.GTVersion = opts.GTVersion
		if err := SaveSpec(opts.WorkDir, spec); err != nil {
			return written, err
		}
		written = append(written, repospec.FileName)
	}
	return written, nil
}

// Init seeds a .gt-repo.yaml for a repository that does not have one, using
// detected ecosystems and the check names its existing workflows already
// produce. It does not write governance files; callers follow with Sync.
func Init(opts Options) (repospec.Spec, error) {
	spec := repospec.Default()
	spec.GTVersion = opts.GTVersion

	entries, err := Detect(opts.WorkDir)
	if err != nil {
		return spec, err
	}
	spec.Dependabot = entries

	names, err := ExistingCheckNames(opts.WorkDir)
	if err != nil {
		return spec, err
	}
	spec.Checks.Required = names

	if err := repospec.Validate(spec); err != nil {
		return spec, fmt.Errorf("generated spec is invalid: %w", err)
	}
	return spec, nil
}

// ExistingCheckNames returns the check names the repository's current
// pull_request workflows produce, excluding gt's own files. Seeding
// checks.required from these means `gt repo init` on an existing repo starts
// from what that repo already enforces rather than from nothing.
//
// Names from workflows carrying a top-level paths: filter are excluded: they
// would immediately fail the lint, and they are exactly the checks that belong
// in checks.optional instead.
func ExistingCheckNames(workdir string) ([]string, error) {
	workflows, err := loadWorkflows(workdir)
	if err != nil {
		return nil, err
	}
	var out []string
	for path, wf := range workflows {
		if isGTManaged(path) {
			continue
		}
		trigger := parsePRTrigger(wf.On)
		if !trigger.Present || len(trigger.Paths) > 0 || len(trigger.PathsIgnore) > 0 {
			continue
		}
		names, _ := checkNames(wf, workflows, "", 0)
		for _, n := range names {
			// Matrix jobs report one check per matrix value with the
			// expression expanded, so the template text is not a usable name.
			// Seeding it would hand the user a spec that fails its own lint.
			if IsTemplated(n) {
				continue
			}
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

// isGTManaged reports whether a workflow path is one gt renders, so init never
// seeds checks.required with gt's own aggregator.
func isGTManaged(path string) bool {
	switch path {
	case WorkflowDir + "/gate.yml",
		WorkflowDir + "/gt-sync.yml",
		WorkflowDir + "/dependabot-auto-merge.yml":
		return true
	}
	return false
}

// SaveSpec writes the manifest back, preserving a managed header so the file
// announces how it is meant to be edited.
func SaveSpec(workdir string, spec repospec.Spec) error {
	var buf strings.Builder
	buf.WriteString(specHeader)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(spec); err != nil {
		return fmt.Errorf("encode %s: %w", repospec.FileName, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encode %s: %w", repospec.FileName, err)
	}
	if err := os.WriteFile(repospec.Path(workdir), []byte(buf.String()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", repospec.FileName, err)
	}
	return nil
}

const specHeader = `# Repository governance for gt. This file is the source of truth; run
# 'gt repo sync' to render it, and 'gt repo check' to verify.
#
# Shared policy (Dependabot cooldown, commit-message prefixes, the weekly sync
# schedule) lives in gt's templates, not here, so it stays consistent across
# every governed repo.
#
# checks.required lists the checks gt's gate waits on. Do not list the gate
# itself — it is the aggregator, and branch protection requires only it.

`

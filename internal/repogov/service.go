package repogov

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/pedromvgomes/gt/internal/git"
	"github.com/pedromvgomes/gt/internal/repospec"
	"gopkg.in/yaml.v3"
)

// ResolveWorkDir returns the working tree governance should act on.
//
// This is deliberately not setup.Context.WorkDir: in a gt-managed bare layout
// that always points at the default-branch checkout, because that is what
// post-clone setup templates operate on. Governance must act on the worktree
// the user is standing in — otherwise `gt repo sync` run from a feature
// worktree writes its changes into the main checkout instead, leaving the
// worktree untouched and dirtying a tree the user was not working in.
//
// `rev-parse --show-toplevel` resolves correctly in every layout: a plain
// clone, the default-branch checkout, and a linked worktree.
func ResolveWorkDir(ctx context.Context, runner git.Runner, cwd string) (string, error) {
	res, err := runner.Run(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("not inside a git repository: %w", err)
	}
	workdir := strings.TrimSpace(res.Stdout)
	if workdir == "" {
		return "", fmt.Errorf("could not resolve the repository working tree")
	}
	return workdir, nil
}

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
	Spec    repospec.Spec
	Results []Result
	// VersionStale is set when the spec records an older gt than the one
	// running, meaning re-rendering may produce different output.
	VersionStale bool
}

// Clean reports whether the repository is fully compliant.
func (r Report) Clean() bool {
	return len(Drifted(r.Results)) == 0
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
	return Report{
		Spec:         spec,
		Results:      results,
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
	if err := refuseDowngrade(report.Spec.GTVersion, opts.GTVersion); err != nil {
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

// refuseDowngrade stops an older gt from rewriting a repository's workflow pins
// backwards.
//
// The `uses:` major tag is derived from whichever binary runs sync, so a stale
// gt would silently repoint every caller at an older major — leaving the repo
// on logic nobody intended, with nothing but a version warning to show for it.
// The comparison is on the major alone, because that is exactly what the pin
// carries: a v0 binary against a v0 spec is fine, however far apart the minors.
//
// There is deliberately no --force. Rolling back is still possible, but it
// takes editing gt_version in .gt-repo.yaml first, which is a considered act
// rather than an accident.
func refuseDowngrade(specVersion, running string) error {
	if specVersion == "" {
		return nil
	}
	specMajor, ok := majorNumber(specVersion)
	if !ok {
		return nil
	}
	runMajor, ok := majorNumber(running)
	if !ok {
		// A dev build reports no usable version. Treat it as v0, which is what
		// it would actually render.
		runMajor = 0
	}
	if runMajor >= specMajor {
		return nil
	}
	return fmt.Errorf(
		"this repository was rendered by gt %s, but %s is running: syncing would repoint its "+
			"workflows from %s to %s.\n"+
			"Upgrade with 'gt update', or edit gt_version in %s if you mean to roll back",
		specVersion, displayVersion(running),
		MajorTag(specVersion), MajorTag(running), repospec.FileName)
}

func majorNumber(version string) (int, bool) {
	tag := strings.TrimPrefix(MajorTag(version), "v")
	n, err := strconv.Atoi(tag)
	if err != nil {
		return 0, false
	}
	// MajorTag falls back to v0 for anything unparseable, so an unrecognised
	// version is reported as such rather than silently passing as major 0.
	if version == "" || !startsWithDigit(strings.TrimPrefix(version, "v")) {
		return n, false
	}
	return n, true
}

func startsWithDigit(s string) bool {
	return s != "" && s[0] >= '0' && s[0] <= '9'
}

func displayVersion(v string) string {
	if !startsWithDigit(strings.TrimPrefix(v, "v")) {
		return "an unversioned build"
	}
	return v
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

	if err := repospec.Validate(spec); err != nil {
		return spec, fmt.Errorf("generated spec is invalid: %w", err)
	}
	return spec, nil
}

// SaveSpec writes the manifest back, preserving a managed header so the file
// announces how it is meant to be edited.
func SaveSpec(workdir string, spec repospec.Spec) error {
	var buf strings.Builder
	buf.WriteString(specHeader)
	if err := WriteSpecTo(&buf, spec); err != nil {
		return err
	}
	if err := os.WriteFile(repospec.Path(workdir), []byte(buf.String()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", repospec.FileName, err)
	}
	return nil
}

// WriteSpecTo encodes a spec as YAML.
func WriteSpecTo(w io.Writer, spec repospec.Spec) error {
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(spec); err != nil {
		return fmt.Errorf("encode %s: %w", repospec.FileName, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encode %s: %w", repospec.FileName, err)
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
# The pipeline stages below become jobs in ci-orchestration.yml, each calling a
# ci-*/cd-* workflow that belongs to this repository: gt creates those once and
# never touches them again. Branch protection needs exactly one check, ci-gate,
# which waits on all of them.

`

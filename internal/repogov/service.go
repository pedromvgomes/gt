package repogov

import (
	"bytes"
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
	// SpecStale is set when .gt-repo.yaml would not round-trip to itself —
	// almost always because it restates gt defaults that sync now omits. It is
	// drift in its own right: a file that pins a default has silently stopped
	// tracking it, so check reports it and sync rewrites it.
	SpecStale bool
}

// Clean reports whether the repository is fully compliant.
func (r Report) Clean() bool {
	return len(Drifted(r.Results)) == 0 && !r.SpecStale
}

// versionStale reports whether the spec was rendered by a different gt than the
// one running, ignoring a leading "v".
//
// The prefix is not cosmetic drift between two conventions we control: goreleaser
// stamps main.version from {{.Version}}, which is "1.0.0", while tags, docs and
// hand-built binaries all say "v1.0.0". A raw string compare therefore called
// every repository stale the moment it was rendered by anything but a release
// build, and told its owner to re-sync a repository that was already identical.
//
// Warning wrongly is worse here than not warning: the whole point of the signal
// is that it means something when it fires.
func versionStale(specVersion, running string) bool {
	norm := func(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }
	if norm(specVersion) == "" {
		return false
	}
	return norm(specVersion) != norm(running)
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
	// Compared against the spec as it stands, version included: this asks
	// "would sync rewrite this file for reasons other than the version stamp?",
	// and folding the stamp in would make every pre-upgrade repository report
	// spec drift on top of the staleness warning it already gets.
	specStale, err := specDrifted(opts.WorkDir, spec)
	if err != nil {
		return Report{}, err
	}
	return Report{
		Spec:         spec,
		Results:      results,
		VersionStale: versionStale(spec.GTVersion, opts.GTVersion),
		SpecStale:    specStale,
	}, nil
}

func specDrifted(workdir string, spec repospec.Spec) (bool, error) {
	want, err := specBytes(spec)
	if err != nil {
		return false, err
	}
	current, err := os.ReadFile(repospec.Path(workdir))
	if err != nil {
		return false, fmt.Errorf("read %s: %w", repospec.FileName, err)
	}
	return !bytes.Equal(current, want), nil
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
	// The spec is rewritten whenever its serialisation differs from what gt
	// would write — which is how a file that restates defaults gets slimmed
	// down. Keying this on the version alone would mean a repository only sheds
	// redundant keys when it happens to upgrade, and never sheds ones added by
	// hand.
	//
	// A --skip-workflows run did not render the workflow files, so it has not
	// brought the repo up to this gt version and must not claim it has:
	// stamping there would suppress the staleness warning that is the only
	// remaining signal those files are behind. Slimming the file is not a
	// version claim, so it still happens — that run is the weekly in-repo sync,
	// and it is what rolls this out across the fleet without anyone cloning
	// eleven repositories.
	spec := report.Spec
	if !opts.SkipWorkflows {
		// Stamp the version only once the files it describes are actually on
		// disk, so an interrupted sync never claims to be newer than it is.
		spec.GTVersion = opts.GTVersion
	}
	changed, err := saveSpecIfChanged(opts.WorkDir, spec)
	if err != nil {
		return written, err
	}
	if changed {
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
	raw, err := specBytes(spec)
	if err != nil {
		return err
	}
	// #nosec G306 -- .gt-repo.yaml is committed and read by CI; it is repository content, not a secret.
	if err := os.WriteFile(repospec.Path(workdir), raw, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", repospec.FileName, err)
	}
	return nil
}

// saveSpecIfChanged writes the manifest only when it would differ from what is
// already there, and reports whether it wrote. Sync uses this so an unchanged
// repository reports nothing written rather than a no-op edit.
func saveSpecIfChanged(workdir string, spec repospec.Spec) (bool, error) {
	raw, err := specBytes(spec)
	if err != nil {
		return false, err
	}
	if current, err := os.ReadFile(repospec.Path(workdir)); err == nil && bytes.Equal(current, raw) {
		return false, nil
	}
	// #nosec G306 -- .gt-repo.yaml is committed and read by CI; it is repository content, not a secret.
	if err := os.WriteFile(repospec.Path(workdir), raw, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", repospec.FileName, err)
	}
	return true, nil
}

func specBytes(spec repospec.Spec) ([]byte, error) {
	var buf strings.Builder
	buf.WriteString(specHeader)
	if err := writeOverridesTo(&buf, spec); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

// WriteSpecTo encodes the fully resolved spec as YAML, defaults included. This
// is what `gt repo config` prints, and it is deliberately not what gets written
// to disk — see writeOverridesTo.
func WriteSpecTo(w io.Writer, spec repospec.Spec) error {
	return encodeYAML(w, spec)
}

// writeOverridesTo encodes a spec as YAML with every field that still carries
// gt's default omitted. This is what lands in .gt-repo.yaml.
//
// It is the whole point of the file being opinionated. Serialising the fully
// resolved spec pinned all ~25 defaults into every repository, and a repository
// that has pinned a default no longer tracks it: changing an opinion in gt
// would reach nobody, because all eleven files already said the old value
// explicitly. What is written is exactly the set of deliberate overrides, so
// everything absent follows gt.
//
// Safe because repospec.Parse unmarshals over Default(), so an absent field and
// a field written with its default value parse to the same spec — which is also
// why pruning a value someone typed by hand loses nothing.
func writeOverridesTo(w io.Writer, spec repospec.Spec) error {
	node, err := prunedSpecNode(spec)
	if err != nil {
		return err
	}
	return encodeYAML(w, node)
}

func encodeYAML(w io.Writer, v any) error {
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encode %s: %w", repospec.FileName, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encode %s: %w", repospec.FileName, err)
	}
	return nil
}

// prunedSpecNode renders spec and repospec.Default() to YAML nodes and removes
// from the former every key whose value the latter already carries.
//
// It works on nodes rather than a map[string]any so that field order survives:
// yaml.v3 emits struct fields in declaration order but map keys sorted, and a
// spec file whose keys reshuffle on every sync is a diff nobody can read.
func prunedSpecNode(spec repospec.Spec) (*yaml.Node, error) {
	actual, err := specNode(spec)
	if err != nil {
		return nil, err
	}
	defaults, err := specNode(repospec.Default())
	if err != nil {
		return nil, err
	}
	pruneNode(actual, defaults)
	return actual, nil
}

func specNode(spec repospec.Spec) (*yaml.Node, error) {
	// Node.Encode rather than a Marshal/Unmarshal round trip through bytes:
	// it yields the mapping directly, with no document node to unwrap and no
	// second error path that a fixed struct type can never take anyway.
	var node yaml.Node
	if err := node.Encode(spec); err != nil {
		return nil, fmt.Errorf("encode %s: %w", repospec.FileName, err)
	}
	return &node, nil
}

// pruneNode deletes from actual every mapping key that is identical to the same
// key in defaults, recursing into nested mappings so a struct with one override
// keeps that one field rather than all of them.
func pruneNode(actual, defaults *yaml.Node) {
	if actual == nil || defaults == nil || actual.Kind != yaml.MappingNode || defaults.Kind != yaml.MappingNode {
		return
	}
	kept := make([]*yaml.Node, 0, len(actual.Content))
	for i := 0; i+1 < len(actual.Content); i += 2 {
		key, val := actual.Content[i], actual.Content[i+1]
		def := mappingValue(defaults, key.Value)
		if def == nil {
			kept = append(kept, key, val)
			continue
		}
		if nodesEqual(val, def) {
			continue
		}
		// No empty-mapping check afterwards: both sides are the same Go struct
		// type, so they always carry the same keys, and a mapping whose every
		// child matched would have been equal and dropped above. A mapping that
		// reaches the recursion therefore always keeps at least one child.
		if val.Kind == yaml.MappingNode && def.Kind == yaml.MappingNode {
			pruneNode(val, def)
		}
		kept = append(kept, key, val)
	}
	actual.Content = kept
}

func mappingValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// nodesEqual compares two nodes structurally. Comments and position are
// deliberately ignored: what matters is whether the two would parse to the same
// value, not whether they were written identically.
func nodesEqual(a, b *yaml.Node) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Kind != b.Kind || a.Tag != b.Tag || a.Value != b.Value || len(a.Content) != len(b.Content) {
		return false
	}
	for i := range a.Content {
		if !nodesEqual(a.Content[i], b.Content[i]) {
			return false
		}
	}
	return true
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
#
# Only deliberate overrides are written here. Everything absent follows gt's
# default and keeps following it as that default changes — a value pinned in
# this file stops tracking gt, which is why sync removes the ones that merely
# restate it. Run 'gt repo config' to see the resolved spec, defaults included.

`

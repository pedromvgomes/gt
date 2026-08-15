// Package repogov renders a repository's governance files from its
// .gt-repo.yaml, diffs them against what is on disk, and lints the workflow
// triggers the gate depends on.
//
// Templates are embedded in the binary rather than fetched: repo governance is
// a single standard gt itself owns, so the policy version is the gt version.
// That removes the whole fetch/lock/cache layer a federated model would need.
package repogov

import (
	"bytes"
	"embed"
	"fmt"
	"path"
	"sort"
	"strings"
	"text/template"

	"github.com/pedromvgomes/gt/internal/repospec"
)

//go:embed templates
var templatesFS embed.FS

// Upstream is the repository hosting gt's reusable workflows. Callers pin the
// moving major tag of this repo.
const Upstream = "pedromvgomes/gt"

// Shared Dependabot policy. These live here, not in .gt-repo.yaml, so changing
// them for every governed repo is a one-line edit in gt.
const (
	// CooldownDays is the supply-chain guard: a freshly-cut version gets this
	// long to be yanked or superseded before Dependabot opens a PR for it.
	CooldownDays = 7
	// DependabotInterval is how often Dependabot looks for updates.
	DependabotInterval = "weekly"
	// DependabotPRLimit raises Dependabot's default of 5. Across several
	// ecosystems on a weekly schedule, 5 leaves the queue permanently full and
	// silently starves the oldest updates.
	DependabotPRLimit = 25
)

// dependabotPrefix maps an ecosystem to its conventional-commit type.
//
// `build` for dependencies, because that is what the type is for; `ci` for
// github-actions, because those bumps only ever touch workflow files. Combined
// with `include: scope` Dependabot produces e.g. `build(deps): bump …`, which
// is a valid conventional subject on the default branch under squash merge —
// so dependency PRs pass the same title check as everything else.
//
// This couples to conventional_commits.types: `build` and `ci` must both be
// accepted there, or every Dependabot PR would fail its own gate.
var dependabotPrefix = map[string]string{
	"github-actions": "ci",
}

const defaultDependabotPrefix = "build"

// File is one rendered governance file.
type File struct {
	// Path is repo-root-relative, always slash-separated.
	Path string
	// Content is the fully rendered bytes.
	Content []byte
	// Workflow marks files under .github/workflows/, which GITHUB_TOKEN
	// cannot write. Sync skips these in CI and defers them to a local run.
	Workflow bool
}

// Input is everything the templates need beyond the spec itself.
type Input struct {
	Spec      repospec.Spec
	RepoOwner string
	RepoName  string
	// GTVersion is the running gt version. It determines the major tag the
	// callers pin, and is recorded in .gt-repo.yaml — deliberately NOT written
	// into the rendered files themselves. Stamping it into file headers would
	// make every gt release drift every workflow file in every repo, and CI
	// cannot repair workflow files, so each release would turn the gate red
	// everywhere until a human ran sync locally.
	GTVersion string
}

// templateData is the flattened view handed to text/template. Templates stay
// free of Go logic; everything they need is precomputed here.
type templateData struct {
	MajorTag             string
	RepoOwner            string
	RepoName             string
	Branch               string
	GateCheckName        string
	GateWorkflowRef      string
	SyncWorkflowRef      string
	AutoMergeWorkflowRef string
	SyncSchedule         string
	AutoMergeSchedule    string
	MaxBump              string
	PRTitleEnforced      bool
	CooldownDays         int
	PRLimit              int
	Entries              []dependabotEntry
}

type dependabotEntry struct {
	Ecosystem string
	Directory string
	Note      string
	Interval  string
	Prefix    string
}

// SyncSchedule is the weekly cron for the drift-repair workflow. Unlike the
// auto-merge schedule it is not configurable per repo — there is no reason for
// repos to disagree about when they check themselves.
const SyncSchedule = "0 6 * * 1"

// Render produces the full set of governance files the spec asks for, sorted
// by path so output is deterministic.
func Render(in Input) ([]File, error) {
	data := buildData(in)

	var files []File

	// dependabot.yml is rendered whenever any ecosystem is declared, and is not
	// gated on files: — declaring an ecosystem is the opt-in.
	if len(in.Spec.Dependabot) > 0 {
		content, err := renderTemplate("templates/dependabot.yml.tmpl", data)
		if err != nil {
			return nil, err
		}
		files = append(files, File{Path: ".github/dependabot.yml", Content: content})
	}

	for _, f := range []struct {
		key      string
		tmpl     string
		path     string
		workflow bool
	}{
		{"gate", "templates/workflows/gate.yml.tmpl", ".github/workflows/gate.yml", true},
		{"sync", "templates/workflows/gt-sync.yml.tmpl", ".github/workflows/gt-sync.yml", true},
		{"dependabot-auto-merge", "templates/workflows/dependabot-auto-merge.yml.tmpl", ".github/workflows/dependabot-auto-merge.yml", true},
		{"codeowners", "templates/codeowners.tmpl", ".github/CODEOWNERS", false},
		{"editorconfig", "templates/editorconfig.tmpl", ".editorconfig", false},
		{"pr-template", "templates/pr-template.tmpl", ".github/pull_request_template.md", false},
	} {
		if !in.Spec.WantsFile(f.key) {
			continue
		}
		// An auto-merge caller with the feature disabled would be a workflow
		// file that exists only to do nothing on a schedule.
		if f.key == "dependabot-auto-merge" && !in.Spec.DependabotAutoMerge.Enabled {
			continue
		}
		content, err := renderTemplate(f.tmpl, data)
		if err != nil {
			return nil, err
		}
		files = append(files, File{Path: f.path, Content: content, Workflow: f.workflow})
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func buildData(in Input) templateData {
	major := MajorTag(in.GTVersion)
	entries := make([]dependabotEntry, 0, len(in.Spec.Dependabot))
	for _, e := range in.Spec.Dependabot {
		prefix, ok := dependabotPrefix[e.Ecosystem]
		if !ok {
			prefix = defaultDependabotPrefix
		}
		entries = append(entries, dependabotEntry{
			Ecosystem: e.Ecosystem,
			Directory: e.Directory,
			Note:      e.Note,
			Interval:  DependabotInterval,
			Prefix:    prefix,
		})
	}
	return templateData{
		MajorTag:             major,
		RepoOwner:            in.RepoOwner,
		RepoName:             in.RepoName,
		Branch:               in.Spec.Settings.BranchProtection.Branch,
		GateCheckName:        repospec.GateCheckName,
		GateWorkflowRef:      workflowRef("gate.yml", major, in.RepoOwner, in.RepoName),
		SyncWorkflowRef:      workflowRef("sync.yml", major, in.RepoOwner, in.RepoName),
		AutoMergeWorkflowRef: workflowRef("dependabot-auto-merge.yml", major, in.RepoOwner, in.RepoName),
		SyncSchedule:         SyncSchedule,
		AutoMergeSchedule:    in.Spec.DependabotAutoMerge.Schedule,
		MaxBump:              in.Spec.DependabotAutoMerge.MaxBump,
		PRTitleEnforced:      in.Spec.ConventionalCommits.EnforcesPRTitle(),
		CooldownDays:         CooldownDays,
		PRLimit:              DependabotPRLimit,
		Entries:              entries,
	}
}

// workflowRef builds a `uses:` reference to one of gt's reusable workflows.
//
// gt's own definitions are named reusable-*.yml so they never collide with the
// caller filenames rendered into consumer repos. Without that distinction, gt
// governing itself would overwrite its own reusable workflows with thin callers
// pointing at themselves.
// In the upstream repository the reference is local (`./…`) rather than pinned
// to the moving tag. A local ref resolves to the caller's own commit, so gt's
// PRs exercise the gate logic *in that PR*; pinning the tag would test the last
// release instead, leaving changes to the reusable workflows unverifiable until
// after they had already shipped. Everywhere else the pinned tag is the whole
// point — it is what lets gate logic reach consumers without editing any file.
func workflowRef(file, majorTag, owner, name string) string {
	if owner+"/"+name == Upstream {
		return "./.github/workflows/reusable-" + file
	}
	return fmt.Sprintf("%s/.github/workflows/reusable-%s@%s", Upstream, file, majorTag)
}

// MajorTag returns the moving tag callers pin, derived from a gt version.
// v0.6.0 yields "v0"; v1.2.3 yields "v1". Tracking the major rather than
// hardcoding v1 means the scheme works today, while gt is still 0.x, and keeps
// working across a 1.0 release without rewriting every caller.
//
// An unparseable version (a dev build) falls back to v0 so local runs still
// render something valid.
func MajorTag(version string) string {
	v := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if i := strings.IndexAny(v, ".-+"); i > 0 {
		v = v[:i]
	}
	if v == "" || strings.ContainsFunc(v, func(r rune) bool { return r < '0' || r > '9' }) {
		return "v0"
	}
	return "v" + v
}

var funcs = template.FuncMap{
	// comment re-emits a spec `note:` as YAML comment lines at the given
	// indent, so per-repo rationale survives into the rendered file instead of
	// being lost the moment the file is generated.
	"comment": func(indent, body string) string {
		lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
		out := make([]string, 0, len(lines))
		for _, l := range lines {
			if strings.TrimSpace(l) == "" {
				out = append(out, indent+"#")
				continue
			}
			out = append(out, indent+"# "+l)
		}
		return strings.Join(out, "\n")
	},
}

func renderTemplate(name string, data templateData) ([]byte, error) {
	raw, err := templatesFS.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("read embedded template %s: %w", name, err)
	}
	tmpl, err := template.New(path.Base(name)).Funcs(funcs).Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("parse template %s: %w", name, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render template %s: %w", name, err)
	}
	out := buf.Bytes()
	// Every rendered file ends with exactly one newline, so a trailing-newline
	// difference never shows up as spurious drift.
	out = append(bytes.TrimRight(out, "\n"), '\n')
	return out, nil
}

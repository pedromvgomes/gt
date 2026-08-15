package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pedromvgomes/gt/internal/repogov"
	"github.com/pedromvgomes/gt/internal/repospec"
	"gopkg.in/yaml.v3"
)

func testInput(spec repospec.Spec) repogov.Input {
	return repogov.Input{Spec: spec, RepoOwner: "pedromvgomes", RepoName: "demo", GTVersion: "v0.6.0"}
}

func renderMap(t *testing.T, in repogov.Input) map[string][]byte {
	t.Helper()
	files, err := repogov.Render(in)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	out := map[string][]byte{}
	for _, f := range files {
		out[f.Path] = f.Content
	}
	return out
}

// Every rendered file must be valid YAML. A template that emits subtly broken
// YAML would be accepted by gt and then silently ignored by GitHub.
func TestRenderProducesValidYAML(t *testing.T) {
	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{
		{Ecosystem: "cargo", Directory: "/source/daemon"},
		{Ecosystem: "npm", Directory: "/source", Note: "Workspace root: single lockfile.\nSecond line."},
		{Ecosystem: "github-actions", Directory: "/"},
	}
	spec.Files = repospec.FileKeys

	for path, content := range renderMap(t, testInput(spec)) {
		if !strings.HasSuffix(path, ".yml") && !strings.HasSuffix(path, ".yaml") {
			continue
		}
		var v any
		if err := yaml.Unmarshal(content, &v); err != nil {
			t.Errorf("%s is not valid YAML: %v\n%s", path, err, content)
		}
	}
}

func TestRenderDependabotPolicy(t *testing.T) {
	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{
		{Ecosystem: "gomod", Directory: "/"},
		{Ecosystem: "github-actions", Directory: "/"},
	}
	got := renderMap(t, testInput(spec))[".github/dependabot.yml"]

	var parsed struct {
		Version int `yaml:"version"`
		Updates []struct {
			Ecosystem string `yaml:"package-ecosystem"`
			Directory string `yaml:"directory"`
			Schedule  struct {
				Interval string `yaml:"interval"`
			} `yaml:"schedule"`
			CommitMessage struct {
				Prefix  string `yaml:"prefix"`
				Include string `yaml:"include"`
			} `yaml:"commit-message"`
			Cooldown struct {
				DefaultDays int `yaml:"default-days"`
			} `yaml:"cooldown"`
			PRLimit int `yaml:"open-pull-requests-limit"`
		} `yaml:"updates"`
	}
	if err := yaml.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal rendered dependabot.yml: %v", err)
	}
	if parsed.Version != 2 {
		t.Errorf("version = %d, want 2", parsed.Version)
	}
	if len(parsed.Updates) != 2 {
		t.Fatalf("updates = %d, want 2", len(parsed.Updates))
	}

	accepted := map[string]bool{}
	for _, ty := range repospec.Default().ConventionalCommits.Types {
		accepted[ty] = true
	}

	for _, u := range parsed.Updates {
		if u.Cooldown.DefaultDays != repogov.CooldownDays {
			t.Errorf("%s cooldown = %d, want %d", u.Ecosystem, u.Cooldown.DefaultDays, repogov.CooldownDays)
		}
		if u.Schedule.Interval != "weekly" {
			t.Errorf("%s interval = %q, want weekly", u.Ecosystem, u.Schedule.Interval)
		}
		if u.PRLimit != repogov.DependabotPRLimit {
			t.Errorf("%s open-pull-requests-limit = %d, want %d", u.Ecosystem, u.PRLimit, repogov.DependabotPRLimit)
		}
		// `include: scope` is what turns the bare type into `build(deps):`.
		if u.CommitMessage.Include != "scope" {
			t.Errorf("%s commit-message.include = %q, want scope", u.Ecosystem, u.CommitMessage.Include)
		}
		// The real coupling: Dependabot's prefix becomes the PR title, which
		// the gate then checks against conventional_commits.types. A prefix
		// missing from that list would fail every dependency PR.
		if !accepted[u.CommitMessage.Prefix] {
			t.Errorf("%s prefix %q is not in conventional_commits.types %v — every dependency PR would fail the title check",
				u.Ecosystem, u.CommitMessage.Prefix, repospec.Default().ConventionalCommits.Types)
		}
	}
}

// A note exists to carry per-repo rationale into the generated file. If it were
// dropped, generating the file would destroy the reasoning behind it.
func TestRenderPreservesNotesAsComments(t *testing.T) {
	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{{
		Ecosystem: "npm",
		Directory: "/source",
		Note:      "Workspace ROOT, single yarn.lock.\nPer-workspace entries break --immutable.",
	}}
	got := string(renderMap(t, testInput(spec))[".github/dependabot.yml"])

	for _, want := range []string{
		"# Workspace ROOT, single yarn.lock.",
		"# Per-workspace entries break --immutable.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered dependabot.yml missing %q:\n%s", want, got)
		}
	}
}

// The moving major tag is what lets gate logic change without editing a single
// repo, so it must track gt's major rather than being pinned to a literal.
func TestMajorTag(t *testing.T) {
	tests := map[string]string{
		"v0.6.0":       "v0",
		"0.6.0":        "v0",
		"v1.2.3":       "v1",
		"v12.0.0":      "v12",
		"v2.0.0-rc.1":  "v2",
		"dev":          "v0",
		"":             "v0",
		"not-a-semver": "v0",
	}
	for in, want := range tests {
		if got := repogov.MajorTag(in); got != want {
			t.Errorf("MajorTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderCallersPinMovingMajorTag(t *testing.T) {
	spec := repospec.Default()
	files := renderMap(t, testInput(spec))
	for _, path := range []string{
		".github/workflows/gate.yml",
		".github/workflows/gt-sync.yml",
		".github/workflows/dependabot-auto-merge.yml",
	} {
		content, ok := files[path]
		if !ok {
			t.Fatalf("%s was not rendered", path)
		}
		if !strings.Contains(string(content), repogov.Upstream+"/.github/workflows/") {
			t.Errorf("%s does not call gt's reusable workflow:\n%s", path, content)
		}
		if !strings.Contains(string(content), "@v0") {
			t.Errorf("%s does not pin the moving major tag:\n%s", path, content)
		}
	}
}

// The check name is the one string branch protection is configured with. If it
// ever changes, every governed repo silently stops being protected.
func TestRenderedGateProducesTheExpectedCheckName(t *testing.T) {
	content := renderMap(t, testInput(repospec.Default()))[".github/workflows/gate.yml"]

	var wf struct {
		Name string `yaml:"name"`
		Jobs map[string]struct {
			Name string `yaml:"name"`
			Uses string `yaml:"uses"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(content, &wf); err != nil {
		t.Fatalf("unmarshal gate.yml: %v", err)
	}
	if len(wf.Jobs) != 1 {
		t.Fatalf("gate.yml has %d jobs, want exactly 1", len(wf.Jobs))
	}
	// A reusable-workflow job reports as "<caller job> / <called job>". The
	// caller job id supplies the first half.
	for id := range wf.Jobs {
		want := id + " / Gate"
		if want != repospec.GateCheckName {
			t.Errorf("caller job %q yields check %q, want %q", id, want, repospec.GateCheckName)
		}
	}
}

// An auto-merge caller with the feature disabled would be a workflow that
// exists only to wake up on a schedule and do nothing.
func TestRenderOmitsAutoMergeWhenDisabled(t *testing.T) {
	spec := repospec.Default()
	spec.DependabotAutoMerge.Enabled = false
	if _, ok := renderMap(t, testInput(spec))[".github/workflows/dependabot-auto-merge.yml"]; ok {
		t.Error("auto-merge workflow was rendered despite being disabled")
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{
		{Ecosystem: "npm", Directory: "/web"},
		{Ecosystem: "gomod", Directory: "/"},
	}
	first := renderMap(t, testInput(spec))
	for i := 0; i < 5; i++ {
		next := renderMap(t, testInput(spec))
		if len(next) != len(first) {
			t.Fatalf("render %d produced %d files, want %d", i, len(next), len(first))
		}
		for path, content := range first {
			if string(next[path]) != string(content) {
				t.Fatalf("render %d differs at %s", i, path)
			}
		}
	}
}

func TestDiffAndWriteRoundTrip(t *testing.T) {
	root := t.TempDir()
	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/"}}

	files, err := repogov.Render(testInput(spec))
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	results, err := repogov.Diff(root, files, false)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	for _, r := range results {
		if r.Status != repogov.StatusMissing {
			t.Errorf("%s status = %q, want missing on an empty tree", r.Path, r.Status)
		}
	}

	if _, err := repogov.Write(root, results); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	results, err = repogov.Diff(root, files, false)
	if err != nil {
		t.Fatalf("Diff() after write error = %v", err)
	}
	if drifted := repogov.Drifted(results); len(drifted) != 0 {
		t.Fatalf("after write, %d file(s) still drifted: %v", len(drifted), drifted)
	}

	// A hand edit must register as drift, otherwise `check` cannot do its job.
	target := filepath.Join(root, ".github", "dependabot.yml")
	if err := os.WriteFile(target, []byte("version: 2\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	results, err = repogov.Diff(root, files, false)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	if drifted := repogov.Drifted(results); len(drifted) != 1 || drifted[0].Status != repogov.StatusDrifted {
		t.Fatalf("hand edit produced %v, want exactly one drifted file", drifted)
	}
}

// The weekly in-repo sync runs under GITHUB_TOKEN, which cannot write workflow
// files. It must not report drift it is structurally unable to fix.
func TestDiffSkipWorkflowsExcludesWorkflowFiles(t *testing.T) {
	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/"}}
	files, err := repogov.Render(testInput(spec))
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	results, err := repogov.Diff(t.TempDir(), files, true)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	for _, r := range results {
		if r.Workflow {
			t.Errorf("%s was included despite skipWorkflows", r.Path)
		}
	}
	if len(results) == 0 {
		t.Error("skipWorkflows excluded everything; non-workflow files should remain")
	}
}

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

// Grouping exists because some dependencies break the default branch unless
// they move together — wardnet's codeql-action pair is the canonical case, and
// three separate red-main incidents there are the evidence. Rendering must
// carry the group through, or migrating a repo onto gt silently removes the
// protection its config was written to provide.
func TestRenderDependabotGroups(t *testing.T) {
	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{{
		Ecosystem: "github-actions",
		Directory: "/",
		Groups: []repospec.DependabotGroup{{
			Name:      "codeql-action",
			Patterns:  []string{"github/codeql-action*"},
			Note:      "init and analyze must move in lockstep.",
			AppliesTo: repospec.AppliesToVersion,
		}},
	}}
	got := renderMap(t, testInput(spec))[".github/dependabot.yml"]

	var parsed struct {
		Updates []struct {
			Groups map[string]struct {
				Patterns  []string `yaml:"patterns"`
				AppliesTo string   `yaml:"applies-to"`
			} `yaml:"groups"`
		} `yaml:"updates"`
	}
	if err := yaml.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal rendered dependabot.yml: %v\n%s", err, got)
	}
	if len(parsed.Updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(parsed.Updates))
	}
	g, ok := parsed.Updates[0].Groups["codeql-action"]
	if !ok {
		t.Fatalf("group codeql-action missing:\n%s", got)
	}
	if len(g.Patterns) != 1 || g.Patterns[0] != "github/codeql-action*" {
		t.Errorf("patterns = %v, want [github/codeql-action*]", g.Patterns)
	}
	if !strings.Contains(string(got), "# init and analyze must move in lockstep.") {
		t.Errorf("group note not carried into the rendered file:\n%s", got)
	}
	// Dropping applies-to would silently fold security advisories on a grouped
	// dependency into the routine batch instead of raising them immediately.
	if g.AppliesTo != repospec.AppliesToVersion {
		t.Errorf("applies-to = %q, want %q", g.AppliesTo, repospec.AppliesToVersion)
	}
}

// applies-to is optional, and an empty one must render nothing rather than an
// empty key Dependabot would reject.
func TestRenderOmitsAppliesToWhenUnset(t *testing.T) {
	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{{
		Ecosystem: "gomod", Directory: "/",
		Groups: []repospec.DependabotGroup{{Name: "g", Patterns: []string{"a*"}}},
	}}
	if got := string(renderMap(t, testInput(spec))[".github/dependabot.yml"]); strings.Contains(got, "applies-to") {
		t.Errorf("rendered an applies-to key that was never set:\n%s", got)
	}
}

// An entry that declares no groups must not emit a bare `groups:` key, which
// Dependabot rejects outright.
func TestRenderOmitsGroupsWhenUnset(t *testing.T) {
	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/"}}
	if got := string(renderMap(t, testInput(spec))[".github/dependabot.yml"]); strings.Contains(got, "groups:") {
		t.Errorf("rendered an empty groups key:\n%s", got)
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
		".github/workflows/gt-sync.yml",
		".github/workflows/dependabot-auto-merge.yml",
		// The orchestrator is not a thin caller, but the fixed jobs inside it
		// still reference gt by the moving tag.
		".github/workflows/ci-orchestration.yml",
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
//
// The aggregator is a plain job in a repo-owned workflow, so its check name is
// simply the job name — no "<caller> / " prefix.
// Rendered files must not vary with the gt version that produced them, beyond
// the pinned major in a `uses:` line. Input.GTVersion's own doc comment says
// so — stamping a version into file contents makes every gt release drift
// every workflow in every repo, and CI cannot repair workflow files, so each
// release would turn the gate red everywhere until someone ran sync locally.
//
// Nothing enforced it, and gt-sync.yml interpolated the major tag into a
// comment. Self-onboarding caught it the hard way: CI builds gt unstamped, so
// it rendered "v0" where the committed file said "v1", and gt's own governance
// job failed on a one-word difference inside a comment.
func TestRenderedContentDoesNotVaryWithGTVersion(t *testing.T) {
	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/"}}
	spec.Files = repospec.FileKeys

	render := func(version string) map[string]string {
		in := repogov.Input{Spec: spec, RepoOwner: "acme", RepoName: "widgets", GTVersion: version}
		files, err := repogov.Render(in)
		if err != nil {
			t.Fatalf("Render(%s) error = %v", version, err)
		}
		out := map[string]string{}
		for _, f := range files {
			out[f.Path] = string(f.Content)
		}
		return out
	}

	// Same major, different patch: identical output, no exceptions.
	for path, a := range render("v1.0.0") {
		if b := render("v1.9.3")[path]; a != b {
			t.Errorf("%s differs between v1.0.0 and v1.9.3 — a patch release would drift every repo", path)
		}
	}

	// Across majors only the pinned `uses:` ref may move. Any other difference
	// is a version stamped into content.
	v1, v2 := render("v1.0.0"), render("v2.0.0")
	for path, a := range v1 {
		b := v2[path]
		if a == b {
			continue
		}
		for _, line := range diffLines(a, b) {
			if !strings.Contains(line, "uses:") {
				t.Errorf("%s: non-`uses:` line varies with the gt major:\n  %s", path, strings.TrimSpace(line))
			}
		}
	}
}

// diffLines returns the lines of a and b that differ positionally.
func diffLines(a, b string) []string {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	var out []string
	for i := range la {
		if i >= len(lb) {
			out = append(out, la[i])
			continue
		}
		if la[i] != lb[i] {
			out = append(out, la[i])
		}
	}
	return out
}

// The orchestrators must not claim a display name a repository already uses.
// Every repo in the fleet but one has a workflow called CI, and the stages move
// into gt's orchestrator a few at a time, so the two coexist for a long while —
// long enough that two entries called "CI" in the Actions list, and checks
// reading "CI / …" from either of them, is a real cost rather than a moment's
// confusion.
func TestOrchestratorsDoNotClaimACommonWorkflowName(t *testing.T) {
	spec := repospec.Default()
	files := renderMap(t, testInput(spec))

	for path, want := range map[string]string{
		".github/workflows/ci-orchestration.yml": "gt CI",
		".github/workflows/cd-orchestration.yml": "gt CD",
	} {
		content, ok := files[path]
		if !ok {
			t.Fatalf("%s was not rendered", path)
		}
		var wf struct {
			Name string `yaml:"name"`
		}
		if err := yaml.Unmarshal(content, &wf); err != nil {
			t.Fatalf("unmarshal %s: %v", path, err)
		}
		if wf.Name != want {
			t.Errorf("%s name = %q, want %q", path, wf.Name, want)
		}
	}
}

func TestRenderedGateProducesTheExpectedCheckName(t *testing.T) {
	content := renderMap(t, testInput(repospec.Default()))[".github/workflows/ci-orchestration.yml"]

	var wf struct {
		Jobs map[string]struct {
			Name string `yaml:"name"`
			Uses string `yaml:"uses"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(content, &wf); err != nil {
		t.Fatalf("unmarshal ci-orchestration.yml: %v", err)
	}

	gate, ok := wf.Jobs[repospec.GateCheckJob]
	if !ok {
		t.Fatalf("no job named %q; jobs = %v", repospec.GateCheckJob, wf.Jobs)
	}
	if gate.Uses != "" {
		t.Errorf("%s calls a reusable workflow (%s), which would prefix its check name",
			repospec.GateCheckJob, gate.Uses)
	}
	if gate.Name != repospec.GateCheckJob {
		t.Errorf("job name = %q, want %q — branch protection is configured with this string",
			gate.Name, repospec.GateCheckJob)
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

// The auto-merge caller must NAME the App secrets rather than inherit them.
//
// `secrets: inherit` is documented as working for reusable workflows "in the
// same organization or enterprise", and gt lives under a different owner than
// the repositories calling it. The bulwark stage already lost its Codecov token
// to exactly that, silently — the job kept passing while the upload stopped.
// Here the failure would be quieter still: every github-actions bump would go
// on being skipped as unmergeable, which is what it did before the App existed.
func TestAutoMergeCallerNamesTheAppSecrets(t *testing.T) {
	spec := repospec.Default()
	spec.Files = repospec.FileKeys
	content := renderMap(t, testInput(spec))[".github/workflows/dependabot-auto-merge.yml"]

	var wf struct {
		Jobs map[string]struct {
			Secrets any `yaml:"secrets"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(content, &wf); err != nil {
		t.Fatalf("unmarshal dependabot-auto-merge.yml: %v", err)
	}
	job, ok := wf.Jobs["auto-merge"]
	if !ok {
		t.Fatal("no auto-merge job rendered")
	}
	if job.Secrets == "inherit" {
		t.Fatal("auto-merge inherits secrets; an org secret will not reach gt across owners")
	}
	named, ok := job.Secrets.(map[string]any)
	if !ok {
		t.Fatalf("secrets = %#v, want a map of named secrets", job.Secrets)
	}
	for _, want := range []string{"APP_CLIENT_ID", "APP_PRIVATE_KEY"} {
		v, present := named[want]
		if !present {
			t.Errorf("auto-merge does not pass %s; workflow bumps stay unmergeable", want)
			continue
		}
		if s, _ := v.(string); !strings.Contains(s, "secrets."+want) {
			t.Errorf("%s = %q, want it to forward ${{ secrets.%s }}", want, s, want)
		}
	}
}

// Opting into the App and then not providing the secret must fail, not fall
// back. A silent fallback to GITHUB_TOKEN looks like a green run while doing
// precisely what the opt-in exists to stop: skipping every workflow-touching
// bump, forever. The whole reason `github_app` exists is that GITHUB_TOKEN
// cannot merge those, so degrading to it is degrading to the broken state.
func TestAutoMergeFailsWhenOptedInWithoutTheSecret(t *testing.T) {
	// The guard lives in the reusable workflow rather than the rendered caller,
	// so this reads the workflow gt publishes.
	body, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "reusable-dependabot-auto-merge.yml"))
	if err != nil {
		t.Skipf("reusable workflow not readable from here: %v", err)
	}
	content := string(body)
	if !strings.Contains(content, "APP_CLIENT_ID is not set") {
		t.Error("no loud failure when github_app is on but APP_CLIENT_ID is missing")
	}
	if !strings.Contains(content, "client-id:") {
		t.Error("still minting with the deprecated app-id input")
	}
	// Match the YAML input, not the word: the comment above it explains why
	// app-id is not used, and a substring check would flag its own rationale.
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "app-id:") {
			t.Errorf("app-id is deprecated by create-github-app-token and warns on every run: %q", trimmed)
		}
	}
}

// Dependabot's default is direct dependencies only, so a pin held indirectly is
// never bumped — and nothing reports a dependency that simply stops being
// updated. bulwark's go-pin module is exactly that: no .go files, so `go mod
// tidy` marks gosec and govulncheck `// indirect`, and without an allow rule
// the security scanner's own tool pins freeze in place.
func TestRenderDependabotAllowRules(t *testing.T) {
	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{{
		Ecosystem: "gomod",
		Directory: "/internal/golang/go-pin",
		Allow: []repospec.DependabotAllow{
			{DependencyName: "github.com/securego/gosec/v2", DependencyType: "all"},
			{DependencyName: "golang.org/x/vuln", DependencyType: "all"},
		},
	}}
	got := renderMap(t, testInput(spec))[".github/dependabot.yml"]

	var parsed struct {
		Updates []struct {
			Allow []struct {
				DependencyName string `yaml:"dependency-name"`
				DependencyType string `yaml:"dependency-type"`
			} `yaml:"allow"`
		} `yaml:"updates"`
	}
	if err := yaml.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal rendered dependabot.yml: %v\n%s", err, got)
	}
	allow := parsed.Updates[0].Allow
	if len(allow) != 2 {
		t.Fatalf("allow = %d rules, want 2:\n%s", len(allow), got)
	}
	if allow[0].DependencyName != "github.com/securego/gosec/v2" || allow[0].DependencyType != "all" {
		t.Errorf("rule 0 = %+v, want gosec/all", allow[0])
	}
	if allow[1].DependencyName != "golang.org/x/vuln" {
		t.Errorf("rule 1 = %+v, want golang.org/x/vuln", allow[1])
	}
}

// An entry with no allow rules must emit no `allow:` key — an empty one would
// match nothing, which Dependabot reads as "update nothing".
func TestRenderOmitsAllowWhenUnset(t *testing.T) {
	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/"}}
	if got := string(renderMap(t, testInput(spec))[".github/dependabot.yml"]); strings.Contains(got, "allow:") {
		t.Errorf("rendered an empty allow key, which would stop all updates:\n%s", got)
	}
}

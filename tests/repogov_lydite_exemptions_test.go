package tests

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/pedromvgomes/gt/internal/repogov"
	"github.com/pedromvgomes/gt/internal/repospec"
)

// lyditeExemptionsDoc mirrors the shape lydite loads, decoded strictly so a
// key lydite would reject fails here first.
type lyditeExemptionsDoc struct {
	Exemptions []repospec.LyditeExemption `yaml:"exemptions"`
}

func decodeExemptions(t *testing.T, content []byte) lyditeExemptionsDoc {
	t.Helper()
	var doc lyditeExemptionsDoc
	dec := yaml.NewDecoder(bytes.NewReader(content))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("exemptions file does not decode in lydite's shape: %v\n%s", err, content)
	}
	return doc
}

func renderedExemptions(t *testing.T, spec repospec.Spec) ([]byte, bool) {
	t.Helper()
	content, ok := renderMap(t, testInput(spec))[repogov.LyditeExemptionsFile]
	return content, ok
}

func bumpEntry(t *testing.T, doc lyditeExemptionsDoc) repospec.LyditeExemption {
	t.Helper()
	for _, e := range doc.Exemptions {
		if e.Name == repospec.DependencyBumpExemption {
			return e
		}
	}
	t.Fatalf("no %s entry in %+v", repospec.DependencyBumpExemption, doc.Exemptions)
	return repospec.LyditeExemption{}
}

// Each ecosystem contributes only the files lydite reads a version comparison
// from. A manifest lydite cannot read would pass the patch-and-minor condition
// vacuously, so listing Cargo.toml or package.json would let an edit to it
// alone merge unread.
func TestLyditeExemptionsDeriveReadableLockfilesPerEcosystem(t *testing.T) {
	cases := []struct {
		name  string
		entry repospec.DependabotEntry
		want  []string
	}{
		{"gomod at the root", repospec.DependabotEntry{Ecosystem: "gomod", Directory: "/"}, []string{"go.mod", "go.sum"}},
		{"cargo lockfile only", repospec.DependabotEntry{Ecosystem: "cargo", Directory: "/source/daemon"},
			[]string{"source/daemon/Cargo.lock"}},
		{"npm lockfile only", repospec.DependabotEntry{Ecosystem: "npm", Directory: "/web/"},
			[]string{"web/package-lock.json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := repospec.Default()
			spec.Dependabot = []repospec.DependabotEntry{tc.entry}
			content, ok := renderedExemptions(t, spec)
			if !ok {
				t.Fatalf("%s not rendered", repogov.LyditeExemptionsFile)
			}
			entry := bumpEntry(t, decodeExemptions(t, content))
			if !reflect.DeepEqual(entry.Paths, tc.want) {
				t.Errorf("paths = %v, want %v", entry.Paths, tc.want)
			}
			if entry.Versions != repospec.LyditeVersionsPatchAndMinor {
				t.Errorf("versions = %q, want %q", entry.Versions, repospec.LyditeVersionsPatchAndMinor)
			}
			for _, banned := range []string{"Cargo.toml", "package.json"} {
				if strings.Contains(strings.Join(entry.Paths, " "), banned) {
					t.Errorf("paths list the manifest %s, which lydite cannot measure: %v", banned, entry.Paths)
				}
			}
		})
	}
}

// The reason is what makes the entry reviewable, and it has to state the
// whole guarantee rather than lean on who opened the pull request.
func TestLyditeDependencyBumpReasonStatesTheCondition(t *testing.T) {
	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/"}}
	content, _ := renderedExemptions(t, spec)
	reason := bumpEntry(t, decodeExemptions(t, content)).Reason
	for _, want := range []string{"patch or minor", "licence and SCA rows are green", "whoever wrote it"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason does not say %q: %s", want, reason)
		}
	}
	if !strings.HasPrefix(string(content), "# "+repogov.ManagedMarker) {
		t.Errorf("exemptions file does not open with the managed marker:\n%s", content)
	}
}

// The body is indented two spaces per level; the encoder's default is four.
func TestLyditeExemptionsBodyIsIndentedTwoSpaces(t *testing.T) {
	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/"}}
	content, _ := renderedExemptions(t, spec)
	for _, want := range []string{
		"\nexemptions:\n  - name: dependency-version-bump\n",
		"\n    paths:\n      - go.mod\n",
	} {
		if !strings.Contains(string(content), want) {
			t.Errorf("exemptions file lacks %q:\n%s", want, content)
		}
	}
}

// Ecosystems lydite has no reader for contribute nothing, and an entry with
// no paths is one lydite refuses to load — so no file is rendered at all.
func TestLyditeExemptionsOmittedForUnreadableEcosystems(t *testing.T) {
	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{
		{Ecosystem: "pip", Directory: "/"},
		{Ecosystem: "docker", Directory: "/"},
		{Ecosystem: "terraform", Directory: "/infra"},
		{Ecosystem: "github-actions", Directory: "/"},
	}
	if content, ok := renderedExemptions(t, spec); ok {
		t.Errorf("rendered an exemptions file with nothing lydite can measure:\n%s", content)
	}
}

// Several directories of the same or different ecosystems merge into one
// entry, sorted and de-duplicated, so the same spec renders the same bytes.
func TestLyditeExemptionsMergeDirectoriesDeterministically(t *testing.T) {
	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{
		{Ecosystem: "gomod", Directory: "/tools"},
		{Ecosystem: "pip", Directory: "/"},
		{Ecosystem: "cargo", Directory: "/"},
		{Ecosystem: "gomod", Directory: "/"},
		{Ecosystem: "gomod", Directory: "/tools/"},
	}
	content, _ := renderedExemptions(t, spec)
	got := bumpEntry(t, decodeExemptions(t, content)).Paths
	want := []string{"Cargo.lock", "go.mod", "go.sum", "tools/go.mod", "tools/go.sum"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("paths = %v, want %v", got, want)
	}

	// Declaration order must not leak into the output.
	reversed := repospec.Default()
	for i := len(spec.Dependabot) - 1; i >= 0; i-- {
		reversed.Dependabot = append(reversed.Dependabot, spec.Dependabot[i])
	}
	again, _ := renderedExemptions(t, reversed)
	if !bytes.Equal(content, again) {
		t.Errorf("reordering dependabot: changed the exemptions file:\n%s\nvs\n%s", content, again)
	}
}

// exempt_dependency_bumps: false drops the derived entry. Repository entries
// still render; with none, the file is not rendered.
func TestLyditeExemptionsOptOut(t *testing.T) {
	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/"}}
	spec.Lydite.ExemptDependencyBumps = false
	if content, ok := renderedExemptions(t, spec); ok {
		t.Errorf("opt-out still rendered an exemptions file:\n%s", content)
	}

	spec.Lydite.Exemptions = []repospec.LyditeExemption{
		{Name: "docs", Reason: "Prose only.", Paths: []string{"docs/**"}},
	}
	content, ok := renderedExemptions(t, spec)
	if !ok {
		t.Fatal("repository entries alone did not render the file")
	}
	if got := decodeExemptions(t, content).Exemptions; !reflect.DeepEqual(got, spec.Lydite.Exemptions) {
		t.Errorf("exemptions = %+v, want only the repository's own %+v", got, spec.Lydite.Exemptions)
	}
}

// Repository entries render after the derived one, in declared order and
// byte-faithful: a reason is free text, and quoting it is the encoder's job.
func TestLyditeExemptionsRenderRepositoryEntries(t *testing.T) {
	spec := repospec.Default()
	spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "npm", Directory: "/"}}
	extra := []repospec.LyditeExemption{
		{Name: "docs", Reason: "Prose: no code runs from it, and \"quotes\" survive.", Paths: []string{"docs/**", "*.md"}},
		{Name: "lock-refresh", Reason: "Multi-line\nreason.\n", Paths: []string{"web/package-lock.json"},
			Versions: repospec.LyditeVersionsPatchAndMinor},
	}
	spec.Lydite.Exemptions = extra
	content, _ := renderedExemptions(t, spec)
	got := decodeExemptions(t, content).Exemptions
	if len(got) != 3 || got[0].Name != repospec.DependencyBumpExemption {
		t.Fatalf("exemptions = %+v, want the derived entry followed by two repository entries", got)
	}
	if !reflect.DeepEqual(got[1:], extra) {
		t.Errorf("repository entries = %+v, want %+v", got[1:], extra)
	}
}

// lydite reads the exemptions file from its scan root, so lydite.dir moves
// the file. The patterns stay repository-root-relative.
func TestLyditeExemptionsFollowTheScanDir(t *testing.T) {
	spec := repospec.Default()
	spec.Lydite.Dir = "source"
	spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/source/cli"}}
	files := renderMap(t, testInput(spec))
	if _, ok := files[repogov.LyditeExemptionsFile]; ok {
		t.Errorf("rendered %s at the root, where a scan of source/ never reads it", repogov.LyditeExemptionsFile)
	}
	content, ok := files["source/"+repogov.LyditeExemptionsFile]
	if !ok {
		t.Fatalf("no exemptions file under the scan dir; rendered %v", sortedKeys(files))
	}
	got := bumpEntry(t, decodeExemptions(t, content)).Paths
	if want := []string{"source/cli/go.mod", "source/cli/go.sum"}; !reflect.DeepEqual(got, want) {
		t.Errorf("paths = %v, want %v", got, want)
	}
}

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// A spec that stops asking for the file — lydite off, the derived entry off
// with nothing else declared — leaves a managed file on disk that lydite still
// obeys. It must be reported orphaned and removed, wherever lydite.dir put it.
func TestLyditeExemptionsAreOrphanedWhenNoLongerWanted(t *testing.T) {
	cases := map[string]struct {
		dir     string
		disable func(*repospec.Spec)
	}{
		"lydite disabled":      {"", func(s *repospec.Spec) { s.Lydite.Enabled = false }},
		"bumps opted out":      {"", func(s *repospec.Spec) { s.Lydite.ExemptDependencyBumps = false }},
		"disabled in scan dir": {"source", func(s *repospec.Spec) { s.Lydite.Enabled = false }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			spec := repospec.Default()
			spec.Lydite.Dir = tc.dir
			spec.Dependabot = []repospec.DependabotEntry{{Ecosystem: "gomod", Directory: "/"}}
			if err := repogov.SaveSpec(root, spec); err != nil {
				t.Fatalf("SaveSpec() error = %v", err)
			}
			if _, _, err := repogov.Sync(testOptions(root)); err != nil {
				t.Fatalf("Sync() error = %v", err)
			}
			rel := repogov.LyditeExemptionsFile
			if tc.dir != "" {
				rel = tc.dir + "/" + rel
			}
			abs := filepath.Join(root, filepath.FromSlash(rel))
			if _, err := os.Stat(abs); err != nil {
				t.Fatalf("%s should exist after the first sync: %v", rel, err)
			}

			tc.disable(&spec)
			if err := repogov.SaveSpec(root, spec); err != nil {
				t.Fatalf("SaveSpec() error = %v", err)
			}
			report, err := repogov.Check(testOptions(root))
			if err != nil {
				t.Fatalf("Check() error = %v", err)
			}
			orphaned := false
			for _, r := range report.Results {
				if r.Path == rel && r.Status == repogov.StatusOrphaned {
					orphaned = true
				}
			}
			if !orphaned {
				t.Fatalf("%s not reported orphaned; results = %v", rel, repogov.Drifted(report.Results))
			}
			if _, _, err := repogov.Sync(testOptions(root)); err != nil {
				t.Fatalf("Sync() error = %v", err)
			}
			if _, err := os.Stat(abs); !os.IsNotExist(err) {
				t.Errorf("%s still present after sync removed the orphan", rel)
			}
		})
	}
}

// exempt_dependency_bumps defaults to true, so a manifest stating true has
// stopped tracking the default: check reports it and sync strips it. An
// explicit false is a real override and survives.
func TestExemptDependencyBumpsDefaultIsStrippedAndFalseKept(t *testing.T) {
	root := t.TempDir()
	body := "dependabot:\n  - ecosystem: gomod\n    directory: /\nlydite:\n  exempt_dependency_bumps: true\n"
	if err := os.WriteFile(repospec.Path(root), []byte(body), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	report, err := repogov.Check(testOptions(root))
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !report.SpecStale {
		t.Error("a manifest restating exempt_dependency_bumps: true was not reported as spec drift")
	}
	if _, _, err := repogov.Sync(testOptions(root)); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if raw := readSpecFile(t, root); strings.Contains(raw, "exempt_dependency_bumps") {
		t.Errorf("sync kept the restated default:\n%s", raw)
	}

	spec, err := repospec.Parse([]byte("lydite:\n  exempt_dependency_bumps: false\n"), "t.yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if spec.Lydite.ExemptDependencyBumps {
		t.Fatal("an explicit false parsed as the default true")
	}
	if err := repogov.SaveSpec(root, spec); err != nil {
		t.Fatalf("SaveSpec() error = %v", err)
	}
	if raw := readSpecFile(t, root); !strings.Contains(raw, "exempt_dependency_bumps: false") {
		t.Errorf("the override was pruned as if it were a default:\n%s", raw)
	}
}

func TestLyditeExemptionsDefaultOn(t *testing.T) {
	spec, err := repospec.Parse(nil, "t.yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !spec.Lydite.ExemptDependencyBumps {
		t.Error("exempt_dependency_bumps should default to true")
	}
}

// Every entry lydite would refuse to load is refused at check time instead,
// where it fails once rather than on every pull request's lydite run.
func TestValidateRejectsBadLyditeExemptions(t *testing.T) {
	good := repospec.LyditeExemption{Name: "docs", Reason: "Prose only.", Paths: []string{"docs/**"}}
	with := func(mutate func(*repospec.LyditeExemption)) []repospec.LyditeExemption {
		e := good
		e.Paths = append([]string(nil), good.Paths...)
		mutate(&e)
		return []repospec.LyditeExemption{e}
	}
	tests := []struct {
		name    string
		mutate  func(*repospec.Spec)
		wantSub string
	}{
		{"lydite disabled", func(s *repospec.Spec) {
			s.Lydite.Enabled = false
			s.Lydite.Exemptions = []repospec.LyditeExemption{good}
		}, "while lydite is disabled"},
		{"missing name", func(s *repospec.Spec) {
			s.Lydite.Exemptions = with(func(e *repospec.LyditeExemption) { e.Name = " " })
		}, "name is required"},
		{"duplicate name", func(s *repospec.Spec) {
			s.Lydite.Exemptions = []repospec.LyditeExemption{good, good}
		}, "duplicate name"},
		{"reserved name", func(s *repospec.Spec) {
			s.Lydite.Exemptions = with(func(e *repospec.LyditeExemption) { e.Name = repospec.DependencyBumpExemption })
		}, "exempt_dependency_bumps: false"},
		{"missing reason", func(s *repospec.Spec) {
			s.Lydite.Exemptions = with(func(e *repospec.LyditeExemption) { e.Reason = "" })
		}, "reason is required"},
		{"placeholder reason", func(s *repospec.Spec) {
			s.Lydite.Exemptions = with(func(e *repospec.LyditeExemption) { e.Reason = "TODO(lydite): why?" })
		}, "placeholder"},
		{"no paths", func(s *repospec.Spec) {
			s.Lydite.Exemptions = with(func(e *repospec.LyditeExemption) { e.Paths = nil })
		}, "at least one path"},
		{"leading slash", func(s *repospec.Spec) {
			s.Lydite.Exemptions = with(func(e *repospec.LyditeExemption) { e.Paths = []string{"/docs/**"} })
		}, "leading"},
		{"empty segment", func(s *repospec.Spec) {
			s.Lydite.Exemptions = with(func(e *repospec.LyditeExemption) { e.Paths = []string{"docs//x"} })
		}, "empty path segment"},
		{"malformed pattern", func(s *repospec.Spec) {
			s.Lydite.Exemptions = with(func(e *repospec.LyditeExemption) { e.Paths = []string{"docs/[a"} })
		}, "bad path pattern"},
		{"unknown condition", func(s *repospec.Spec) {
			s.Lydite.Exemptions = with(func(e *repospec.LyditeExemption) { e.Versions = "major" })
		}, "not a condition lydite knows"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := repospec.Default()
			tc.mutate(&spec)
			err := repospec.Validate(spec)
			if err == nil {
				t.Fatalf("Validate() = nil, want an error containing %q", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("Validate() = %q, want it to contain %q", err, tc.wantSub)
			}
		})
	}

	// The reserved name is free once the derived entry is off.
	spec := repospec.Default()
	spec.Lydite.ExemptDependencyBumps = false
	spec.Lydite.Exemptions = with(func(e *repospec.LyditeExemption) { e.Name = repospec.DependencyBumpExemption })
	if err := repospec.Validate(spec); err != nil {
		t.Errorf("Validate() = %v, want the reserved name accepted with the derived entry off", err)
	}
}

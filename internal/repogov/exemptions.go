package repogov

import (
	"bytes"
	"fmt"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/pedromvgomes/gt/internal/repospec"
)

// LyditeExemptionsFile is lydite's exemptions file, relative to its scan root.
// lydite reads it from the directory it scans, so a repository that sets
// lydite.dir gets the file under that directory instead of at the root.
const LyditeExemptionsFile = ".lydite/exemptions.yml"

// dependencyBumpReason is the derived entry's reason. lydite requires one on
// every entry, and this one states the whole guarantee: the condition is
// measured off the diff, so it holds or fails the same way whoever opened the
// pull request.
const dependencyBumpReason = "A version bump of a dependency the base already declares, " +
	"in a file lydite reads a version comparison from on both sides. " +
	"A bump is exempt only when it stays within patch or minor and its licence and SCA rows are green, " +
	"whoever wrote it. Rendered by gt from the dependabot: block of .gt-repo.yaml."

// bumpPaths maps a Dependabot ecosystem to the files under its directory that
// lydite can measure a version bump in.
//
// Only files lydite reads a version comparison from, never the manifests it
// does not (Cargo.toml, package.json):
// lydite reads no comparison from those, and a path it cannot read passes the
// patch-and-minor condition vacuously, so listing a manifest would let an edit
// to it alone merge unread. A real bump also edits the manifest, which then
// stays an uncovered path and refers the pull request — the safe failure.
//
// Ecosystems absent here contribute nothing, so their bumps stay referred:
// pip, docker and terraform have no reader in lydite, and github-actions is
// absent because lydite cannot yet measure an action bump. A workflow path
// listed here would pass the patch-and-minor condition vacuously.
var bumpPaths = map[string][]string{
	"gomod": {"go.mod", "go.sum"},
	"cargo": {"Cargo.lock"},
	"npm":   {"package-lock.json"},
}

// lyditeExemptionsPath is where the exemptions file lives for a spec: under
// lydite.dir when one is set, at the repository root otherwise.
func lyditeExemptionsPath(s repospec.Spec) string {
	dir := strings.Trim(path.Clean("/"+s.Lydite.Dir), "/")
	if dir == "" {
		return LyditeExemptionsFile
	}
	return dir + "/" + LyditeExemptionsFile
}

// dependencyBumpPaths derives the patterns for the dependency-version-bump
// entry, sorted and de-duplicated so the same spec renders the same bytes.
// Exemption patterns are repository-root-relative, even under lydite.dir, so
// each Dependabot directory is joined in as written.
func dependencyBumpPaths(entries []repospec.DependabotEntry) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range entries {
		dir := strings.Trim(path.Clean("/"+e.Directory), "/")
		for _, file := range bumpPaths[e.Ecosystem] {
			p := file
			if dir != "" {
				p = dir + "/" + file
			}
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out
}

// lyditeExemptions is every entry the exemptions file carries for a spec: the
// derived dependency-version-bump entry first, when it is on and has paths,
// then the repository's own lydite.exemptions in declared order. Empty means
// the file is not rendered at all.
func lyditeExemptions(s repospec.Spec) []repospec.LyditeExemption {
	if !s.Lydite.Enabled {
		return nil
	}
	var out []repospec.LyditeExemption
	if s.Lydite.ExemptDependencyBumps {
		// An entry with no paths is one lydite refuses to load, so a spec
		// whose ecosystems lydite cannot read gets no derived entry at all.
		if paths := dependencyBumpPaths(s.Dependabot); len(paths) > 0 {
			out = append(out, repospec.LyditeExemption{
				Name:     repospec.DependencyBumpExemption,
				Reason:   dependencyBumpReason,
				Paths:    paths,
				Versions: repospec.LyditeVersionsPatchAndMinor,
			})
		}
	}
	return append(out, s.Lydite.Exemptions...)
}

type lyditeExemptionsData struct {
	// Body is the `exemptions:` document, encoded rather than templated: a
	// reason is free text, and YAML quoting is the encoder's job, not the
	// template's.
	Body string
}

func buildLyditeExemptionsData(s repospec.Spec) (lyditeExemptionsData, error) {
	doc := struct {
		Exemptions []repospec.LyditeExemption `yaml:"exemptions"`
	}{lyditeExemptions(s)}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return lyditeExemptionsData{}, fmt.Errorf("encode %s: %w", LyditeExemptionsFile, err)
	}
	if err := enc.Close(); err != nil {
		return lyditeExemptionsData{}, fmt.Errorf("encode %s: %w", LyditeExemptionsFile, err)
	}
	return lyditeExemptionsData{Body: buf.String()}, nil
}

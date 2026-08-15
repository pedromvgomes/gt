package repogov

import (
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pedromvgomes/gt/internal/repospec"
)

// skipDirs are never descended into when detecting ecosystems. They hold
// dependencies rather than declarations, and a manifest found inside one
// describes someone else's package.
var skipDirs = map[string]bool{
	".git":         true,
	".bare":        true,
	"node_modules": true,
	"vendor":       true,
	"target":       true,
	"dist":         true,
	"build":        true,
	".venv":        true,
	"venv":         true,
	".terraform":   true,
}

// manifests maps a filename to the ecosystem it implies.
var manifests = map[string]string{
	"go.mod":           "gomod",
	"Cargo.toml":       "cargo",
	"package.json":     "npm",
	"pyproject.toml":   "pip",
	"requirements.txt": "pip",
	"Dockerfile":       "docker",
}

// Detect infers Dependabot entries from what is actually in the working tree.
// It exists so `gt repo init` can seed a spec for an existing repo instead of
// making you enumerate ecosystems by hand across ten heterogeneous projects.
//
// The result is a starting point, not an authority: it is written into
// .gt-repo.yaml where it can be corrected, and never re-derived afterwards.
func Detect(workdir string) ([]repospec.DependabotEntry, error) {
	found := map[string]map[string]bool{} // ecosystem -> set of directories
	add := func(ecosystem, dir string) {
		if found[ecosystem] == nil {
			found[ecosystem] = map[string]bool{}
		}
		found[ecosystem][dir] = true
	}

	err := filepath.WalkDir(workdir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != workdir && skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		dir := dependabotDir(workdir, filepath.Dir(p))
		if ecosystem, ok := manifests[d.Name()]; ok {
			add(ecosystem, dir)
		}
		if strings.HasSuffix(d.Name(), ".tf") {
			add("terraform", dir)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Workflows imply the github-actions ecosystem, always rooted at "/".
	if _, err := os.Stat(filepath.Join(workdir, filepath.FromSlash(WorkflowDir))); err == nil {
		add("github-actions", "/")
	}

	// Both npm and cargo have a workspace concept with a single lockfile at the
	// root. Emitting a member directory edits only its manifest, leaves the
	// lockfile stale, and produces PRs that fail on a frozen-lockfile install.
	if dirs, ok := found["npm"]; ok {
		found["npm"] = collapseWorkspaces(workdir, dirs, "package.json", hasNPMWorkspaces, npmLockfiles)
	}
	if dirs, ok := found["cargo"]; ok {
		found["cargo"] = collapseWorkspaces(workdir, dirs, "Cargo.toml", hasCargoWorkspace, cargoLockfiles)
	}

	var entries []repospec.DependabotEntry
	for ecosystem, dirs := range found {
		for dir := range dirs {
			entries = append(entries, repospec.DependabotEntry{Ecosystem: ecosystem, Directory: dir})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Ecosystem != entries[j].Ecosystem {
			return entries[i].Ecosystem < entries[j].Ecosystem
		}
		return entries[i].Directory < entries[j].Directory
	})
	return entries, nil
}

// collapseWorkspaces drops manifest directories nested under a workspace root.
//
// This is the mistake wardnet documents in its own dependabot.yml: a
// yarn-workspaces monorepo has a single lockfile at the workspace root, so a
// per-member entry edits only that manifest, leaves the lockfile stale, and
// every resulting PR fails `yarn install --immutable`. Cargo workspaces share
// one Cargo.lock the same way. Detecting the root and dropping its members is
// the difference between a config that works and one that opens broken PRs
// every week.
// A nested manifest that carries its own lockfile is NOT a workspace member —
// it is a standalone package that happens to live inside the tree, and it needs
// its own entry. wardnet has exactly this shape and documents it:
// /source/end2end-tests/daemon has its own yarn.lock and is not part of the
// /source workspace. Collapsing it would silently stop updating it.
func collapseWorkspaces(
	workdir string,
	dirs map[string]bool,
	manifest string,
	isRoot func(string) bool,
	lockfiles []string,
) map[string]bool {
	abs := func(dir string, name string) string {
		return filepath.Join(workdir, filepath.FromSlash(strings.TrimPrefix(dir, "/")), name)
	}

	var roots []string
	for dir := range dirs {
		if isRoot(abs(dir, manifest)) {
			roots = append(roots, dir)
		}
	}
	if len(roots) == 0 {
		return dirs
	}

	out := map[string]bool{}
	for dir := range dirs {
		covered := false
		for _, root := range roots {
			if dir != root && isUnder(dir, root) && !hasAnyFile(abs(dir, ""), lockfiles) {
				covered = true
				break
			}
		}
		if !covered {
			out[dir] = true
		}
	}
	return out
}

var (
	npmLockfiles   = []string{"yarn.lock", "package-lock.json", "pnpm-lock.yaml", "npm-shrinkwrap.json"}
	cargoLockfiles = []string{"Cargo.lock"}
)

func hasAnyFile(dir string, names []string) bool {
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			return true
		}
	}
	return false
}

func hasNPMWorkspaces(packageJSON string) bool {
	data, err := os.ReadFile(packageJSON)
	if err != nil {
		return false
	}
	var pkg struct {
		Workspaces json.RawMessage `json:"workspaces"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return false
	}
	return len(pkg.Workspaces) > 0
}

// hasCargoWorkspace looks for a [workspace] table. This is a line scan rather
// than a TOML parse: gt has no TOML dependency, and the section header is
// unambiguous enough that adding one to answer a yes/no question is not worth
// it. A false negative only means a member keeps its own entry, which the
// human editing .gt-repo.yaml will notice.
func hasCargoWorkspace(cargoToml string) bool {
	data, err := os.ReadFile(cargoToml)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "[workspace]" || strings.HasPrefix(line, "[workspace.") {
			return true
		}
	}
	return false
}

// isUnder reports whether dir is nested inside root, both in Dependabot's
// leading-slash form.
func isUnder(dir, root string) bool {
	if root == "/" {
		return dir != "/"
	}
	return strings.HasPrefix(dir, root+"/")
}

// dependabotDir converts an absolute directory to Dependabot's
// repo-root-relative, leading-slash, forward-slash form.
func dependabotDir(workdir, dir string) string {
	rel, err := filepath.Rel(workdir, dir)
	if err != nil || rel == "." {
		return "/"
	}
	return "/" + path.Clean(filepath.ToSlash(rel))
}

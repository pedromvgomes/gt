package tests

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pedromvgomes/gt/internal/repogov"
	"github.com/pedromvgomes/gt/internal/repospec"
)

// writeFile creates a fixture file, making parent directories as needed.
func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// writeWorkflow drops a workflow file into a fixture repo.
func writeWorkflow(t *testing.T, root, name, body string) {
	t.Helper()
	writeFile(t, root, ".github/workflows/"+name, body)
}

// detected renders the result as a comparable set of "ecosystem@directory".
func detected(t *testing.T, root string) map[string]bool {
	t.Helper()
	entries, err := repogov.Detect(root)
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	out := map[string]bool{}
	for _, e := range entries {
		out[e.Ecosystem+"@"+e.Directory] = true
	}
	return out
}

func assertDetected(t *testing.T, got map[string]bool, want, notWant []string) {
	t.Helper()
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing %s; got %v", w, keys(got))
		}
	}
	for _, n := range notWant {
		if got[n] {
			t.Errorf("unexpectedly detected %s; got %v", n, keys(got))
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestDetectEcosystems(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module demo\n")
	writeFile(t, root, "daemon/Cargo.toml", "[package]\nname = \"daemon\"\n")
	writeFile(t, root, "infra/main.tf", "resource \"null_resource\" \"a\" {}\n")
	writeFile(t, root, "api/Dockerfile", "FROM debian:bookworm\n")
	writeFile(t, root, "tools/pyproject.toml", "[project]\nname = \"tools\"\n")
	writeFile(t, root, ".github/workflows/ci.yml", "name: CI\non: pull_request\n")

	assertDetected(t, detected(t, root), []string{
		"gomod@/",
		"cargo@/daemon",
		"terraform@/infra",
		"docker@/api",
		"pip@/tools",
		"github-actions@/",
	}, nil)
}

// Dependency directories describe someone else's package, not this repo's.
func TestDetectSkipsDependencyDirectories(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "package.json", `{"name":"demo"}`)
	writeFile(t, root, "node_modules/left-pad/package.json", `{"name":"left-pad"}`)
	writeFile(t, root, "vendor/example.com/dep/go.mod", "module dep\n")
	writeFile(t, root, "target/debug/Cargo.toml", "[package]\nname=\"x\"\n")

	assertDetected(t, detected(t, root), []string{"npm@/"}, []string{
		"npm@/node_modules/left-pad",
		"gomod@/vendor/example.com/dep",
		"cargo@/target/debug",
	})
}

// wardnet's documented trap: a yarn-workspaces monorepo has one lockfile at the
// root, so per-member entries leave it stale and every PR fails --immutable.
func TestDetectCollapsesNPMWorkspaceMembers(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "source/package.json", `{"name":"root","workspaces":["web","sdk"]}`)
	writeFile(t, root, "source/yarn.lock", "")
	writeFile(t, root, "source/web/package.json", `{"name":"web"}`)
	writeFile(t, root, "source/sdk/package.json", `{"name":"sdk"}`)

	assertDetected(t, detected(t, root),
		[]string{"npm@/source"},
		[]string{"npm@/source/web", "npm@/source/sdk"})
}

// pnpm is the case that checking package.json alone misses entirely: members
// are declared in a sibling pnpm-workspace.yaml and the root manifest carries
// no "workspaces" field at all. wardnet-status is exactly this shape, and
// without collapsing, Dependabot would bump page/ and worker/ individually
// while the single root pnpm-lock.yaml went stale — every resulting PR failing
// `pnpm install --frozen-lockfile`.
func TestDetectCollapsesPNPMWorkspaceMembers(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "package.json", `{"name":"root","packageManager":"pnpm@10.12.1"}`)
	writeFile(t, root, "pnpm-workspace.yaml", "packages:\n  - worker\n  - page\n")
	writeFile(t, root, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	writeFile(t, root, "page/package.json", `{"name":"page"}`)
	writeFile(t, root, "worker/package.json", `{"name":"worker"}`)

	assertDetected(t, detected(t, root),
		[]string{"npm@/"},
		[]string{"npm@/page", "npm@/worker"})
}

// The other half of wardnet's real config: a nested package with its OWN
// lockfile is standalone, not a workspace member, and needs its own entry.
// Collapsing it would silently stop updating it.
func TestDetectKeepsNestedPackageWithOwnLockfile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "source/package.json", `{"name":"root","workspaces":["web"]}`)
	writeFile(t, root, "source/yarn.lock", "")
	writeFile(t, root, "source/web/package.json", `{"name":"web"}`)
	writeFile(t, root, "source/end2end-tests/daemon/package.json", `{"name":"e2e"}`)
	writeFile(t, root, "source/end2end-tests/daemon/yarn.lock", "")

	assertDetected(t, detected(t, root),
		[]string{"npm@/source", "npm@/source/end2end-tests/daemon"},
		[]string{"npm@/source/web"})
}

// Cargo workspaces share a single Cargo.lock the same way.
func TestDetectCollapsesCargoWorkspaceMembers(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "daemon/Cargo.toml", "[workspace]\nmembers = [\"crates/*\"]\n")
	writeFile(t, root, "daemon/Cargo.lock", "")
	writeFile(t, root, "daemon/crates/api/Cargo.toml", "[package]\nname=\"api\"\n")
	writeFile(t, root, "daemon/crates/data/Cargo.toml", "[package]\nname=\"data\"\n")

	assertDetected(t, detected(t, root),
		[]string{"cargo@/daemon"},
		[]string{"cargo@/daemon/crates/api", "cargo@/daemon/crates/data"})
}

func TestDetectEmptyRepo(t *testing.T) {
	entries, err := repogov.Detect(t.TempDir())
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Detect() = %v, want none", entries)
	}
}

// Everything Detect produces must survive validation, or `gt repo init` would
// hand the user a spec that its own `check` rejects.
func TestDetectProducesValidSpec(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module demo\n")
	writeFile(t, root, "web/package.json", `{"name":"web"}`)
	writeFile(t, root, ".github/workflows/ci.yml", "name: CI\non: pull_request\n")

	entries, err := repogov.Detect(root)
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	spec := repospec.Default()
	spec.Dependabot = entries
	if err := repospec.Validate(spec); err != nil {
		t.Fatalf("detected spec failed validation: %v", err)
	}
}

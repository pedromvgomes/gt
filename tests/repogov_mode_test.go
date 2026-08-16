package tests

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pedromvgomes/gt/internal/repogov"
)

// scaffoldFile is a stand-in for a pipeline stage: gt writes the stub, the
// repository then fills it with its own build or test logic.
func scaffoldFile(path string) repogov.File {
	return repogov.File{
		Path:     path,
		Content:  []byte("# stub\non:\n  workflow_call:\njobs: {}\n"),
		Workflow: true,
		Mode:     repogov.ModeScaffold,
	}
}

func managedFile(path string) repogov.File {
	return repogov.File{
		Path:    path,
		Content: []byte("managed content\n"),
		Mode:    repogov.ModeManaged,
	}
}

func resultFor(t *testing.T, results []repogov.Result, path string) repogov.Result {
	t.Helper()
	for _, r := range results {
		if r.Path == path {
			return r
		}
	}
	t.Fatalf("no result for %s; got %v", path, results)
	return repogov.Result{}
}

func TestScaffoldIsCreatedWhenMissing(t *testing.T) {
	root := t.TempDir()
	files := []repogov.File{scaffoldFile(".github/workflows/ci-build.yml")}

	results, err := repogov.Diff(root, files, false)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	if got := resultFor(t, results, ".github/workflows/ci-build.yml"); got.Status != repogov.StatusMissing {
		t.Fatalf("status = %q, want missing", got.Status)
	}

	if _, err := repogov.Write(root, results); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".github", "workflows", "ci-build.yml")); err != nil {
		t.Fatalf("scaffold was not created: %v", err)
	}
}

// The whole point of a scaffold: once the repository has put its own build
// logic in it, gt must never report that as drift, because the offered fix
// would be to replace it with the empty stub.
func TestScaffoldWithRepoContentIsNeverDrift(t *testing.T) {
	root := t.TempDir()
	path := ".github/workflows/ci-build.yml"
	realWork := []byte("on:\n  workflow_call:\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: make build\n")

	if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), realWork, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	files := []repogov.File{scaffoldFile(path)}
	results, err := repogov.Diff(root, files, false)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	if got := resultFor(t, results, path); got.Status != repogov.StatusOK {
		t.Fatalf("status = %q, want ok — a filled-in scaffold is not drift", got.Status)
	}
	if len(repogov.Drifted(results)) != 0 {
		t.Fatalf("Drifted() = %v, want none", repogov.Drifted(results))
	}

	// And a sync must leave it byte-identical.
	if _, err := repogov.Write(root, results); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	after, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(after) != string(realWork) {
		t.Fatalf("sync rewrote the repository's own pipeline:\n%s", after)
	}
}

// Write is the irreversible path. Even if something upstream mislabels a
// scaffold as drifted or orphaned, it must not be rewritten or deleted.
func TestWriteRefusesToTouchAnExistingScaffold(t *testing.T) {
	for _, status := range []repogov.Status{repogov.StatusDrifted, repogov.StatusOrphaned} {
		t.Run(string(status), func(t *testing.T) {
			root := t.TempDir()
			path := ".github/workflows/ci-test.yml"
			realWork := []byte("jobs:\n  test:\n    steps:\n      - run: go test ./...\n")

			if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			abs := filepath.Join(root, filepath.FromSlash(path))
			if err := os.WriteFile(abs, realWork, 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}

			if _, err := repogov.Write(root, []repogov.Result{{
				Path:   path,
				Status: status,
				Mode:   repogov.ModeScaffold,
				Want:   []byte("# stub\n"),
				Got:    realWork,
			}}); err != nil {
				t.Fatalf("Write() error = %v", err)
			}

			after, err := os.ReadFile(abs)
			if err != nil {
				t.Fatalf("scaffold was deleted despite mode: %v", err)
			}
			if string(after) != string(realWork) {
				t.Fatalf("scaffold was overwritten:\n%s", after)
			}
		})
	}
}

// A managed file must still behave exactly as before.
func TestManagedFileStillDriftsAndIsRewritten(t *testing.T) {
	root := t.TempDir()
	path := ".editorconfig"
	if err := os.WriteFile(filepath.Join(root, path), []byte("hand edited\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	files := []repogov.File{managedFile(path)}
	results, err := repogov.Diff(root, files, false)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	if got := resultFor(t, results, path); got.Status != repogov.StatusDrifted {
		t.Fatalf("status = %q, want drifted", got.Status)
	}
	if _, err := repogov.Write(root, results); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	after, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(after) != "managed content\n" {
		t.Fatalf("managed file was not rewritten: %q", after)
	}
}

// Orphan detection must never target a scaffold, since an unreferenced
// scaffold holds the repository's own work. That is asserted end-to-end once
// the ci-* scaffolds join the registry; there are none in it yet, so a test
// here would only be checking an empty set.

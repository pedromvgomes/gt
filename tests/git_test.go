package tests

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pedromvgomes/gt/internal/git"
)

func TestParseDefaultBranch(t *testing.T) {
	got := git.ParseDefaultBranch(`* remote origin
  Fetch URL: git@github.com:pedromvgomes/gt.git
  HEAD branch: main
`)
	if got != "main" {
		t.Fatalf("ParseDefaultBranch() = %q, want main", got)
	}
}

func TestFindRepoRoot(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "feature", "x")
	if err := os.MkdirAll(filepath.Join(root, ".bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := git.FindRepoRoot(nested)
	if err != nil {
		t.Fatalf("FindRepoRoot() error = %v", err)
	}
	if got != root {
		t.Fatalf("FindRepoRoot() = %q, want %q", got, root)
	}
}

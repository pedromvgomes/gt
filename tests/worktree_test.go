package tests

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pedromvgomes/gt/internal/config"
	"github.com/pedromvgomes/gt/internal/git"
	"github.com/pedromvgomes/gt/internal/ui"
	"github.com/pedromvgomes/gt/internal/worktree"
)

func TestParseSpec(t *testing.T) {
	typ, name, err := worktree.ParseSpec("feature/new-thing", []string{"feature", "fix"})
	if err != nil {
		t.Fatalf("ParseSpec() error = %v", err)
	}
	if typ != "feature" || name != "new-thing" {
		t.Fatalf("ParseSpec() = %q, %q", typ, name)
	}
}

func TestParseSpecRejectsInvalidInput(t *testing.T) {
	tests := []string{"feature", "feature/", "release/x", "feature/a/b"}
	for _, spec := range tests {
		t.Run(spec, func(t *testing.T) {
			if _, _, err := worktree.ParseSpec(spec, []string{"feature", "fix"}); err == nil {
				t.Fatal("ParseSpec() succeeded, want error")
			}
		})
	}
}

func TestWorktreeAddFromSource(t *testing.T) {
	root := makeManagedRoot(t)
	runner := &fakeGitRunner{
		errs: map[string]error{
			gitDirKey(root, "show-ref", "--verify", "--quiet", "refs/heads/feature/new-thing"):          errors.New("missing"),
			gitDirKey(root, "show-ref", "--verify", "--quiet", "refs/remotes/origin/feature/new-thing"): errors.New("missing"),
		},
	}
	printer := quietUI()

	err := worktree.Add(context.Background(), runner, printer, testConfig(), worktree.AddOptions{
		Spec: "feature/new-thing",
		From: "main",
		CWD:  root,
	})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	wantLast := []string{gitDir(root), "worktree", "add", "--no-track", "-b", "feature/new-thing", filepath.Join(root, "feature", "new-thing"), "origin/main"}
	if got := runner.calls[len(runner.calls)-1]; !reflect.DeepEqual(got, wantLast) {
		t.Fatalf("last call = %#v, want %#v", got, wantLast)
	}
}

func TestWorktreeAddExistingRemoteBranch(t *testing.T) {
	root := makeManagedRoot(t)
	runner := &fakeGitRunner{
		errs: map[string]error{
			gitDirKey(root, "show-ref", "--verify", "--quiet", "refs/heads/fix/bug"): errors.New("missing"),
		},
	}
	err := worktree.Add(context.Background(), runner, quietUI(), testConfig(), worktree.AddOptions{
		Spec: "fix/bug",
		CWD:  root,
	})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	wantLast := []string{gitDir(root), "worktree", "add", "--track", "-b", "fix/bug", filepath.Join(root, "fix", "bug"), "origin/fix/bug"}
	if got := runner.calls[len(runner.calls)-1]; !reflect.DeepEqual(got, wantLast) {
		t.Fatalf("last call = %#v, want %#v", got, wantLast)
	}
}

func TestWorktreeRemovePromptsForForce(t *testing.T) {
	root := makeManagedRoot(t)
	if err := os.MkdirAll(filepath.Join(root, "feature", "stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeGitRunner{
		errs: map[string]error{
			gitDirKey(root, "worktree", "remove", filepath.Join(root, "feature", "stale")): errors.New("dirty"),
			gitDirKey(root, "branch", "-d", "feature/stale"):                               errors.New("unmerged"),
		},
	}
	var out bytes.Buffer
	printer := &ui.UI{In: strings.NewReader("y\ny\n"), Out: &out, Err: ioDiscard{}, Interactive: true}

	err := worktree.Remove(context.Background(), runner, printer, testConfig(), worktree.RemoveOptions{
		Name:         "stale",
		DeleteBranch: true,
		CWD:          root,
	})
	if err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	wantSuffix := [][]string{
		{gitDir(root), "worktree", "remove", "--force", filepath.Join(root, "feature", "stale")},
		{gitDir(root), "show-ref", "--verify", "--quiet", "refs/heads/feature/stale"},
		{gitDir(root), "branch", "-d", "feature/stale"},
		{gitDir(root), "branch", "-D", "feature/stale"},
	}
	gotSuffix := runner.calls[len(runner.calls)-4:]
	if !reflect.DeepEqual(gotSuffix, wantSuffix) {
		t.Fatalf("call suffix = %#v, want %#v", gotSuffix, wantSuffix)
	}
}

func TestWorktreeListIncludesScratchAndTypedWorktrees(t *testing.T) {
	root := makeManagedRoot(t)
	for _, dir := range []string{"scratch", filepath.Join("feature", "one"), filepath.Join("fix", "two")} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runner := &fakeGitRunner{responses: map[string]git.Result{
		"-C " + filepath.Join(root, "feature", "one") + " log -1 --format=%h %s": {Stdout: "abc123 feature commit\n"},
	}}
	var out bytes.Buffer
	printer := &ui.UI{Out: &out, Err: ioDiscard{}}

	if err := worktree.List(context.Background(), runner, printer, testConfig(), root); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{"feature/one", "fix/two", "scratch", "abc123 feature commit"} {
		if !strings.Contains(got, want) {
			t.Fatalf("List() output = %q, missing %q", got, want)
		}
	}
}

func TestWorktreeNukeDeletesBranchesWhenRequested(t *testing.T) {
	root := makeManagedRoot(t)
	if err := os.MkdirAll(filepath.Join(root, "feature", "one"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeGitRunner{}

	err := worktree.Nuke(context.Background(), runner, quietUI(), testConfig(), worktree.NukeOptions{
		DeleteBranches: true,
		CWD:            root,
	})
	if err != nil {
		t.Fatalf("Nuke() error = %v", err)
	}
	want := []string{gitDir(root), "branch", "-D", "feature/one"}
	if got := runner.calls[len(runner.calls)-1]; !reflect.DeepEqual(got, want) {
		t.Fatalf("last call = %#v, want %#v", got, want)
	}
}

func TestPruneBranchesDryRunSkipsProtectedAndActive(t *testing.T) {
	root := makeManagedRoot(t)
	runner := &fakeGitRunner{responses: map[string]git.Result{
		gitDirKey(root, "remote", "show", "origin"): {
			Stdout: "  HEAD branch: main\n",
		},
		gitDirKey(root, "for-each-ref", "--format=%(refname:short)", "refs/heads/"): {
			Stdout: "main\nscratch\nfeature/active\nfeature/orphan\n",
		},
		gitDirKey(root, "worktree", "list", "--porcelain"): {
			Stdout: "worktree /tmp/x\nbranch refs/heads/feature/active\n",
		},
	}}
	var out bytes.Buffer
	printer := &ui.UI{Out: &out, Err: ioDiscard{}}

	err := worktree.PruneBranches(context.Background(), runner, printer, worktree.PruneBranchesOptions{
		DryRun: true,
		CWD:    root,
	})
	if err != nil {
		t.Fatalf("PruneBranches() error = %v", err)
	}
	if !strings.Contains(out.String(), "Would delete branch: feature/orphan") {
		t.Fatalf("output = %q", out.String())
	}
	for _, call := range runner.calls {
		if reflect.DeepEqual(call, []string{gitDir(root), "branch", "-D", "feature/orphan"}) {
			t.Fatal("dry-run deleted orphan branch")
		}
	}
}

func makeManagedRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func testConfig() config.Config {
	return config.Config{
		WorktreeTypes: []string{"feature", "fix"},
		SSH:           config.SSH{HostAliases: map[string]string{}},
	}
}

func quietUI() *ui.UI {
	return &ui.UI{In: strings.NewReader(""), Out: ioDiscard{}, Err: ioDiscard{}, Quiet: true}
}

func gitDir(root string) string {
	return "--git-dir=" + filepath.Join(root, ".bare")
}

func gitDirKey(root string, args ...string) string {
	return strings.Join(append([]string{gitDir(root)}, args...), " ")
}

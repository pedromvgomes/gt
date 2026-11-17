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

	"github.com/pedromvgomes/gt/internal/git"
	"github.com/pedromvgomes/gt/internal/scratch"
)

func TestScratchCreatePromptsForBranchAndSource(t *testing.T) {
	root := makeManagedRoot(t)
	runner := &fakeGitRunner{responses: map[string]git.Result{
		gitDirKey(root, "remote", "show", "origin"): {
			Stdout: "  HEAD branch: main\n",
		},
	}}
	inv := testInvocation("scratch-exp\nmain\n", ioDiscard{}, ioDiscard{}, true)

	err := scratch.Run(context.Background(), runner, inv, scratch.Options{CWD: root})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []string{gitDir(root), "worktree", "add", "--no-track", "-b", "scratch-exp", filepath.Join(root, "scratch"), "origin/main"}
	if got := runner.calls[len(runner.calls)-1]; !reflect.DeepEqual(got, want) {
		t.Fatalf("last call = %#v, want %#v", got, want)
	}
}

func TestScratchExistingPrintsCommitAndHint(t *testing.T) {
	root := makeManagedRoot(t)
	path := filepath.Join(root, "scratch")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeGitRunner{responses: map[string]git.Result{
		"-C " + path + " log -1 --format=%h %s": {Stdout: "abc123 scratch commit\n"},
	}}
	var out bytes.Buffer
	inv := testInvocation("", &out, ioDiscard{}, false)

	if err := scratch.Run(context.Background(), runner, inv, scratch.Options{CWD: root}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{"scratch", "abc123 scratch commit", "gt scratch --reset"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output = %q, missing %q", got, want)
		}
	}
}

func TestScratchResetChecksOutScratchFromSelectedBranch(t *testing.T) {
	root := makeManagedRoot(t)
	path := filepath.Join(root, "scratch")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeGitRunner{responses: map[string]git.Result{
		gitDirKey(root, "remote", "show", "origin"): {
			Stdout: "  HEAD branch: main\n",
		},
	}}
	inv := testInvocation("develop\n", ioDiscard{}, ioDiscard{}, true)

	err := scratch.Run(context.Background(), runner, inv, scratch.Options{Reset: true, CWD: root})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	wantSuffix := [][]string{
		{gitDir(root), "show-ref", "--verify", "--quiet", "refs/remotes/origin/develop"},
		{"-C", path, "fetch", "origin"},
		{"-C", path, "checkout", "-B", "scratch", "origin/develop"},
	}
	gotSuffix := runner.calls[len(runner.calls)-3:]
	if !reflect.DeepEqual(gotSuffix, wantSuffix) {
		t.Fatalf("call suffix = %#v, want %#v", gotSuffix, wantSuffix)
	}
}

func TestScratchDeleteRemovesWorktreeAndBranch(t *testing.T) {
	root := makeManagedRoot(t)
	if err := os.Mkdir(filepath.Join(root, "scratch"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeGitRunner{}

	err := scratch.Run(context.Background(), runner, quietInvocation(), scratch.Options{Delete: true, CWD: root})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	wantSuffix := [][]string{
		{gitDir(root), "worktree", "remove", filepath.Join(root, "scratch")},
		{gitDir(root), "show-ref", "--verify", "--quiet", "refs/heads/scratch"},
		{gitDir(root), "branch", "-D", "scratch"},
	}
	gotSuffix := runner.calls[len(runner.calls)-3:]
	if !reflect.DeepEqual(gotSuffix, wantSuffix) {
		t.Fatalf("call suffix = %#v, want %#v", gotSuffix, wantSuffix)
	}
}

func TestScratchRejectsResetAndDeleteTogether(t *testing.T) {
	err := scratch.Run(context.Background(), &fakeGitRunner{}, quietInvocation(), scratch.Options{Reset: true, Delete: true, CWD: t.TempDir()})
	if err == nil {
		t.Fatal("Run() succeeded, want error")
	}
	if !strings.Contains(err.Error(), "--reset and --delete are mutually exclusive") {
		t.Fatalf("Run() error = %v, want the exclusive-flags message", err)
	}
}

func TestScratchCreateRejectsMissingRemoteSource(t *testing.T) {
	root := makeManagedRoot(t)
	runner := &fakeGitRunner{
		responses: map[string]git.Result{
			gitDirKey(root, "remote", "show", "origin"): {Stdout: "  HEAD branch: main\n"},
		},
		errs: map[string]error{
			gitDirKey(root, "show-ref", "--verify", "--quiet", "refs/remotes/origin/missing"): errors.New("missing"),
		},
	}
	inv := testInvocation("scratch\nmissing\n", ioDiscard{}, ioDiscard{}, true)

	err := scratch.Run(context.Background(), runner, inv, scratch.Options{CWD: root})
	if err == nil {
		t.Fatal("Run() succeeded, want error")
	}
}

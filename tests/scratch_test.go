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
	"github.com/pedromvgomes/gt/internal/ui"
)

func TestScratchCreatePromptsForBranchAndSource(t *testing.T) {
	root := makeManagedRoot(t)
	runner := &fakeGitRunner{responses: map[string]git.Result{
		gitDirKey(root, "remote", "show", "origin"): {
			Stdout: "  HEAD branch: main\n",
		},
	}}
	printer := &ui.UI{In: strings.NewReader("scratch-exp\nmain\n"), Out: ioDiscard{}, Err: ioDiscard{}, Interactive: true}

	err := scratch.Run(context.Background(), runner, printer, scratch.Options{CWD: root})
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
	printer := &ui.UI{Out: &out, Err: ioDiscard{}}

	if err := scratch.Run(context.Background(), runner, printer, scratch.Options{CWD: root}); err != nil {
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
	printer := &ui.UI{In: strings.NewReader("develop\n"), Out: ioDiscard{}, Err: ioDiscard{}, Interactive: true}

	err := scratch.Run(context.Background(), runner, printer, scratch.Options{Reset: true, CWD: root})
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

	err := scratch.Run(context.Background(), runner, quietUI(), scratch.Options{Delete: true, CWD: root})
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
	err := scratch.Run(context.Background(), &fakeGitRunner{}, quietUI(), scratch.Options{Reset: true, Delete: true, CWD: t.TempDir()})
	if err == nil {
		t.Fatal("Run() succeeded, want error")
	}
	if ui.ExitCode(err) != ui.ExitUser {
		t.Fatalf("ExitCode() = %d, want %d", ui.ExitCode(err), ui.ExitUser)
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
	printer := &ui.UI{In: strings.NewReader("scratch\nmissing\n"), Out: ioDiscard{}, Err: ioDiscard{}, Interactive: true}

	err := scratch.Run(context.Background(), runner, printer, scratch.Options{CWD: root})
	if err == nil {
		t.Fatal("Run() succeeded, want error")
	}
}

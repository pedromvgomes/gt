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
	var stdout, stderr bytes.Buffer
	inv := testInvocation("", &stdout, &stderr, false)

	err := worktree.Add(context.Background(), runner, inv, testConfig(), worktree.AddOptions{
		Spec: "feature/new-thing",
		From: "main",
		CWD:  root,
	})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	path := filepath.Join(root, "feature", "new-thing")
	wantLast := []string{gitDir(root), "worktree", "add", "--no-track", "-b", "feature/new-thing", path, "origin/main"}
	if got := runner.calls[len(runner.calls)-1]; !reflect.DeepEqual(got, wantLast) {
		t.Fatalf("last call = %#v, want %#v", got, wantLast)
	}
	for _, want := range []string{"Fetching latest from origin...", "Worktree created at " + path} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr = %q, missing %q", stderr.String(), want)
		}
	}
	// A branch cut with --no-track has no upstream until its first push.
	for _, want := range []string{"  cd " + path + "\n", "  git push -u origin feature/new-thing"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, missing %q", stdout.String(), want)
		}
	}
}

func TestWorktreeAddReturnsWorktreeAddFailure(t *testing.T) {
	root := makeManagedRoot(t)
	path := filepath.Join(root, "feature", "new-thing")
	runner := &fakeGitRunner{
		errs: map[string]error{
			gitDirKey(root, "show-ref", "--verify", "--quiet", "refs/heads/feature/new-thing"):               errors.New("missing"),
			gitDirKey(root, "worktree", "add", "--no-track", "-b", "feature/new-thing", path, "origin/main"): errors.New("worktree add failed"),
		},
	}

	err := worktree.Add(context.Background(), runner, quietInvocation(), testConfig(), worktree.AddOptions{
		Spec: "feature/new-thing",
		From: "main",
		CWD:  root,
	})
	if err == nil || !strings.Contains(err.Error(), "worktree add failed") {
		t.Fatalf("Add() error = %v, want the worktree add failure", err)
	}
}

func TestWorktreeAddExistingRemoteBranch(t *testing.T) {
	root := makeManagedRoot(t)
	runner := &fakeGitRunner{
		errs: map[string]error{
			gitDirKey(root, "show-ref", "--verify", "--quiet", "refs/heads/fix/bug"): errors.New("missing"),
		},
	}
	var stdout, stderr bytes.Buffer
	inv := testInvocation("", &stdout, &stderr, false)
	err := worktree.Add(context.Background(), runner, inv, testConfig(), worktree.AddOptions{
		Spec: "fix/bug",
		CWD:  root,
	})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	path := filepath.Join(root, "fix", "bug")
	wantLast := []string{gitDir(root), "worktree", "add", "--track", "-b", "fix/bug", path, "origin/fix/bug"}
	if got := runner.calls[len(runner.calls)-1]; !reflect.DeepEqual(got, wantLast) {
		t.Fatalf("last call = %#v, want %#v", got, wantLast)
	}
	if want := "Worktree created at " + path; !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr = %q, missing %q", stderr.String(), want)
	}
	// The branch already tracks origin/fix/bug, so there is no first push to suggest.
	if want := "  cd " + path + "\n"; !strings.Contains(stdout.String(), want) {
		t.Fatalf("stdout = %q, missing %q", stdout.String(), want)
	}
	if strings.Contains(stdout.String(), "git push -u") {
		t.Fatalf("stdout = %q, want no first-push hint for a tracking branch", stdout.String())
	}
}

// Without a terminal the source-branch question is never asked: a new branch
// with no local or remote counterpart is cut from the default branch, so
// `gt wt add` in a script or hook neither hangs nor fails.
func TestWorktreeAddNonInteractiveTakesDefaultBranch(t *testing.T) {
	root := makeManagedRoot(t)
	runner := &fakeGitRunner{
		responses: map[string]git.Result{
			gitDirKey(root, "remote", "show", "origin"): {Stdout: "  HEAD branch: develop\n"},
		},
		errs: map[string]error{
			gitDirKey(root, "show-ref", "--verify", "--quiet", "refs/heads/feature/new-thing"):          errors.New("missing"),
			gitDirKey(root, "show-ref", "--verify", "--quiet", "refs/remotes/origin/feature/new-thing"): errors.New("missing"),
		},
	}
	var stderr bytes.Buffer
	inv := testInvocation("", ioDiscard{}, &stderr, false)

	err := worktree.Add(context.Background(), runner, inv, testConfig(), worktree.AddOptions{
		Spec: "feature/new-thing",
		CWD:  root,
	})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	wantLast := []string{gitDir(root), "worktree", "add", "--no-track", "-b", "feature/new-thing", filepath.Join(root, "feature", "new-thing"), "origin/develop"}
	if got := runner.calls[len(runner.calls)-1]; !reflect.DeepEqual(got, wantLast) {
		t.Fatalf("last call = %#v, want %#v", got, wantLast)
	}
	if strings.Contains(stderr.String(), "Enter source branch") {
		t.Fatalf("stderr = %q, want no question asked without a terminal", stderr.String())
	}
}

// dirtyStatusKey is the fake-runner key for the porcelain status probe Remove
// runs against a worktree path.
func dirtyStatusKey(path string) string {
	return strings.Join([]string{"-C", path, "status", "--porcelain"}, " ")
}

func TestWorktreeRemoveCleanRemovesWithoutPrompt(t *testing.T) {
	root := makeManagedRoot(t)
	path := filepath.Join(root, "feature", "clean")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeGitRunner{} // empty status response => clean
	var out, stderr bytes.Buffer
	// Empty input at a terminal: a question asked here would end the run
	// with ErrAborted, so success proves a clean worktree asks nothing.
	inv := testInvocation("", &out, &stderr, true)

	err := worktree.Remove(context.Background(), runner, inv, testConfig(), worktree.RemoveOptions{
		Name: "clean",
		CWD:  root,
	})
	if err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	want := []string{gitDir(root), "worktree", "remove", path}
	if got := runner.calls[len(runner.calls)-1]; !reflect.DeepEqual(got, want) {
		t.Fatalf("last call = %#v, want %#v", got, want)
	}
	if !strings.Contains(stderr.String(), `Worktree "clean" removed`) {
		t.Fatalf("stderr = %q, want the removal confirmed", stderr.String())
	}
}

func TestWorktreeRemoveReturnsBranchDeleteFailure(t *testing.T) {
	root := makeManagedRoot(t)
	path := filepath.Join(root, "feature", "stale")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeGitRunner{errs: map[string]error{
		gitDirKey(root, "branch", "-D", "feature/stale"): errors.New("cannot delete branch"),
	}}
	var stderr bytes.Buffer
	inv := testInvocation("", ioDiscard{}, &stderr, false)

	err := worktree.Remove(context.Background(), runner, inv, testConfig(), worktree.RemoveOptions{
		Name:         "stale",
		DeleteBranch: true,
		Force:        true,
		CWD:          root,
	})
	if err == nil || !strings.Contains(err.Error(), "cannot delete branch") {
		t.Fatalf("Remove() error = %v, want the branch deletion failure", err)
	}
	if strings.Contains(stderr.String(), "removed") {
		t.Fatalf("stderr = %q, want no success reported over a failed branch deletion", stderr.String())
	}
}

func TestWorktreeRemoveDirtyConfirmedForces(t *testing.T) {
	root := makeManagedRoot(t)
	path := filepath.Join(root, "feature", "stale")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeGitRunner{
		responses: map[string]git.Result{
			dirtyStatusKey(path): {Stdout: " M file.go\n"},
		},
		errs: map[string]error{
			gitDirKey(root, "branch", "-d", "feature/stale"): errors.New("unmerged"),
		},
	}
	var out bytes.Buffer
	inv := testInvocation("y\n", &out, ioDiscard{}, true)

	err := worktree.Remove(context.Background(), runner, inv, testConfig(), worktree.RemoveOptions{
		Name:         "stale",
		DeleteBranch: true,
		CWD:          root,
	})
	if err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	// One confirmation covers both the dirty removal and the unmerged branch,
	// so the branch is force-deleted without a second `branch -d` attempt.
	wantSuffix := [][]string{
		{gitDir(root), "worktree", "remove", "--force", path},
		{gitDir(root), "show-ref", "--verify", "--quiet", "refs/heads/feature/stale"},
		{gitDir(root), "branch", "-D", "feature/stale"},
	}
	gotSuffix := runner.calls[len(runner.calls)-3:]
	if !reflect.DeepEqual(gotSuffix, wantSuffix) {
		t.Fatalf("call suffix = %#v, want %#v", gotSuffix, wantSuffix)
	}
}

func TestWorktreeRemoveDirtyDeclinedAborts(t *testing.T) {
	root := makeManagedRoot(t)
	path := filepath.Join(root, "feature", "stale")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeGitRunner{responses: map[string]git.Result{
		dirtyStatusKey(path): {Stdout: "?? new.go\n"},
	}}
	var out bytes.Buffer
	inv := testInvocation("n\n", &out, ioDiscard{}, true)

	err := worktree.Remove(context.Background(), runner, inv, testConfig(), worktree.RemoveOptions{
		Name: "stale",
		CWD:  root,
	})
	if err == nil {
		t.Fatal("Remove() succeeded, want error after declining")
	}
	for _, call := range runner.calls {
		if len(call) >= 3 && call[1] == "worktree" && call[2] == "remove" {
			t.Fatalf("declined removal still ran: %#v", call)
		}
	}
}

func TestWorktreeRemoveDirtyForcedSkipsPrompt(t *testing.T) {
	root := makeManagedRoot(t)
	path := filepath.Join(root, "feature", "stale")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	// No status response is configured: --force must skip the dirty probe.
	runner := &fakeGitRunner{}
	var out bytes.Buffer
	inv := testInvocation("", &out, ioDiscard{}, true)

	err := worktree.Remove(context.Background(), runner, inv, testConfig(), worktree.RemoveOptions{
		Name:  "stale",
		Force: true,
		CWD:   root,
	})
	if err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	for _, call := range runner.calls {
		if len(call) >= 1 && call[0] == "-C" {
			t.Fatalf("--force still probed status: %#v", call)
		}
	}
	want := []string{gitDir(root), "worktree", "remove", "--force", path}
	if got := runner.calls[len(runner.calls)-1]; !reflect.DeepEqual(got, want) {
		t.Fatalf("last call = %#v, want %#v", got, want)
	}
}

func TestWorktreeRemoveDirtyNonInteractiveAborts(t *testing.T) {
	root := makeManagedRoot(t)
	path := filepath.Join(root, "feature", "stale")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeGitRunner{responses: map[string]git.Result{
		dirtyStatusKey(path): {Stdout: " M file.go\n"},
	}}
	inv := quietInvocation() // no terminal

	err := worktree.Remove(context.Background(), runner, inv, testConfig(), worktree.RemoveOptions{
		Name: "stale",
		CWD:  root,
	})
	if err == nil {
		t.Fatal("Remove() succeeded, want error in non-interactive mode without --force")
	}
	for _, call := range runner.calls {
		if len(call) >= 3 && call[1] == "worktree" && call[2] == "remove" {
			t.Fatalf("non-interactive removal still ran: %#v", call)
		}
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
	inv := testInvocation("", &out, ioDiscard{}, false)

	if err := worktree.List(context.Background(), runner, inv, testConfig(), root); err != nil {
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

	err := worktree.Nuke(context.Background(), runner, quietInvocation(), testConfig(), worktree.NukeOptions{
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

func TestWorktreeNukeForceUsesForceRemoval(t *testing.T) {
	root := makeManagedRoot(t)
	path := filepath.Join(root, "feature", "one")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeGitRunner{}

	err := worktree.Nuke(context.Background(), runner, quietInvocation(), testConfig(), worktree.NukeOptions{
		Force: true,
		CWD:   root,
	})
	if err != nil {
		t.Fatalf("Nuke() error = %v", err)
	}
	want := []string{gitDir(root), "worktree", "remove", "--force", path}
	found := false
	for _, call := range runner.calls {
		if reflect.DeepEqual(call, want) {
			found = true
		}
		if reflect.DeepEqual(call, []string{gitDir(root), "worktree", "remove", path}) {
			t.Fatalf("force nuke issued a non-force remove: %#v", call)
		}
	}
	if !found {
		t.Fatalf("force remove not issued; calls = %#v", runner.calls)
	}
}

func TestWorktreeNukeSkipsDirtyWithoutForce(t *testing.T) {
	root := makeManagedRoot(t)
	path := filepath.Join(root, "feature", "one")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeGitRunner{errs: map[string]error{
		gitDirKey(root, "worktree", "remove", path): errors.New("worktree is dirty"),
	}}
	var out bytes.Buffer
	inv := testInvocation("", &out, &out, false)

	err := worktree.Nuke(context.Background(), runner, inv, testConfig(), worktree.NukeOptions{
		CWD: root,
	})
	if err == nil {
		t.Fatal("Nuke() error = nil, want a failure for the dirty worktree")
	}
	for _, call := range runner.calls {
		if len(call) > 0 && reflect.DeepEqual(call[1:], []string{"worktree", "remove", "--force", path}) {
			t.Fatalf("non-force nuke issued a force remove: %#v", call)
		}
	}
	if !strings.Contains(out.String(), "--force") {
		t.Fatalf("expected hint to re-run with --force, output = %q", out.String())
	}
	if !strings.Contains(out.String(), "Removed 0 worktree(s)") {
		t.Fatalf("expected 0 worktrees removed, output = %q", out.String())
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
	inv := testInvocation("", ioDiscard{}, &out, false)

	err := worktree.PruneBranches(context.Background(), runner, inv, worktree.PruneBranchesOptions{
		DryRun: true,
		CWD:    root,
	})
	if err != nil {
		t.Fatalf("PruneBranches() error = %v", err)
	}
	if !strings.Contains(out.String(), "Would delete branch: feature/orphan") {
		t.Fatalf("output = %q", out.String())
	}
	if !strings.Contains(out.String(), "1 branch(es) would be deleted") {
		t.Fatalf("output = %q, want the dry-run count", out.String())
	}
	for _, call := range runner.calls {
		if reflect.DeepEqual(call, []string{gitDir(root), "branch", "-D", "feature/orphan"}) {
			t.Fatal("dry-run deleted orphan branch")
		}
	}
}

func TestWorktreeNukeReturnsErrorOnPartialFailure(t *testing.T) {
	root := makeManagedRoot(t)
	bad := filepath.Join(root, "feature", "bad")
	good := filepath.Join(root, "feature", "good")
	for _, p := range []string{bad, good} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runner := &fakeGitRunner{errs: map[string]error{
		gitDirKey(root, "worktree", "remove", bad): errors.New("worktree is dirty"),
	}}

	err := worktree.Nuke(context.Background(), runner, quietInvocation(), testConfig(), worktree.NukeOptions{CWD: root})
	if err == nil || !strings.Contains(err.Error(), "1 of 2 worktrees could not be removed") {
		t.Fatalf("Nuke() error = %v, want a 1 of 2 failure", err)
	}
	removedGood := false
	for _, call := range runner.calls {
		if reflect.DeepEqual(call, []string{gitDir(root), "worktree", "remove", good}) {
			removedGood = true
		}
	}
	if !removedGood {
		t.Fatalf("the healthy worktree was not processed; calls = %#v", runner.calls)
	}
}

func TestPruneBranchesReturnsErrorOnPartialFailure(t *testing.T) {
	root := makeManagedRoot(t)
	runner := &fakeGitRunner{
		responses: map[string]git.Result{
			gitDirKey(root, "remote", "show", "origin"): {Stdout: "  HEAD branch: main\n"},
			gitDirKey(root, "for-each-ref", "--format=%(refname:short)", "refs/heads/"): {
				Stdout: "main\nfeature/a\nfeature/b\n",
			},
			gitDirKey(root, "worktree", "list", "--porcelain"): {Stdout: "worktree /tmp/x\n"},
		},
		errs: map[string]error{
			gitDirKey(root, "branch", "-D", "feature/a"): errors.New("cannot delete"),
		},
	}

	var stderr bytes.Buffer
	inv := testInvocation("", ioDiscard{}, &stderr, false)
	err := worktree.PruneBranches(context.Background(), runner, inv, worktree.PruneBranchesOptions{CWD: root})
	if err == nil || !strings.Contains(err.Error(), "1 of 2 branches could not be deleted") {
		t.Fatalf("PruneBranches() error = %v, want a 1 of 2 failure", err)
	}
	// The count reported deleted excludes the branch that failed.
	for _, want := range []string{`Failed to delete branch "feature/a"`, "Deleted 1 branch(es)"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr = %q, missing %q", stderr.String(), want)
		}
	}
	deletedB := false
	for _, call := range runner.calls {
		if reflect.DeepEqual(call, []string{gitDir(root), "branch", "-D", "feature/b"}) {
			deletedB = true
		}
	}
	if !deletedB {
		t.Fatalf("feature/b was not processed; calls = %#v", runner.calls)
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

func gitDir(root string) string {
	return "--git-dir=" + filepath.Join(root, ".bare")
}

func gitDirKey(root string, args ...string) string {
	return strings.Join(append([]string{gitDir(root)}, args...), " ")
}

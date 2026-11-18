package tests

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/pedromvgomes/gt/internal/app"
	gtcli "github.com/pedromvgomes/gt/internal/cli"
	"github.com/pedromvgomes/gt/internal/git"
	vcli "go.vipengele.org/cli/core"
)

// fakeFleetGH answers the gh calls the fleet commands make, keyed by the
// space-joined arguments, and records every call.
type fakeFleetGH struct {
	mu        sync.Mutex
	responses map[string]string
	errs      map[string]error
	calls     []string
}

func (f *fakeFleetGH) Run(_ context.Context, args ...string) ([]byte, error) {
	key := strings.Join(args, " ")
	f.mu.Lock()
	f.calls = append(f.calls, key)
	f.mu.Unlock()
	for prefix, err := range f.errs {
		if strings.HasPrefix(key, prefix) {
			return nil, err
		}
	}
	for prefix, out := range f.responses {
		if strings.HasPrefix(key, prefix) {
			return []byte(out), nil
		}
	}
	return nil, fmt.Errorf("fakeFleetGH: unexpected call %q", key)
}

func (f *fakeFleetGH) RunWithInput(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
	return f.Run(ctx, args...)
}

func (f *fakeFleetGH) called(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// fakeFleetGit fails a clone of any repository named in failClone and treats
// every other call as a success that leaves the clone empty.
type fakeFleetGit struct {
	failClone map[string]bool
}

func (f fakeFleetGit) Run(_ context.Context, _ string, args ...string) (git.Result, error) {
	if len(args) >= 2 && args[0] == "clone" {
		for repo := range f.failClone {
			if strings.Contains(args[1], repo) {
				return git.Result{}, errors.New("clone failed")
			}
		}
	}
	return git.Result{}, nil
}

const workflowPRList = `[{"number":7,"title":"bump actions/checkout from 4.1.0 to 4.2.0",` +
	`"files":[{"path":".github/workflows/ci.yml"}],"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","isDraft":false}]`

func twoPRGH() *fakeFleetGH {
	return &fakeFleetGH{
		responses: map[string]string{
			"repo list":             `[{"nameWithOwner":"o/bad"},{"nameWithOwner":"o/good"}]`,
			"pr list --repo o/good": workflowPRList,
			"pr merge":              "",
		},
		errs: map[string]error{},
	}
}

func runFleet(t *testing.T, deps gtcli.Deps, args ...string) appRun {
	t.Helper()
	env, _ := appEnv(t)
	a := vcli.New(app.Identity(), vcli.WithQuiet())
	for _, cmd := range gtcli.Commands(deps) {
		a.Add(cmd)
	}
	return runApp(t, a, env, args...)
}

func TestFleetMergePendingFailsWhenARepoCannotBeScanned(t *testing.T) {
	gh := twoPRGH()
	gh.errs["pr list --repo o/bad"] = errors.New("HTTP 403")

	got := runFleet(t, gtcli.Deps{GH: gh}, "repo", "fleet", "merge-pending", "--owner", "o", "--quiet")

	if got.code != vcli.ExitError {
		t.Fatalf("exit = %d, want %d; stderr = %q", got.code, vcli.ExitError, got.stderr)
	}
	if !strings.Contains(got.stderr, "1 operation(s) failed") {
		t.Fatalf("stderr = %q, want the failure count", got.stderr)
	}
	if !strings.Contains(got.stdout, "o/good#7") {
		t.Fatalf("stdout = %q, want the healthy repository's PR listed", got.stdout)
	}
}

func TestFleetMergePendingMergesTheRestWhenOneMergeFails(t *testing.T) {
	gh := twoPRGH()
	gh.responses["repo list"] = `[{"nameWithOwner":"o/a"},{"nameWithOwner":"o/b"}]`
	gh.responses["pr list --repo o/a"] = workflowPRList
	gh.responses["pr list --repo o/b"] = workflowPRList
	gh.errs["pr merge 7 --repo o/a"] = errors.New("merge blocked")

	got := runFleet(t, gtcli.Deps{GH: gh}, "repo", "fleet", "merge-pending", "--owner", "o", "--merge", "--quiet")

	if got.code != vcli.ExitError {
		t.Fatalf("exit = %d, want %d; stderr = %q", got.code, vcli.ExitError, got.stderr)
	}
	if !strings.Contains(got.stderr, "1 operation(s) failed") {
		t.Fatalf("stderr = %q, want the failure count", got.stderr)
	}
	if !gh.called("pr merge 7 --repo o/b") {
		t.Fatalf("the healthy PR was not merged; calls = %#v", gh.calls)
	}
}

func TestFleetMergePendingSucceedsWithoutFailures(t *testing.T) {
	gh := twoPRGH()
	gh.responses["pr list --repo o/bad"] = "[]"

	got := runFleet(t, gtcli.Deps{GH: gh}, "repo", "fleet", "merge-pending", "--owner", "o", "--merge", "--quiet")

	if got.code != vcli.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr = %q", got.code, vcli.ExitOK, got.stderr)
	}
	if !gh.called("pr merge 7 --repo o/good") {
		t.Fatalf("the eligible PR was not merged; calls = %#v", gh.calls)
	}
}

func TestFleetCheckFailsWhenARepoCannotBeSwept(t *testing.T) {
	gh := &fakeFleetGH{responses: map[string]string{
		"repo list": `[{"nameWithOwner":"o/bad"},{"nameWithOwner":"o/good"}]`,
	}}
	deps := gtcli.Deps{GH: gh, Git: fakeFleetGit{failClone: map[string]bool{"o/bad": true}}}

	got := runFleet(t, deps, "repo", "fleet", "check", "--owner", "o", "--cache-dir", t.TempDir(), "--quiet")

	if got.code != vcli.ExitError {
		t.Fatalf("exit = %d, want %d; stderr = %q", got.code, vcli.ExitError, got.stderr)
	}
	if !strings.Contains(got.stderr, "1 repository(ies) could not be swept") {
		t.Fatalf("stderr = %q, want the sweep failure count", got.stderr)
	}
}

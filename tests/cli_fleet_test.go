package tests

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pedromvgomes/gt/internal/app"
	gtcli "github.com/pedromvgomes/gt/internal/cli"
	"github.com/pedromvgomes/gt/internal/git"
	"github.com/pedromvgomes/gt/internal/repogov"
	"github.com/pedromvgomes/gt/internal/repospec"
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

// fleetCloneGit makes a clone of a repository named in governed leave a
// .gt-repo.yaml behind. A repository mapped to true is also synced, so it
// starts with no drift; one mapped to false is left with every managed file
// missing.
type fleetCloneGit struct {
	fakeFleetGit
	governed map[string]bool
}

func (f fleetCloneGit) Run(ctx context.Context, dir string, args ...string) (git.Result, error) {
	res, err := f.fakeFleetGit.Run(ctx, dir, args...)
	if err != nil || len(args) < 3 || args[0] != "clone" {
		return res, err
	}
	for repo, synced := range f.governed {
		if !strings.Contains(args[1], "/"+repo+".git") {
			continue
		}
		clone := args[2]
		if err := os.MkdirAll(filepath.Join(clone, ".git"), 0o755); err != nil {
			return res, err
		}
		if err := os.WriteFile(filepath.Join(clone, repospec.FileName), []byte("{}\n"), 0o600); err != nil {
			return res, err
		}
		if synced {
			owner, name, _ := strings.Cut(repo, "/")
			if _, _, err := repogov.Sync(repogov.Options{WorkDir: clone, RepoOwner: owner, RepoName: name}); err != nil {
				return res, err
			}
		}
	}
	return res, nil
}

// runFleetLoud runs without --quiet so Info, Success and Warning reach stderr.
func runFleetLoud(t *testing.T, deps gtcli.Deps, args ...string) appRun {
	t.Helper()
	env, _ := appEnv(t)
	a := vcli.New(app.Identity())
	for _, cmd := range gtcli.Commands(deps) {
		a.Add(cmd)
	}
	return runApp(t, a, env, args...)
}

func fleetRepoListGH(repos ...string) *fakeFleetGH {
	var items []string
	for _, r := range repos {
		items = append(items, fmt.Sprintf(`{"nameWithOwner":%q}`, r))
	}
	return &fakeFleetGH{responses: map[string]string{"repo list": "[" + strings.Join(items, ",") + "]"}}
}

func assertContains(t *testing.T, what, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("%s = %q, want it to contain %q", what, got, w)
		}
	}
}

func assertOmits(t *testing.T, what, got string, unwanted ...string) {
	t.Helper()
	for _, w := range unwanted {
		if strings.Contains(got, w) {
			t.Errorf("%s = %q, want it not to contain %q", what, got, w)
		}
	}
}

func TestFleetCheckAnnouncesTheSweepAndReportsACompliantFleet(t *testing.T) {
	cache := t.TempDir()
	deps := gtcli.Deps{GH: fleetRepoListGH("o/a", "o/b"), Git: fleetCloneGit{governed: map[string]bool{"o/b": true}}}

	got := runFleetLoud(t, deps, "repo", "fleet", "check", "--owner", "o", "--cache-dir", cache)

	if got.code != vcli.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr = %q", got.code, vcli.ExitOK, got.stderr)
	}
	assertContains(t, "stderr", got.stderr, "Sweeping 2 repositories in o (cache: "+cache+")", "Fleet is compliant.")
	assertContains(t, "stdout", got.stdout, "o/a", "not governed", "o/b", "ok")
}

func TestFleetCheckDefaultsTheCacheToTheUserCacheDirectory(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", base)
	t.Setenv("HOME", base)
	deps := gtcli.Deps{GH: fleetRepoListGH("o/a"), Git: fleetCloneGit{}}

	got := runFleetLoud(t, deps, "repo", "fleet", "check", "--owner", "o")

	want := "(cache: " + filepath.Join(base, "gt", "fleet") + ")"
	assertContains(t, "stderr", got.stderr, want)
}

func TestFleetCheckWarnsPerFailedRepoWithoutClaimingDrift(t *testing.T) {
	deps := gtcli.Deps{
		GH:  fleetRepoListGH("o/bad", "o/good"),
		Git: fleetCloneGit{fakeFleetGit: fakeFleetGit{failClone: map[string]bool{"o/bad": true}}},
	}

	got := runFleetLoud(t, deps, "repo", "fleet", "check", "--owner", "o", "--cache-dir", t.TempDir())

	if got.code != vcli.ExitError {
		t.Fatalf("exit = %d, want %d; stderr = %q", got.code, vcli.ExitError, got.stderr)
	}
	assertContains(t, "stderr", got.stderr, "o/bad", "clone failed", "1 repository(ies) could not be swept")
	assertOmits(t, "stderr", got.stderr, "need changes", "Fleet is compliant.", "are not compliant")
}

func TestFleetCheckReportsDriftWithoutCountingItAsASweepFailure(t *testing.T) {
	deps := gtcli.Deps{GH: fleetRepoListGH("o/drift"), Git: fleetCloneGit{governed: map[string]bool{"o/drift": false}}}

	got := runFleetLoud(t, deps, "repo", "fleet", "check", "--owner", "o", "--cache-dir", t.TempDir())

	if got.code != vcli.ExitError {
		t.Fatalf("exit = %d, want %d; stderr = %q", got.code, vcli.ExitError, got.stderr)
	}
	assertContains(t, "stderr", got.stderr,
		"1 repository(ies) need changes. Run 'gt repo fleet sync' to open PRs.", "1 repository(ies) are not compliant")
	assertOmits(t, "stderr", got.stderr, "could not be swept", "Fleet is compliant.")
	assertContains(t, "stdout", got.stdout, "o/drift", "file(s) to change")
}

func TestFleetSyncOpensPRsWithoutSuggestingASyncOrFailing(t *testing.T) {
	gh := fleetRepoListGH("o/drift")
	gh.responses["pr list --repo o/drift"] = "https://github.com/o/drift/pull/1\n"
	deps := gtcli.Deps{GH: gh, Git: fleetCloneGit{governed: map[string]bool{"o/drift": false}}}

	got := runFleetLoud(t, deps, "repo", "fleet", "sync", "--owner", "o", "--cache-dir", t.TempDir())

	if got.code != vcli.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr = %q", got.code, vcli.ExitOK, got.stderr)
	}
	assertContains(t, "stdout", got.stdout, "-> https://github.com/o/drift/pull/1")
	assertOmits(t, "stderr", got.stderr, "need changes", "Fleet is compliant.")
}

func TestFleetMergePendingReportsTheScanAndTheCounts(t *testing.T) {
	gh := fleetRepoListGH("o/a")
	gh.responses["pr list --repo o/a"] = `[` +
		`{"number":7,"title":"bump actions/checkout from 4.1.0 to 4.2.0",` +
		`"files":[{"path":".github/workflows/ci.yml"}],"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","isDraft":false},` +
		`{"number":8,"title":"bump actions/cache from 3.0.0 to 3.1.0",` +
		`"files":[{"path":".github/workflows/ci.yml"}],"mergeable":"CONFLICTING","mergeStateStatus":"DIRTY","isDraft":false}]`

	got := runFleetLoud(t, gtcli.Deps{GH: gh}, "repo", "fleet", "merge-pending", "--owner", "o")

	if got.code != vcli.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr = %q", got.code, vcli.ExitOK, got.stderr)
	}
	assertContains(t, "stderr", got.stderr, "Scanning 1 repositories in o...",
		"2 pending PR(s), 1 eligible. Re-run with --merge to squash-merge the eligible ones.")
	assertOmits(t, "stderr", got.stderr, "No pending workflow PRs.")
	if gh.called("pr merge") {
		t.Fatalf("a report-only run merged a PR; calls = %#v", gh.calls)
	}
}

func TestFleetMergePendingSaysSoWhenNothingIsPending(t *testing.T) {
	gh := fleetRepoListGH("o/a")
	gh.responses["pr list --repo o/a"] = "[]"

	got := runFleetLoud(t, gtcli.Deps{GH: gh}, "repo", "fleet", "merge-pending", "--owner", "o")

	if got.code != vcli.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr = %q", got.code, vcli.ExitOK, got.stderr)
	}
	assertContains(t, "stderr", got.stderr, "No pending workflow PRs.")
	assertOmits(t, "stderr", got.stderr, "pending PR(s)")
}

func TestFleetMergePendingWarnsPerFailedScanInsteadOfClaimingNothingIsPending(t *testing.T) {
	gh := fleetRepoListGH("o/bad")
	gh.errs = map[string]error{"pr list --repo o/bad": errors.New("HTTP 403")}

	got := runFleetLoud(t, gtcli.Deps{GH: gh}, "repo", "fleet", "merge-pending", "--owner", "o")

	if got.code != vcli.ExitError {
		t.Fatalf("exit = %d, want %d; stderr = %q", got.code, vcli.ExitError, got.stderr)
	}
	assertContains(t, "stderr", got.stderr, "o/bad: ", "HTTP 403", "1 operation(s) failed")
	assertOmits(t, "stderr", got.stderr, "No pending workflow PRs.", "pending PR(s)")
}

func TestFleetMergePendingAnnouncesEachMergeOutcome(t *testing.T) {
	gh := fleetRepoListGH("o/a", "o/b")
	gh.responses["pr list --repo o/a"] = workflowPRList
	gh.responses["pr list --repo o/b"] = workflowPRList
	gh.responses["pr merge"] = ""
	gh.errs = map[string]error{"pr merge 7 --repo o/a": errors.New("merge blocked")}

	got := runFleetLoud(t, gtcli.Deps{GH: gh}, "repo", "fleet", "merge-pending", "--owner", "o", "--merge")

	if got.code != vcli.ExitError {
		t.Fatalf("exit = %d, want %d; stderr = %q", got.code, vcli.ExitError, got.stderr)
	}
	assertContains(t, "stderr", got.stderr, "merge failed: ", "merge blocked", "merged o/b#7", "1 operation(s) failed")
	assertOmits(t, "stderr", got.stderr, "merged o/a#7", "Re-run with --merge")
}

package tests

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pedromvgomes/gt/internal/clone"
	"github.com/pedromvgomes/gt/internal/config"
	"github.com/pedromvgomes/gt/internal/git"
	"github.com/pedromvgomes/gt/internal/ui"
)

func TestDeriveFolder(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "https", url: "https://github.com/pedromvgomes/gt.git", want: "gt"},
		{name: "ssh", url: "git@github.com:pedromvgomes/tooling.git", want: "tooling"},
		{name: "trailing slash", url: "https://github.com/pedromvgomes/example/", want: "example"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := clone.DeriveFolder(tt.url)
			if err != nil {
				t.Fatalf("DeriveFolder() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("DeriveFolder() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateFolderRejectsUnsafePaths(t *testing.T) {
	tests := []string{"", ".", "..", "../gt", filepath.Join("..", "gt"), filepath.Clean("/tmp/gt")}
	for _, folder := range tests {
		t.Run(folder, func(t *testing.T) {
			if err := clone.ValidateFolder(folder); err == nil {
				t.Fatalf("ValidateFolder(%q) succeeded, want error", folder)
			}
		})
	}
}

func TestRunMinimalClonePath(t *testing.T) {
	t.Chdir(t.TempDir())
	runner := &fakeGitRunner{
		responses: map[string]git.Result{
			"--git-dir=.bare remote show origin": {
				Stdout: "* remote origin\n  HEAD branch: trunk\n",
			},
			"--git-dir=.bare rev-parse refs/heads/trunk": {
				Stdout: "abc123\n",
			},
		},
	}
	printer := ui.New(strings.NewReader(""), ioDiscard{}, ioDiscard{}, true, false)

	err := clone.Run(context.Background(), runner, printer, config.Config{
		WorktreeTypes: []string{"feature", "fix", "chore"},
		SSH:           config.SSH{HostAliases: map[string]string{}},
	}, clone.Options{RepoURL: "git@github.com:pedromvgomes/gt.git", NoSetupAuth: true})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	wantCalls := [][]string{
		{"clone", "--bare", "git@github.com:pedromvgomes/gt.git", filepath.Join("gt", ".bare")},
		{"--git-dir=.bare", "config", "core.bare", "false"},
		{"--git-dir=.bare", "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"},
		{"--git-dir=.bare", "fetch", "origin"},
		{"--git-dir=.bare", "remote", "show", "origin"},
		{"--git-dir=.bare", "rev-parse", "refs/heads/trunk"},
		{"--git-dir=.bare", "update-ref", "--no-deref", "HEAD", "abc123"},
		{"--git-dir=.bare", "worktree", "add", "trunk", "trunk"},
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("git calls = %#v, want %#v", runner.calls, wantCalls)
	}

	for _, path := range []string{
		filepath.Join("gt", ".git"),
		filepath.Join("gt", "feature"),
		filepath.Join("gt", "fix"),
		filepath.Join("gt", "chore"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}
}

func TestRunRejectsExistingFolder(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.Mkdir("gt", 0o755); err != nil {
		t.Fatal(err)
	}
	printer := ui.New(strings.NewReader(""), ioDiscard{}, ioDiscard{}, true, false)
	err := clone.Run(context.Background(), &fakeGitRunner{}, printer, config.Default(), clone.Options{
		RepoURL:     "git@github.com:pedromvgomes/gt.git",
		NoSetupAuth: true,
	})
	if err == nil {
		t.Fatal("Run() succeeded, want error")
	}
	if ui.ExitCode(err) != ui.ExitUser {
		t.Fatalf("ExitCode() = %d, want %d", ui.ExitCode(err), ui.ExitUser)
	}
}

func TestRunFallsBackToMainWhenRemoteShowFails(t *testing.T) {
	t.Chdir(t.TempDir())
	runner := &fakeGitRunner{
		errs: map[string]error{
			"--git-dir=.bare remote show origin": errors.New("remote show failed"),
		},
	}
	printer := ui.New(strings.NewReader(""), ioDiscard{}, ioDiscard{}, true, false)

	err := clone.Run(context.Background(), runner, printer, config.Default(), clone.Options{
		RepoURL:     "git@github.com:pedromvgomes/gt.git",
		Folder:      "repo",
		NoSetupAuth: true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	last := runner.calls[len(runner.calls)-1]
	want := []string{"--git-dir=.bare", "worktree", "add", "main", "main"}
	if !reflect.DeepEqual(last, want) {
		t.Fatalf("last call = %#v, want %#v", last, want)
	}
}

func TestHTTPToSSHUsesHostAlias(t *testing.T) {
	got, converted, err := clone.HTTPToSSH("https://github.com/pedromvgomes/gt", map[string]string{"github.com": "github-personal"})
	if err != nil {
		t.Fatalf("HTTPToSSH() error = %v", err)
	}
	if !converted {
		t.Fatal("HTTPToSSH() converted = false, want true")
	}
	if want := "git@github-personal:pedromvgomes/gt.git"; got != want {
		t.Fatalf("HTTPToSSH() = %q, want %q", got, want)
	}
}

func TestResolveRepoURLHonorsPromptNo(t *testing.T) {
	printer := &ui.UI{In: strings.NewReader("n\n"), Out: ioDiscard{}, Err: ioDiscard{}, Interactive: true}
	got, err := clone.ResolveRepoURL(printer, config.Default(), clone.Options{
		RepoURL: "https://github.com/pedromvgomes/gt.git",
	})
	if err != nil {
		t.Fatalf("ResolveRepoURL() error = %v", err)
	}
	if want := "https://github.com/pedromvgomes/gt.git"; got != want {
		t.Fatalf("ResolveRepoURL() = %q, want %q", got, want)
	}
}

func TestRunInvokesSetAuthWhenEnabled(t *testing.T) {
	t.Chdir(t.TempDir())
	runner := &fakeGitRunner{
		responses: map[string]git.Result{
			"--git-dir=.bare remote show origin": {
				Stdout: "  HEAD branch: main\n",
			},
		},
	}
	authRunner := &fakeCommandRunner{
		paths: map[string]bool{"direnv": true},
		responses: map[string]setauthResult{
			"gh api user --jq .login": {stdout: "pedromvgomes\n"},
		},
		env: map[string]string{"SHELL": "zsh"},
	}
	printer := &ui.UI{In: strings.NewReader("\n"), Out: ioDiscard{}, Err: ioDiscard{}, Interactive: true}

	err := clone.Run(context.Background(), runner, printer, config.Default(), clone.Options{
		RepoURL:    "git@github.com:pedromvgomes/gt.git",
		AuthRunner: authRunner,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join("gt", ".envrc")); err != nil {
		t.Fatalf("expected .envrc to exist: %v", err)
	}
	if !authRunner.hasCall("direnv allow " + filepath.Join(mustGetwd(t), "gt")) {
		t.Fatalf("set-auth calls = %#v, missing direnv allow", authRunner.calls)
	}
}

func TestResolveRepoURLForceSSHSkipsPrompt(t *testing.T) {
	printer := &ui.UI{In: strings.NewReader("n\n"), Out: ioDiscard{}, Err: ioDiscard{}, Interactive: true}
	got, err := clone.ResolveRepoURL(printer, config.Default(), clone.Options{
		RepoURL:  "https://github.com/pedromvgomes/gt.git",
		ForceSSH: true,
	})
	if err != nil {
		t.Fatalf("ResolveRepoURL() error = %v", err)
	}
	if want := "git@github.com:pedromvgomes/gt.git"; got != want {
		t.Fatalf("ResolveRepoURL() = %q, want %q", got, want)
	}
}

func TestResolveRepoURLNoSSHKeepsHTTPS(t *testing.T) {
	printer := &ui.UI{In: strings.NewReader(""), Out: ioDiscard{}, Err: ioDiscard{}, Interactive: false}
	got, err := clone.ResolveRepoURL(printer, config.Default(), clone.Options{
		RepoURL: "https://github.com/pedromvgomes/gt.git",
		NoSSH:   true,
	})
	if err != nil {
		t.Fatalf("ResolveRepoURL() error = %v", err)
	}
	if want := "https://github.com/pedromvgomes/gt.git"; got != want {
		t.Fatalf("ResolveRepoURL() = %q, want %q", got, want)
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}

type fakeGitRunner struct {
	calls     [][]string
	responses map[string]git.Result
	errs      map[string]error
}

func (f *fakeGitRunner) Run(_ context.Context, _ string, args ...string) (git.Result, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	key := strings.Join(args, " ")
	if len(args) == 4 && reflect.DeepEqual(args[:2], []string{"clone", "--bare"}) {
		if err := os.MkdirAll(args[3], 0o755); err != nil {
			return git.Result{}, err
		}
	}
	if err := f.errs[key]; err != nil {
		return git.Result{}, err
	}
	if result, ok := f.responses[key]; ok {
		return result, nil
	}
	return git.Result{}, nil
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}

func TestFakeRunnerCanReturnErrors(t *testing.T) {
	expected := errors.New("boom")
	runner := &fakeGitRunner{errs: map[string]error{"status": expected}}
	_, err := runner.Run(context.Background(), "", "status")
	if !errors.Is(err, expected) {
		t.Fatalf("Run() error = %v, want %v", err, expected)
	}
}

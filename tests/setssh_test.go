package tests

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pedromvgomes/gt/internal/config"
	"github.com/pedromvgomes/gt/internal/git"
	"github.com/pedromvgomes/gt/internal/setssh"
	"github.com/pedromvgomes/gt/internal/ui"
)

func TestSetSSHRemoteRewritesOriginForUser(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeGitRunner{
		responses: map[string]git.Result{
			"--git-dir=.bare remote get-url origin": {
				Stdout: "git@github-personal:pedromvgomes/gt.git\n",
			},
		},
	}
	printer := ui.New(strings.NewReader(""), ioDiscard{}, ioDiscard{}, true, false)
	cfg := config.Default()
	cfg.SSH.UserAliases = map[string]map[string]string{
		"pedromvgomes": {"github.com": "github-personal"},
		"pedro-work":   {"github.com": "github-work"},
	}

	err := setssh.Run(context.Background(), runner, printer, cfg, setssh.Options{
		CWD:  root,
		User: "pedro-work",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	wantLast := []string{
		"--git-dir=.bare", "remote", "set-url", "origin",
		"git@github-work:pedromvgomes/gt.git",
	}
	if !reflect.DeepEqual(runner.calls[len(runner.calls)-1], wantLast) {
		t.Fatalf("last call = %#v, want %#v", runner.calls[len(runner.calls)-1], wantLast)
	}
}

func TestSetSSHRemoteNoOpWhenAlreadyCorrect(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeGitRunner{
		responses: map[string]git.Result{
			"--git-dir=.bare remote get-url origin": {
				Stdout: "git@github-work:pedromvgomes/gt.git\n",
			},
		},
	}
	printer := ui.New(strings.NewReader(""), ioDiscard{}, ioDiscard{}, true, false)
	cfg := config.Default()
	cfg.SSH.UserAliases = map[string]map[string]string{
		"pedro-work": {"github.com": "github-work"},
	}

	err := setssh.Run(context.Background(), runner, printer, cfg, setssh.Options{
		CWD:  root,
		User: "pedro-work",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, call := range runner.calls {
		if len(call) >= 3 && call[1] == "remote" && call[2] == "set-url" {
			t.Fatalf("set-url was called for a no-op rewrite: %#v", call)
		}
	}
}

func TestSetSSHRemoteRejectsHTTPSOrigin(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeGitRunner{
		responses: map[string]git.Result{
			"--git-dir=.bare remote get-url origin": {
				Stdout: "https://github.com/pedromvgomes/gt.git\n",
			},
		},
	}
	printer := ui.New(strings.NewReader(""), ioDiscard{}, ioDiscard{}, true, false)
	err := setssh.Run(context.Background(), runner, printer, config.Default(), setssh.Options{
		CWD:  root,
		User: "pedro-work",
	})
	if err == nil {
		t.Fatal("Run() succeeded, want error rejecting HTTPS origin")
	}
	if ui.ExitCode(err) != ui.ExitUser {
		t.Fatalf("ExitCode = %d, want ExitUser", ui.ExitCode(err))
	}
}

func TestSetSSHRemoteFallsBackWithWarning(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeGitRunner{
		responses: map[string]git.Result{
			"--git-dir=.bare remote get-url origin": {
				Stdout: "git@github.com:pedromvgomes/gt.git\n",
			},
		},
	}
	var errBuf strings.Builder
	printer := ui.New(strings.NewReader(""), ioDiscard{}, &errBuf, true, false)
	cfg := config.Default()
	cfg.SSH.HostAliases = map[string]string{"github.com": "github-personal"}

	err := setssh.Run(context.Background(), runner, printer, cfg, setssh.Options{
		CWD:  root,
		User: "ghost",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(errBuf.String(), `user "ghost"`) {
		t.Fatalf("expected fallback warning, got %q", errBuf.String())
	}
	last := runner.calls[len(runner.calls)-1]
	want := []string{
		"--git-dir=.bare", "remote", "set-url", "origin",
		"git@github-personal:pedromvgomes/gt.git",
	}
	if !reflect.DeepEqual(last, want) {
		t.Fatalf("last call = %#v, want %#v", last, want)
	}
}

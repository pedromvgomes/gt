package scratch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pedromvgomes/gt/internal/git"
	"go.vipengele.org/cli/spec"
)

type Options struct {
	Reset  bool
	Delete bool
	CWD    string
}

func Run(ctx context.Context, runner git.Runner, inv *spec.Invocation, opts Options) error {
	if opts.Reset && opts.Delete {
		return errors.New("--reset and --delete are mutually exclusive")
	}
	root, err := rootFrom(opts.CWD)
	if err != nil {
		return err
	}
	path := filepath.Join(root, "scratch")

	switch {
	case opts.Delete:
		return deleteScratch(ctx, runner, inv, root, path)
	case opts.Reset:
		return resetScratch(ctx, runner, inv, root, path)
	case isDir(path):
		return showExisting(ctx, runner, inv, path)
	default:
		return createScratch(ctx, runner, inv, root, path)
	}
}

func createScratch(ctx context.Context, runner git.Runner, inv *spec.Invocation, root, path string) error {
	inv.Info("Fetching latest from origin...")
	if err := fetchOrigin(ctx, runner, root); err != nil {
		return err
	}
	defaultBranch, err := defaultBranch(ctx, runner, root)
	if err != nil {
		return err
	}
	branch, err := inv.Prompt("Enter branch name for scratch worktree", spec.PromptOptions{Default: "scratch"})
	if err != nil {
		return err
	}
	source, err := inv.Prompt("Enter source branch to create from", spec.PromptOptions{Default: defaultBranch})
	if err != nil {
		return err
	}
	if exists, err := remoteRefExists(ctx, runner, root, source); err != nil {
		return err
	} else if !exists {
		return fmt.Errorf("source branch %q does not exist on remote", source)
	}
	if _, err := runner.Run(ctx, "", gitDir(root), "worktree", "add", "--no-track", "-b", branch, path, "origin/"+source); err != nil {
		return err
	}
	inv.Success("Scratch worktree created at %s", path)
	_, _ = fmt.Fprintf(inv.Env.Stdout, "\nNext steps:\n  cd %s\n  git push -u origin %s  # First push sets up tracking\n", path, branch)
	return nil
}

func resetScratch(ctx context.Context, runner git.Runner, inv *spec.Invocation, root, path string) error {
	if !isDir(path) {
		return errors.New("scratch worktree does not exist. Create it first with 'gt scratch'")
	}
	inv.Info("Fetching latest from origin...")
	if err := fetchOrigin(ctx, runner, root); err != nil {
		return err
	}
	defaultBranch, err := defaultBranch(ctx, runner, root)
	if err != nil {
		return err
	}
	source, err := inv.Prompt("Enter branch to reset scratch to", spec.PromptOptions{Default: defaultBranch})
	if err != nil {
		return err
	}
	if exists, err := remoteRefExists(ctx, runner, root, source); err != nil {
		return err
	} else if !exists {
		return fmt.Errorf("branch %q does not exist on remote", source)
	}
	if _, err := runner.Run(ctx, "", "-C", path, "fetch", "origin"); err != nil {
		return err
	}
	if _, err := runner.Run(ctx, "", "-C", path, "checkout", "-B", "scratch", "origin/"+source); err != nil {
		return err
	}
	inv.Success("Scratch reset to origin/%s", source)
	_, _ = fmt.Fprintf(inv.Env.Stdout, "\nLocation: %s\n", path)
	return nil
}

func deleteScratch(ctx context.Context, runner git.Runner, inv *spec.Invocation, root, path string) error {
	if !isDir(path) {
		return errors.New("scratch worktree does not exist")
	}
	if _, err := runner.Run(ctx, "", gitDir(root), "worktree", "remove", path); err != nil {
		return err
	}
	if exists, err := localBranchExists(ctx, runner, root, "scratch"); err != nil {
		return err
	} else if exists {
		if _, err := runner.Run(ctx, "", gitDir(root), "branch", "-D", "scratch"); err != nil {
			return err
		}
	}
	inv.Success("Scratch worktree removed")
	return nil
}

func showExisting(ctx context.Context, runner git.Runner, inv *spec.Invocation, path string) error {
	inv.Info("Scratch worktree already exists at %s", path)
	_, _ = fmt.Fprintln(inv.Env.Stdout)
	_, _ = fmt.Fprintln(inv.Env.Stdout, "scratch")
	if result, err := runner.Run(ctx, "", "-C", path, "log", "-1", "--format=%h %s"); err == nil {
		commit := strings.TrimSpace(result.Stdout)
		if len(commit) > 60 {
			commit = commit[:60]
		}
		if commit != "" {
			_, _ = fmt.Fprintf(inv.Env.Stdout, "  %s\n", commit)
		}
	}
	_, _ = fmt.Fprintln(inv.Env.Stdout)
	_, _ = fmt.Fprintln(inv.Env.Stdout, "Use 'gt scratch --reset' to reset to a different branch")
	return nil
}

func fetchOrigin(ctx context.Context, runner git.Runner, root string) error {
	if _, err := runner.Run(ctx, "", gitDir(root), "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		return err
	}
	_, err := runner.Run(ctx, "", gitDir(root), "fetch", "origin", "--prune")
	return err
}

func defaultBranch(ctx context.Context, runner git.Runner, root string) (string, error) {
	result, err := runner.Run(ctx, "", gitDir(root), "remote", "show", "origin")
	if err != nil {
		return "", err
	}
	if branch := git.ParseDefaultBranch(result.Stdout); branch != "" {
		return branch, nil
	}
	return "main", nil
}

func localBranchExists(ctx context.Context, runner git.Runner, root, branch string) (bool, error) {
	_, err := runner.Run(ctx, "", gitDir(root), "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil, nil
}

func remoteRefExists(ctx context.Context, runner git.Runner, root, branch string) (bool, error) {
	_, err := runner.Run(ctx, "", gitDir(root), "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+branch)
	return err == nil, nil
}

func rootFrom(cwd string) (string, error) {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve current directory: %w", err)
		}
	}
	return git.FindRepoRoot(cwd)
}

func gitDir(root string) string {
	return "--git-dir=" + filepath.Join(root, ".bare")
}

func isDir(path string) bool {
	stat, err := os.Stat(path)
	return err == nil && stat.IsDir()
}

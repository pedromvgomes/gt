package worktree

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pedromvgomes/gt/internal/config"
	"github.com/pedromvgomes/gt/internal/git"
	"github.com/pedromvgomes/gt/internal/ui"
)

type AddOptions struct {
	Spec string
	From string
	CWD  string
}

type RemoveOptions struct {
	Name         string
	DeleteBranch bool
	CWD          string
}

type NukeOptions struct {
	DeleteBranches bool
	CWD            string
}

type PruneBranchesOptions struct {
	DryRun bool
	CWD    string
}

func Add(ctx context.Context, runner git.Runner, printer *ui.UI, cfg config.Config, opts AddOptions) error {
	typ, name, err := ParseSpec(opts.Spec, cfg.WorktreeTypes)
	if err != nil {
		return err
	}
	root, err := rootFrom(opts.CWD)
	if err != nil {
		return ui.Errorf(ui.ExitUser, "%w", err)
	}
	worktreePath := filepath.Join(root, typ, name)
	branch := typ + "/" + name
	if _, err := os.Stat(worktreePath); err == nil {
		return ui.Errorf(ui.ExitUser, "worktree already exists at %s", worktreePath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat worktree path: %w", err)
	}

	printer.Info("Fetching latest from origin...")
	if err := fetchOrigin(ctx, runner, root); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(root, typ), 0o755); err != nil {
		return fmt.Errorf("create worktree type directory: %w", err)
	}

	if exists, err := localBranchExists(ctx, runner, root, branch); err != nil {
		return err
	} else if exists {
		if _, err := runner.Run(ctx, "", gitDir(root), "worktree", "add", worktreePath, branch); err != nil {
			return err
		}
		printCreated(printer, worktreePath, "", false)
		return nil
	}

	if opts.From != "" {
		return addFromSource(ctx, runner, printer, root, worktreePath, branch, opts.From, true)
	}

	if exists, err := remoteRefExists(ctx, runner, root, branch); err != nil {
		return err
	} else if exists {
		if _, err := runner.Run(ctx, "", gitDir(root), "worktree", "add", "--track", "-b", branch, worktreePath, "origin/"+branch); err != nil {
			return err
		}
		printCreated(printer, worktreePath, "", false)
		return nil
	}

	defaultBranch, err := defaultBranch(ctx, runner, root)
	if err != nil {
		return err
	}
	source, err := printer.Prompt("Enter source branch or press enter to use", defaultBranch, false, "--from <branch> to skip the prompt")
	if err != nil {
		return err
	}
	return addFromSource(ctx, runner, printer, root, worktreePath, branch, source, true)
}

func Remove(ctx context.Context, runner git.Runner, printer *ui.UI, cfg config.Config, opts RemoveOptions) error {
	if strings.TrimSpace(opts.Name) == "" {
		return ui.Errorf(ui.ExitUser, "worktree name is required")
	}
	root, err := rootFrom(opts.CWD)
	if err != nil {
		return ui.Errorf(ui.ExitUser, "%w", err)
	}
	typ, path, ok := findNamedWorktree(root, cfg.WorktreeTypes, opts.Name)
	if !ok {
		return ui.Errorf(ui.ExitUser, "worktree %q not found", opts.Name)
	}
	branch := typ + "/" + opts.Name

	if _, err := runner.Run(ctx, "", gitDir(root), "worktree", "remove", path); err != nil {
		answer, promptErr := printer.Prompt("Force remove? [y/N]", "N", false, "")
		if promptErr != nil {
			return promptErr
		}
		if !isYes(answer) {
			return ui.Errorf(ui.ExitGeneral, "worktree removal cancelled")
		}
		if _, err := runner.Run(ctx, "", gitDir(root), "worktree", "remove", "--force", path); err != nil {
			return err
		}
	}

	if opts.DeleteBranch {
		if exists, err := localBranchExists(ctx, runner, root, branch); err != nil {
			return err
		} else if exists {
			if _, err := runner.Run(ctx, "", gitDir(root), "branch", "-d", branch); err != nil {
				answer, promptErr := printer.Prompt("Force delete? [y/N]", "N", false, "")
				if promptErr != nil {
					return promptErr
				}
				if isYes(answer) {
					if _, err := runner.Run(ctx, "", gitDir(root), "branch", "-D", branch); err != nil {
						return err
					}
				} else {
					printer.Warn("Branch %q was not deleted", branch)
				}
			}
		} else {
			printer.Warn("Branch %q does not exist locally", branch)
		}
	}

	printer.Success("Worktree %q removed", opts.Name)
	return nil
}

func List(ctx context.Context, runner git.Runner, printer *ui.UI, cfg config.Config, cwd string) error {
	root, err := rootFrom(cwd)
	if err != nil {
		return ui.Errorf(ui.ExitUser, "%w", err)
	}
	items := collectWorktrees(root, cfg.WorktreeTypes, true)
	if len(items) == 0 {
		_, _ = fmt.Fprintln(printer.Out, "No worktrees found")
		return nil
	}
	for _, item := range items {
		_, _ = fmt.Fprintln(printer.Out, item.Label)
		if result, err := runner.Run(ctx, "", "-C", item.Path, "log", "-1", "--format=%h %s"); err == nil {
			commit := strings.TrimSpace(result.Stdout)
			if len(commit) > 60 {
				commit = commit[:60]
			}
			if commit != "" {
				_, _ = fmt.Fprintf(printer.Out, "  %s\n", commit)
			}
		}
	}
	return nil
}

func Nuke(ctx context.Context, runner git.Runner, printer *ui.UI, cfg config.Config, opts NukeOptions) error {
	root, err := rootFrom(opts.CWD)
	if err != nil {
		return ui.Errorf(ui.ExitUser, "%w", err)
	}
	items := collectWorktrees(root, cfg.WorktreeTypes, false)
	removed := 0
	branches := make([]string, 0, len(items))
	for _, item := range items {
		if _, err := runner.Run(ctx, "", gitDir(root), "worktree", "remove", item.Path); err != nil {
			printer.Warn("Failed to remove worktree at %s", item.Path)
			continue
		}
		removed++
		branches = append(branches, item.Label)
	}
	if _, err := runner.Run(ctx, "", gitDir(root), "worktree", "prune"); err != nil {
		return err
	}
	if opts.DeleteBranches {
		for _, branch := range branches {
			if exists, err := localBranchExists(ctx, runner, root, branch); err != nil {
				return err
			} else if exists {
				if _, err := runner.Run(ctx, "", gitDir(root), "branch", "-D", branch); err != nil {
					printer.Warn("Failed to delete branch %q", branch)
				}
			}
		}
	}
	printer.Success("Removed %d worktree(s)", removed)
	return nil
}

func PruneBranches(ctx context.Context, runner git.Runner, printer *ui.UI, opts PruneBranchesOptions) error {
	root, err := rootFrom(opts.CWD)
	if err != nil {
		return ui.Errorf(ui.ExitUser, "%w", err)
	}
	defaultBranch, err := defaultBranch(ctx, runner, root)
	if err != nil {
		return err
	}
	result, err := runner.Run(ctx, "", gitDir(root), "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return err
	}
	worktrees, err := activeWorktreeBranches(ctx, runner, root)
	if err != nil {
		return err
	}
	protected := map[string]bool{defaultBranch: true, "main": true, "master": true, "scratch": true}
	count := 0
	for _, branch := range lines(result.Stdout) {
		if protected[branch] || worktrees[branch] {
			continue
		}
		count++
		if opts.DryRun {
			printer.Info("Would delete branch: %s", branch)
			continue
		}
		if _, err := runner.Run(ctx, "", gitDir(root), "branch", "-D", branch); err != nil {
			printer.Warn("Failed to delete branch %q", branch)
		}
	}
	if count == 0 {
		printer.Success("No orphan branches found")
	} else if opts.DryRun {
		printer.Info("%d branch(es) would be deleted. Run without --dry-run to delete.", count)
	} else {
		printer.Success("Deleted %d branch(es)", count)
	}
	return nil
}

func ParseSpec(spec string, types []string) (string, string, error) {
	typ, name, ok := strings.Cut(spec, "/")
	if !ok || typ == "" || name == "" || strings.Contains(name, "/") {
		return "", "", ui.Errorf(ui.ExitUser, "expected worktree name as <type>/<name>")
	}
	for _, valid := range types {
		if typ == valid {
			return typ, name, nil
		}
	}
	return "", "", ui.Errorf(ui.ExitUser, "invalid worktree type %q", typ)
}

func addFromSource(ctx context.Context, runner git.Runner, printer *ui.UI, root, path, branch, source string, newBranch bool) error {
	if exists, err := remoteRefExists(ctx, runner, root, source); err != nil {
		return err
	} else if !exists {
		return ui.Errorf(ui.ExitUser, "source branch %q does not exist on remote", source)
	}
	if _, err := runner.Run(ctx, "", gitDir(root), "worktree", "add", "--no-track", "-b", branch, path, "origin/"+source); err != nil {
		return err
	}
	printCreated(printer, path, branch, newBranch)
	return nil
}

func printCreated(printer *ui.UI, path, branch string, newBranch bool) {
	printer.Success("Worktree created at %s", path)
	_, _ = fmt.Fprintf(printer.Out, "\nNext steps:\n  cd %s\n", path)
	if newBranch {
		_, _ = fmt.Fprintf(printer.Out, "  git push -u origin %s  # First push sets up tracking\n", branch)
	}
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

func activeWorktreeBranches(ctx context.Context, runner git.Runner, root string) (map[string]bool, error) {
	result, err := runner.Run(ctx, "", gitDir(root), "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	active := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(result.Stdout))
	for scanner.Scan() {
		line := scanner.Text()
		if branch, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
			active[branch] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan worktree list: %w", err)
	}
	return active, nil
}

type item struct {
	Label string
	Path  string
}

func collectWorktrees(root string, types []string, includeScratch bool) []item {
	items := []item{}
	if includeScratch && isDir(filepath.Join(root, "scratch")) {
		items = append(items, item{Label: "scratch", Path: filepath.Join(root, "scratch")})
	}
	for _, typ := range types {
		parent := filepath.Join(root, typ)
		entries, err := os.ReadDir(parent)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				items = append(items, item{Label: typ + "/" + entry.Name(), Path: filepath.Join(parent, entry.Name())})
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Label < items[j].Label })
	return items
}

func findNamedWorktree(root string, types []string, name string) (string, string, bool) {
	for _, typ := range types {
		path := filepath.Join(root, typ, name)
		if isDir(path) {
			return typ, path, true
		}
	}
	return "", "", false
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

func isYes(answer string) bool {
	answer = strings.TrimSpace(strings.ToLower(answer))
	return answer == "y" || answer == "yes"
}

func lines(value string) []string {
	raw := strings.Split(value, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

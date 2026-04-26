package clone

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/pedromvgomes/gt/internal/config"
	"github.com/pedromvgomes/gt/internal/git"
	"github.com/pedromvgomes/gt/internal/setauth"
	"github.com/pedromvgomes/gt/internal/ui"
)

type Options struct {
	RepoURL     string
	Folder      string
	ForceSSH    bool
	NoSSH       bool
	NoSetupAuth bool
	User        string
	AuthRunner  setauth.Runner
}

func Run(ctx context.Context, runner git.Runner, printer *ui.UI, cfg config.Config, opts Options) error {
	if strings.TrimSpace(opts.RepoURL) == "" {
		return ui.Errorf(ui.ExitUser, "repository URL is required")
	}
	if opts.ForceSSH && opts.NoSSH {
		return ui.Errorf(ui.ExitUser, "--ssh and --no-ssh are mutually exclusive")
	}
	repoURL, err := ResolveRepoURL(printer, cfg, opts)
	if err != nil {
		return err
	}
	folder := opts.Folder
	if folder == "" {
		derived, err := DeriveFolder(repoURL)
		if err != nil {
			return err
		}
		folder = derived
		printer.Info("Using folder name: %s", folder)
	}
	if err := ValidateFolder(folder); err != nil {
		return err
	}
	if _, err := os.Stat(folder); err == nil {
		return ui.Errorf(ui.ExitUser, "folder %q already exists", folder)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat folder %s: %w", folder, err)
	}

	barePath := filepath.Join(folder, ".bare")
	printer.Info("Cloning %s into %s...", repoURL, barePath)
	if _, err := runner.Run(ctx, "", "clone", "--bare", repoURL, barePath); err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(folder, ".git"), []byte("gitdir: ./.bare\n"), 0o644); err != nil {
		return fmt.Errorf("write .git pointer: %w", err)
	}

	commands := [][]string{
		{"--git-dir=.bare", "config", "core.bare", "false"},
		{"--git-dir=.bare", "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"},
		{"--git-dir=.bare", "fetch", "origin"},
	}
	for _, args := range commands {
		if _, err := runner.Run(ctx, folder, args...); err != nil {
			return err
		}
	}

	defaultBranch := "main"
	if result, err := runner.Run(ctx, folder, "--git-dir=.bare", "remote", "show", "origin"); err == nil {
		if parsed := git.ParseDefaultBranch(result.Stdout); parsed != "" {
			defaultBranch = parsed
		}
	} else {
		printer.Warn("Could not detect default branch, using 'main'")
	}
	printer.Info("Default branch: %s", defaultBranch)

	if result, err := runner.Run(ctx, folder, "--git-dir=.bare", "rev-parse", "refs/heads/"+defaultBranch); err == nil {
		commit := strings.TrimSpace(result.Stdout)
		if commit != "" {
			if _, err := runner.Run(ctx, folder, "--git-dir=.bare", "update-ref", "--no-deref", "HEAD", commit); err != nil {
				return err
			}
		}
	}

	printer.Info("Creating %s worktree...", defaultBranch)
	if _, err := runner.Run(ctx, folder, "--git-dir=.bare", "worktree", "add", defaultBranch, defaultBranch); err != nil {
		return err
	}

	for _, typ := range cfg.WorktreeTypes {
		if err := os.MkdirAll(filepath.Join(folder, typ), 0o755); err != nil {
			return fmt.Errorf("create %s worktree directory: %w", typ, err)
		}
	}

	printer.Success("Repository cloned successfully")
	_, _ = fmt.Fprintf(printer.Out, "\nStructure:\n  %s/\n  |-- .bare/\n  |-- .git\n  |-- %s/\n", folder, defaultBranch)
	for _, typ := range cfg.WorktreeTypes {
		_, _ = fmt.Fprintf(printer.Out, "  |-- %s/\n", typ)
	}
	_, _ = fmt.Fprintf(printer.Out, "\nNext steps:\n  cd %s/%s\n", folder, defaultBranch)
	if !opts.NoSetupAuth {
		authRunner := opts.AuthRunner
		if authRunner == nil {
			authRunner = setauth.ExecRunner{}
		}
		if err := setauth.Run(ctx, authRunner, printer, setauth.Options{
			CWD:  folder,
			User: opts.User,
		}); err != nil {
			return err
		}
	}
	return nil
}

func ResolveRepoURL(printer *ui.UI, cfg config.Config, opts Options) (string, error) {
	if opts.NoSSH {
		return opts.RepoURL, nil
	}
	sshURL, converted, err := HTTPToSSH(opts.RepoURL, cfg.SSH.HostAliases)
	if err != nil {
		return "", err
	}
	if !converted {
		return opts.RepoURL, nil
	}
	if opts.ForceSSH {
		return sshURL, nil
	}
	printer.Warn("HTTPS repository URL detected; equivalent SSH URL: %s", sshURL)
	answer, err := printer.Prompt("Use SSH instead? [Y/n]", "Y", false, "")
	if err != nil {
		return "", err
	}
	if isYes(answer) {
		return sshURL, nil
	}
	return opts.RepoURL, nil
}

func HTTPToSSH(raw string, aliases map[string]string) (string, bool, error) {
	trimmed := strings.TrimSpace(raw)
	lower := strings.ToLower(trimmed)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return raw, false, nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", false, ui.Errorf(ui.ExitUser, "parse repository URL: %v", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return raw, false, nil
	}
	host := parsed.Hostname()
	if host == "" {
		return "", false, ui.Errorf(ui.ExitUser, "repository URL host is required")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false, ui.Errorf(ui.ExitUser, "HTTPS repository URL must look like https://<host>/<owner>/<repo>")
	}
	repo := strings.TrimSuffix(parts[1], ".git")
	if repo == "" {
		return "", false, ui.Errorf(ui.ExitUser, "repository URL repo name is required")
	}
	alias := host
	if aliases != nil && aliases[host] != "" {
		alias = aliases[host]
	}
	return fmt.Sprintf("git@%s:%s/%s.git", alias, parts[0], repo), true, nil
}

func isYes(answer string) bool {
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "" || answer == "y" || answer == "yes"
}

func DeriveFolder(raw string) (string, error) {
	raw = strings.TrimSpace(strings.TrimSuffix(raw, "/"))
	if raw == "" {
		return "", ui.Errorf(ui.ExitUser, "repository URL is required")
	}
	trimmed := strings.TrimSuffix(raw, ".git")
	if parsed, err := url.Parse(trimmed); err == nil && parsed.Host != "" {
		name := strings.Trim(filepath.Base(parsed.Path), "/")
		if name != "" && name != "." {
			return name, nil
		}
	}
	name := filepath.Base(trimmed)
	if idx := strings.LastIndex(name, ":"); idx >= 0 {
		name = name[idx+1:]
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == "/" {
		return "", ui.Errorf(ui.ExitUser, "could not derive folder name from %q", raw)
	}
	return name, nil
}

func ValidateFolder(folder string) error {
	if strings.TrimSpace(folder) == "" {
		return ui.Errorf(ui.ExitUser, "folder cannot be empty")
	}
	if filepath.IsAbs(folder) {
		return ui.Errorf(ui.ExitUser, "folder must be relative")
	}
	clean := filepath.Clean(folder)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return ui.Errorf(ui.ExitUser, "folder must stay within the current directory")
	}
	return nil
}

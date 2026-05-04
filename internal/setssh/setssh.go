package setssh

import (
	"context"
	"fmt"
	"strings"

	"github.com/pedromvgomes/gt/internal/clone"
	"github.com/pedromvgomes/gt/internal/config"
	"github.com/pedromvgomes/gt/internal/git"
	"github.com/pedromvgomes/gt/internal/ui"
)

type Options struct {
	CWD  string
	User string
}

func Run(ctx context.Context, runner git.Runner, printer *ui.UI, cfg config.Config, opts Options) error {
	root, err := git.FindRepoRoot(opts.CWD)
	if err != nil {
		return err
	}

	result, err := runner.Run(ctx, root, "--git-dir=.bare", "remote", "get-url", "origin")
	if err != nil {
		return err
	}
	current := strings.TrimSpace(result.Stdout)
	if current == "" {
		return ui.Errorf(ui.ExitGeneral, "origin URL is empty")
	}
	host, owner, repo, ok := clone.ParseSSHURL(current)
	if !ok {
		return ui.Errorf(ui.ExitUser, "origin %q is not an SSH URL (git@host:owner/repo[.git]); switch to SSH first", current)
	}

	canonical := clone.CanonicalHost(cfg.SSH, host)
	alias, err := clone.ResolveAlias(printer, cfg.SSH, canonical, opts.User)
	if err != nil {
		return err
	}
	next := fmt.Sprintf("git@%s:%s/%s.git", alias, owner, repo)
	if next == current {
		printer.Info("origin already points to %s", current)
		return nil
	}
	if _, err := runner.Run(ctx, root, "--git-dir=.bare", "remote", "set-url", "origin", next); err != nil {
		return err
	}
	printer.Success("origin: %s -> %s", current, next)
	return nil
}

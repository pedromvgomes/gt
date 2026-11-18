package setssh

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pedromvgomes/gt/internal/clone"
	"github.com/pedromvgomes/gt/internal/config"
	"github.com/pedromvgomes/gt/internal/git"
	"go.vipengele.org/cli/spec"
)

type Options struct {
	CWD  string
	User string
}

func Run(ctx context.Context, runner git.Runner, inv *spec.Invocation, cfg config.Config, opts Options) error {
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
		return errors.New("origin URL is empty")
	}
	host, owner, repo, ok := clone.ParseSSHURL(current)
	if !ok {
		return fmt.Errorf("origin %q is not an SSH URL (git@host:owner/repo[.git]); switch to SSH first", current)
	}

	canonical := clone.CanonicalHost(cfg.SSH, host)
	alias, err := clone.ResolveAlias(inv, cfg.SSH, canonical, opts.User)
	if err != nil {
		return err
	}
	next := fmt.Sprintf("git@%s:%s/%s.git", alias, owner, repo)
	if next == current {
		inv.Info("origin already points to %s", current)
		return nil
	}
	if _, err := runner.Run(ctx, root, "--git-dir=.bare", "remote", "set-url", "origin", next); err != nil {
		return err
	}
	inv.Success("origin: %s -> %s", current, next)
	return nil
}

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
	host, owner, repo, ok := parseSSHURL(current)
	if !ok {
		return ui.Errorf(ui.ExitUser, "origin %q is not an SSH URL (git@host:owner/repo[.git]); switch to SSH first", current)
	}

	canonical := canonicalHost(cfg.SSH, host)
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

func parseSSHURL(raw string) (host, owner, repo string, ok bool) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "git@") {
		return "", "", "", false
	}
	rest := strings.TrimPrefix(raw, "git@")
	colonIdx := strings.Index(rest, ":")
	if colonIdx <= 0 {
		return "", "", "", false
	}
	host = rest[:colonIdx]
	path := strings.TrimSuffix(rest[colonIdx+1:], ".git")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", false
	}
	return host, parts[0], parts[1], true
}

// canonicalHost reverses the alias maps so a URL like git@github-personal:...
// resolves back to its canonical host (e.g. github.com) for the next lookup.
// Returns the input host unchanged if no mapping points to it.
func canonicalHost(ssh config.SSH, host string) string {
	for h, alias := range ssh.HostAliases {
		if alias == host {
			return h
		}
	}
	for _, hosts := range ssh.UserAliases {
		for h, alias := range hosts {
			if alias == host {
				return h
			}
		}
	}
	return host
}

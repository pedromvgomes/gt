// Package cli is gt's command tree: every command gt declares, described with
// go.vipengele.org/cli/spec and run by the App internal/app builds. The root,
// its standard flags and the "config" and "version" commands belong to that
// App, not to this package.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/pedromvgomes/gt/internal/config"
	"github.com/pedromvgomes/gt/internal/git"
	"github.com/pedromvgomes/gt/internal/repogov"
	vcli "go.vipengele.org/cli/core"
	"go.vipengele.org/cli/spec"
)

// Deps is what the command tree needs from the binary that runs it.
type Deps struct {
	// Version is the version the binary was stamped with, as the release
	// ldflags set it: "3.0.0" without a leading v, or "dev". repogov records
	// it in .gt-repo.yaml and derives the major tag of the reusable workflows
	// from it, so it is passed on unnormalised.
	Version string

	// GH is the gh client the fleet commands run with. Nil means the real
	// `gh`, reusing the user's own credentials.
	GH repogov.GH
	// Git is the git runner the fleet sweep clones and pushes with. Nil means
	// the real `git`.
	Git git.Runner
}

func (d Deps) gh() repogov.GH {
	if d.GH != nil {
		return d.GH
	}
	return repogov.ExecGH{}
}

func (d Deps) git() git.Runner {
	if d.Git != nil {
		return d.Git
	}
	return git.ExecRunner{}
}

// Commands returns gt's top-level commands, each with its subcommands. Every
// call builds a fresh tree.
func Commands(d Deps) []*spec.Command {
	return []*spec.Command{
		newCloneCommand(),
		newWorktreeCommand(),
		newScratchCommand(),
		newSetAuthCommand(),
		newSetSSHRemoteCommand(),
		newSetupCommand(),
		newRepoCommand(d),
	}
}

// workDir is the invocation's working directory, or the process's when the
// invocation names none.
func workDir(inv *spec.Invocation) (string, error) {
	if inv.Env.WorkDir != "" {
		return inv.Env.WorkDir, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve current directory: %w", err)
	}
	return cwd, nil
}

// configFrom returns the configuration the App loaded for the running command.
// Every gt command runs with configuration loaded, so its absence means the
// command is run by something other than gt's App.
func configFrom(ctx context.Context) (config.Config, error) {
	res, ok := vcli.ConfigFrom[config.Config](ctx)
	if !ok {
		return config.Config{}, errors.New("gt configuration is not loaded")
	}
	return res.Value(), nil
}

// exclusive is a FlagGroup allowing at most one of the named flags.
func exclusive(names ...string) spec.FlagGroup {
	return spec.FlagGroup{Kind: spec.MutuallyExclusive, Flags: names}
}

func parseNames(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if name := strings.TrimSpace(p); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// Package app builds gt's App: its identity, build information, standard
// flags, configuration, directory layout and self-update, around the command
// tree of internal/cli.
package app

import (
	"context"

	gtcli "github.com/pedromvgomes/gt/internal/cli"
	"github.com/pedromvgomes/gt/internal/config"
	"github.com/pedromvgomes/gt/internal/selfupdate"
	vcli "go.vipengele.org/cli/core"
	"go.vipengele.org/cli/core/buildinfo"
	"go.vipengele.org/cli/spec"
	"go.vipengele.org/update"
	"go.vipengele.org/update/updatecmd"
)

// PathsLayout is the directory layout of every directory gt resolves, the
// App's and the updater's alike. XDG on every operating system keeps the
// configuration at $XDG_CONFIG_HOME/gt, or ~/.config/gt, on macOS as well as on
// Linux.
const PathsLayout = selfupdate.PathsLayout

// Identity is gt's identity as the vipengele libraries know it. Its EnvPrefix
// names the GT_* variables: GT_HOME, GT_NO_COLOR and the per-key configuration
// overrides.
func Identity() spec.Identity {
	return selfupdate.Identity()
}

// BuildInfo is what the release build stamps into package main. Version is
// stamped without a leading v, as "3.0.0", or left at "dev".
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// Version returns the stamped version as the vipengele libraries expect it:
// with a leading v when the stamp is a semantic version without one, so that
// "1.2.3" is the release "v1.2.3". Anything else, "dev" included, is returned
// unchanged.
func Version(stamped string) string {
	return selfupdate.Version(stamped)
}

// Options adjusts the App New builds. The zero Options is the production App.
type Options struct {
	// Update, when set, is applied to the update Config before anything uses
	// it, so it reaches both "gt update" and the background check. Tests set
	// its seams: HTTPClient, StateDir, Now, IsTerminal and Getenv.
	Update func(*update.Config)
}

// New returns gt's App, holding every command of internal/cli and the
// "update" command, with the background update check around every command.
func New(b BuildInfo, opts Options) *vcli.App {
	base := selfupdate.Base(b.Version)
	if opts.Update != nil {
		opts.Update(&base)
	}
	// A post-run hook reads nothing of its Config: it finishes whichever check
	// a pre-run hook stored in the context, so base's finishes the check of
	// every run, whatever the run's own Config. An invalid base yields a nil
	// hook, which WithPostRun ignores, and the pre-run hook then starts no
	// check either.
	_, finishCheck, _ := base.UpdateCheckHooks()

	a := vcli.New(Identity(),
		vcli.WithBuildInfo(b.info()),
		vcli.WithVerbose(),
		vcli.WithQuiet(),
		vcli.WithNoColor(),
		vcli.WithLogging(),
		vcli.WithCompletion(),
		vcli.WithPathsLayout(PathsLayout),
		vcli.WithConfig(config.Default()),
		vcli.WithConfigOverlay(config.OverlayPath, config.OverlayAllow...),
		vcli.WithPreRun(startUpdateCheck(base)),
		vcli.WithPostRun(finishCheck),
	)
	// The commands get the stamp itself: repogov derives the major tag of the
	// reusable workflows from it and records it in .gt-repo.yaml as stamped.
	for _, cmd := range gtcli.Commands(gtcli.Deps{Version: b.Version}) {
		a.Add(cmd)
	}
	for _, cmd := range updatecmd.Commands(base) {
		a.Add(cmd)
	}
	return a
}

// startUpdateCheck returns the pre-run hook that starts the background update
// check under the auto_update settings of the configuration loaded for the
// command. A command that loads no configuration runs without a check. The
// hook never fails a command: a Config the run's settings make invalid is
// reported as a verbose detail and the command runs without a check.
func startUpdateCheck(base update.Config) vcli.PreRunHook {
	return func(ctx context.Context, inv *spec.Invocation, cmd spec.CommandInfo) (context.Context, error) {
		res, ok := vcli.ConfigFrom[config.Config](ctx)
		if !ok {
			return ctx, nil
		}
		start, _, err := selfupdate.ForRun(base, res.Value()).UpdateCheckHooks()
		if err != nil {
			inv.Detail("update check skipped: %v", err)
			return ctx, nil
		}
		return start(ctx, inv, cmd)
	}
}

// info is the build information "gt version" prints: the running binary's, as
// buildinfo.Read reports it, with the stamped version normalised by Version and
// the stamped commit and date wherever they are set.
func (b BuildInfo) info() buildinfo.Info {
	info := buildinfo.Read()
	info.Version = Version(b.Version)
	if b.Commit != "" {
		info.Commit = b.Commit
	}
	if b.Date != "" {
		info.Date = b.Date
	}
	return info
}

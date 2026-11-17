// Package app builds gt's App: its identity, build information, standard
// flags, configuration and directory layout, around the command tree of
// internal/cli.
package app

import (
	"strings"

	gtcli "github.com/pedromvgomes/gt/internal/cli"
	"github.com/pedromvgomes/gt/internal/config"
	vcli "go.vipengele.org/cli/core"
	"go.vipengele.org/cli/core/buildinfo"
	"go.vipengele.org/cli/spec"
	"go.vipengele.org/cli/spec/paths"
	"golang.org/x/mod/semver"
)

// PathsLayout is the directory layout of every directory gt resolves. XDG on
// every operating system keeps the configuration at $XDG_CONFIG_HOME/gt, or
// ~/.config/gt, on macOS as well as on Linux.
const PathsLayout = paths.LayoutXDG

// Identity is gt's identity as the vipengele libraries know it. Its EnvPrefix
// names the GT_* variables: GT_HOME, GT_NO_COLOR and the per-key configuration
// overrides.
func Identity() spec.Identity {
	return spec.Identity{
		Name:      "gt",
		Owner:     "pedromvgomes",
		Repo:      "gt",
		EnvPrefix: "GT",
		DirName:   "gt",
	}
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
	if !strings.HasPrefix(stamped, "v") && semver.IsValid("v"+stamped) {
		return "v" + stamped
	}
	return stamped
}

// New returns gt's App, holding every command of internal/cli.
func New(b BuildInfo) *vcli.App {
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
	)
	// The commands get the stamp itself: repogov derives the major tag of the
	// reusable workflows from it and records it in .gt-repo.yaml as stamped.
	for _, cmd := range gtcli.Commands(gtcli.Deps{Version: b.Version}) {
		a.Add(cmd)
	}
	return a
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

// Package selfupdate builds the go.vipengele.org/update Config gt updates
// itself with: Base for the "gt update" command, and ForRun for the background
// check of one run, under the user's auto_update settings.
//
// gt's identity, its directory layout and its version normalisation live here
// rather than in internal/app because internal/app imports this package; app
// re-exports them, so the App and the updater share one definition of each.
package selfupdate

import (
	"context"
	_ "embed"
	"strings"

	"github.com/pedromvgomes/gt/internal/config"
	"go.vipengele.org/cli/spec"
	"go.vipengele.org/cli/spec/paths"
	"go.vipengele.org/update"
	"go.vipengele.org/update/asset"
	"golang.org/x/mod/semver"
)

// publicKey is the minisign public key every gt release's checksums.txt is
// signed with. It is the only key gt update trusts: a release signed with any
// other key is refused. The release workflow verifies the MINISIGN_KEY secret
// against this file before it tags anything.
//
//go:embed minisign.pub
var publicKey string

// PathsLayout is the directory layout of every directory gt resolves. XDG on
// every operating system keeps the configuration at $XDG_CONFIG_HOME/gt, or
// ~/.config/gt, and the update state at $XDG_STATE_HOME/gt, or
// ~/.local/state/gt, on macOS as well as on Linux.
const PathsLayout = paths.LayoutXDG

// Identity is gt's identity as the vipengele libraries know it. Its EnvPrefix
// names the GT_* variables: GT_HOME, GT_NO_COLOR, GT_NO_UPDATE_CHECK and the
// per-key configuration overrides. Owner and Repo name the GitHub repository
// releases are read from.
func Identity() spec.Identity {
	return spec.Identity{
		Name:      "gt",
		Owner:     "pedromvgomes",
		Repo:      "gt",
		EnvPrefix: "GT",
		DirName:   "gt",
	}
}

// Version returns the stamped version as the vipengele libraries expect it:
// with a leading v when the stamp is a semantic version without one, so that
// "1.2.3" is the release "v1.2.3". Anything else, "dev" included, is returned
// unchanged. Without the v a release binary counts as a development build and
// never checks for or installs an update.
func Version(stamped string) string {
	if !strings.HasPrefix(stamped, "v") && semver.IsValid("v"+stamped) {
		return "v" + stamped
	}
	return stamped
}

// Base returns the update Config of the binary stamped with version, which
// "gt update" runs with whatever auto_update says. Its assets are the ones
// .goreleaser.yaml publishes: gt_<os>_<arch>.tar.gz, checksums.txt and
// checksums.txt.minisig.
//
// The release lookup is unauthenticated: Token returns "", so a GH_TOKEN in
// the environment, which gt scopes per repository, never reaches the
// background check, and a bad one cannot become a recorded failed attempt.
func Base(version string) update.Config {
	return update.Config{
		Identity:       Identity(),
		Version:        Version(version),
		TrustedKeys:    []string{publicKey},
		PathsLayout:    PathsLayout,
		AssetTemplate:  asset.DefaultTemplate,
		ChecksumsAsset: asset.DefaultChecksumsTemplate,
		Token:          func(context.Context) (string, error) { return "", nil },
	}
}

// ForRun returns base under the auto_update settings of cfg: the background
// check is off when auto_update is disabled, and runs at most once every
// check_interval when it is enabled. A disabled check keeps base's interval,
// because the updater rejects an interval under an hour even when the check
// is off, and configuration accepts any interval while it is.
func ForRun(base update.Config, cfg config.Config) update.Config {
	run := base
	run.DisableCheck = !cfg.AutoUpdate.Enabled
	if cfg.AutoUpdate.Enabled {
		run.CheckInterval = cfg.AutoUpdate.CheckInterval
	}
	return run
}

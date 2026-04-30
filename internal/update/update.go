package update

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/pedromvgomes/gt/internal/ui"
)

const (
	DefaultRepo    = "pedromvgomes/gt"
	DefaultBaseURL = "https://api.github.com"
)

type Available struct {
	Current   string
	Latest    string
	AssetURL  string
	AssetName string
	Checksums string
	HTMLURL   string
}

type Options struct {
	Repo       string
	BaseURL    string
	HTTPClient HTTPClient
	Now        func() time.Time
	OS         string
	Arch       string
	ExePath    string
}

func (o Options) withDefaults() Options {
	if o.Repo == "" {
		o.Repo = DefaultRepo
	}
	if o.BaseURL == "" {
		o.BaseURL = DefaultBaseURL
	}
	if o.HTTPClient == nil {
		o.HTTPClient = defaultClient()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.OS == "" {
		o.OS = runtime.GOOS
	}
	if o.Arch == "" {
		o.Arch = runtime.GOARCH
	}
	return o
}

// Eligible reports whether an update check should run for this invocation.
// reason is empty when eligible.
func Eligible(currentVersion string, interactive bool) (bool, string) {
	if strings.TrimSpace(currentVersion) == "" || currentVersion == "dev" {
		return false, "running a development build"
	}
	if os.Getenv("GT_NO_UPDATE_CHECK") != "" {
		return false, "GT_NO_UPDATE_CHECK is set"
	}
	if os.Getenv("CI") != "" {
		return false, "running in CI"
	}
	if !interactive {
		return false, "not attached to a terminal"
	}
	return true, ""
}

// ManagedExternally reports whether the binary at path looks like it was
// installed by a package manager that owns its lifecycle.
func ManagedExternally(path string) (bool, string) {
	if path == "" {
		return false, ""
	}
	abs, err := filepath.EvalSymlinks(path)
	if err != nil {
		abs = path
	}
	prefixes := []struct {
		path string
		name string
	}{
		{"/opt/homebrew/", "Homebrew"},
		{"/usr/local/Cellar/", "Homebrew"},
		{"/home/linuxbrew/", "Homebrew"},
		{"/nix/store/", "Nix"},
	}
	for _, p := range prefixes {
		if strings.HasPrefix(abs, p.path) {
			return true, p.name
		}
	}
	return false, ""
}

// Check fetches the latest release, compares it to currentVersion, and
// returns Available when a newer version is available. Returns nil when
// already up-to-date.
func Check(ctx context.Context, currentVersion string, opts Options) (*Available, error) {
	opts = opts.withDefaults()
	rel, err := fetchLatest(ctx, opts.HTTPClient, opts.BaseURL, opts.Repo)
	if err != nil {
		return nil, err
	}
	latest := normalizeVersion(rel.TagName)
	current := normalizeVersion(currentVersion)
	cmp, err := compareSemver(latest, current)
	if err != nil {
		return nil, fmt.Errorf("compare versions %q vs %q: %w", latest, current, err)
	}
	if cmp <= 0 {
		return nil, nil
	}
	assetName := fmt.Sprintf("gt_%s_%s.tar.gz", opts.OS, opts.Arch)
	assetURL := ""
	checksumsURL := ""
	for _, a := range rel.Assets {
		switch a.Name {
		case assetName:
			assetURL = a.BrowserDownloadURL
		case "checksums.txt":
			checksumsURL = a.BrowserDownloadURL
		}
	}
	if assetURL == "" {
		return nil, fmt.Errorf("release %s has no asset %s", rel.TagName, assetName)
	}
	if checksumsURL == "" {
		return nil, fmt.Errorf("release %s has no checksums.txt", rel.TagName)
	}
	return &Available{
		Current:   current,
		Latest:    latest,
		AssetURL:  assetURL,
		AssetName: assetName,
		Checksums: checksumsURL,
		HTMLURL:   rel.HTMLURL,
	}, nil
}

// Apply downloads, verifies, and replaces the running binary with the
// latest release.
func Apply(ctx context.Context, u *ui.UI, available *Available, opts Options) error {
	opts = opts.withDefaults()
	if available == nil {
		return fmt.Errorf("no update available")
	}
	exe := opts.ExePath
	if exe == "" {
		path, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolve current executable: %w", err)
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			resolved = path
		}
		exe = resolved
	}
	if managed, by := ManagedExternally(exe); managed {
		return ui.Errorf(ui.ExitUser, "gt at %s is managed by %s; update through that package manager instead", exe, by)
	}
	u.Info("downloading %s", available.AssetName)
	return downloadAndReplace(ctx, opts.HTTPClient, available, exe)
}

// SkipReason returns a non-empty reason when an update check should be
// skipped because of where the binary lives or other static state.
func SkipReason(exePath string) string {
	if managed, by := ManagedExternally(exePath); managed {
		return fmt.Sprintf("binary is managed by %s", by)
	}
	return ""
}

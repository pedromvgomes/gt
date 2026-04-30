package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

func defaultClient() HTTPClient {
	return &http.Client{Timeout: 10 * time.Second}
}

type release struct {
	TagName string         `json:"tag_name"`
	HTMLURL string         `json:"html_url"`
	Assets  []releaseAsset `json:"assets"`
}

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func fetchLatest(ctx context.Context, client HTTPClient, baseURL, repo string) (*release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(baseURL, "/"), repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch latest release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("fetch latest release: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decode release JSON: %w", err)
	}
	if rel.TagName == "" {
		return nil, fmt.Errorf("release has no tag_name")
	}
	return &rel, nil
}

func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	return v
}

// compareSemver returns >0 if a > b, <0 if a < b, 0 if equal. It accepts
// MAJOR.MINOR.PATCH with optional pre-release suffixes (treated as
// less-than the same version without a suffix).
func compareSemver(a, b string) (int, error) {
	aBase, aPre := splitPre(a)
	bBase, bPre := splitPre(b)
	aParts, err := parseParts(aBase)
	if err != nil {
		return 0, err
	}
	bParts, err := parseParts(bBase)
	if err != nil {
		return 0, err
	}
	for i := 0; i < 3; i++ {
		if aParts[i] != bParts[i] {
			if aParts[i] > bParts[i] {
				return 1, nil
			}
			return -1, nil
		}
	}
	switch {
	case aPre == "" && bPre == "":
		return 0, nil
	case aPre == "":
		return 1, nil
	case bPre == "":
		return -1, nil
	}
	return strings.Compare(aPre, bPre), nil
}

func splitPre(v string) (string, string) {
	if idx := strings.Index(v, "-"); idx >= 0 {
		return v[:idx], v[idx+1:]
	}
	return v, ""
}

func parseParts(v string) ([3]int, error) {
	var out [3]int
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return out, fmt.Errorf("invalid semver %q", v)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return out, fmt.Errorf("invalid semver %q: %w", v, err)
		}
		out[i] = n
	}
	return out, nil
}

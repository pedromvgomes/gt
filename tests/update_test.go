package tests

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pedromvgomes/gt/internal/ui"
	"github.com/pedromvgomes/gt/internal/update"
)

func TestCheckReturnsAvailableWhenNewer(t *testing.T) {
	srv := newReleaseServer(t, "v1.4.0", map[string][]byte{
		"gt_linux_amd64.tar.gz": tarballWithBinary(t, []byte("new gt v1.4.0")),
	})
	defer srv.Close()

	available, err := update.Check(context.Background(), "1.3.2", update.Options{
		Repo:    "owner/gt",
		BaseURL: srv.URL,
		OS:      "linux",
		Arch:    "amd64",
	})
	if err != nil {
		t.Fatalf("Check error: %v", err)
	}
	if available == nil {
		t.Fatal("expected an available update, got nil")
	}
	if available.Latest != "1.4.0" {
		t.Errorf("Latest = %q, want %q", available.Latest, "1.4.0")
	}
	if available.AssetName != "gt_linux_amd64.tar.gz" {
		t.Errorf("AssetName = %q", available.AssetName)
	}
}

func TestCheckReturnsNilWhenSameVersion(t *testing.T) {
	srv := newReleaseServer(t, "v1.3.2", map[string][]byte{
		"gt_linux_amd64.tar.gz": tarballWithBinary(t, []byte("gt v1.3.2")),
	})
	defer srv.Close()

	available, err := update.Check(context.Background(), "1.3.2", update.Options{
		Repo:    "owner/gt",
		BaseURL: srv.URL,
		OS:      "linux",
		Arch:    "amd64",
	})
	if err != nil {
		t.Fatalf("Check error: %v", err)
	}
	if available != nil {
		t.Errorf("expected nil, got %+v", available)
	}
}

func TestCheckReturnsNilWhenOlderTagPublished(t *testing.T) {
	srv := newReleaseServer(t, "v1.0.0", map[string][]byte{
		"gt_linux_amd64.tar.gz": tarballWithBinary(t, []byte("gt v1.0.0")),
	})
	defer srv.Close()

	available, err := update.Check(context.Background(), "1.3.2", update.Options{
		Repo:    "owner/gt",
		BaseURL: srv.URL,
		OS:      "linux",
		Arch:    "amd64",
	})
	if err != nil {
		t.Fatalf("Check error: %v", err)
	}
	if available != nil {
		t.Errorf("expected nil, got %+v", available)
	}
}

func TestCheckErrorsWhenAssetMissing(t *testing.T) {
	srv := newReleaseServer(t, "v1.4.0", map[string][]byte{
		"gt_darwin_arm64.tar.gz": tarballWithBinary(t, []byte("gt")),
	})
	defer srv.Close()

	_, err := update.Check(context.Background(), "1.3.2", update.Options{
		Repo:    "owner/gt",
		BaseURL: srv.URL,
		OS:      "linux",
		Arch:    "amd64",
	})
	if err == nil || !strings.Contains(err.Error(), "gt_linux_amd64.tar.gz") {
		t.Fatalf("expected asset-missing error, got %v", err)
	}
}

func TestApplyDownloadsAndReplacesBinary(t *testing.T) {
	payload := []byte("REPLACED-BINARY-CONTENT")
	srv := newReleaseServer(t, "v2.0.0", map[string][]byte{
		"gt_linux_amd64.tar.gz": tarballWithBinary(t, payload),
	})
	defer srv.Close()

	exeDir := t.TempDir()
	exePath := filepath.Join(exeDir, "gt")
	if err := os.WriteFile(exePath, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}

	available, err := update.Check(context.Background(), "1.0.0", update.Options{
		Repo:    "owner/gt",
		BaseURL: srv.URL,
		OS:      "linux",
		Arch:    "amd64",
	})
	if err != nil {
		t.Fatalf("Check error: %v", err)
	}
	if available == nil {
		t.Fatal("expected available update")
	}

	u := ui.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, true, true)
	if err := update.Apply(context.Background(), u, available, update.Options{
		BaseURL: srv.URL,
		ExePath: exePath,
	}); err != nil {
		t.Fatalf("Apply error: %v", err)
	}

	got, err := os.ReadFile(exePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("binary content not replaced; got %q", got)
	}
	info, _ := os.Stat(exePath)
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("binary not executable: %v", info.Mode())
	}
}

func TestApplyRejectsChecksumMismatch(t *testing.T) {
	good := tarballWithBinary(t, []byte("real"))
	bad := tarballWithBinary(t, []byte("tampered"))

	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/gt/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		base := serverURL
		rel := map[string]any{
			"tag_name": "v3.0.0",
			"html_url": base,
			"assets": []map[string]string{
				{"name": "gt_linux_amd64.tar.gz", "browser_download_url": base + "/dl/gt_linux_amd64.tar.gz"},
				{"name": "checksums.txt", "browser_download_url": base + "/dl/checksums.txt"},
			},
		}
		_ = json.NewEncoder(w).Encode(rel)
	})
	mux.HandleFunc("/dl/gt_linux_amd64.tar.gz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bad)
	})
	mux.HandleFunc("/dl/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  gt_linux_amd64.tar.gz\n", sha256Hex(good))
	})
	srv := httptest.NewServer(mux)
	serverURL = srv.URL
	defer srv.Close()

	available, err := update.Check(context.Background(), "1.0.0", update.Options{
		Repo: "owner/gt", BaseURL: srv.URL, OS: "linux", Arch: "amd64",
	})
	if err != nil {
		t.Fatalf("Check error: %v", err)
	}
	exePath := filepath.Join(t.TempDir(), "gt")
	_ = os.WriteFile(exePath, []byte("OLD"), 0o755)

	u := ui.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, true, true)
	err = update.Apply(context.Background(), u, available, update.Options{
		BaseURL: srv.URL, ExePath: exePath,
	})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch error, got %v", err)
	}
	got, _ := os.ReadFile(exePath)
	if string(got) != "OLD" {
		t.Errorf("binary should be untouched on checksum failure, got %q", got)
	}
}

func TestEligibleSkipsDevVersion(t *testing.T) {
	t.Setenv("GT_NO_UPDATE_CHECK", "")
	t.Setenv("CI", "")
	if ok, _ := update.Eligible("dev", true); ok {
		t.Errorf("expected dev version to be ineligible")
	}
}

func TestEligibleSkipsCI(t *testing.T) {
	t.Setenv("GT_NO_UPDATE_CHECK", "")
	t.Setenv("CI", "true")
	if ok, _ := update.Eligible("1.0.0", true); ok {
		t.Errorf("expected CI to be ineligible")
	}
}

func TestEligibleSkipsNonInteractive(t *testing.T) {
	t.Setenv("GT_NO_UPDATE_CHECK", "")
	t.Setenv("CI", "")
	if ok, _ := update.Eligible("1.0.0", false); ok {
		t.Errorf("expected non-interactive to be ineligible")
	}
}

func TestEligibleSkipsWhenEnvDisabled(t *testing.T) {
	t.Setenv("GT_NO_UPDATE_CHECK", "1")
	if ok, _ := update.Eligible("1.0.0", true); ok {
		t.Errorf("expected GT_NO_UPDATE_CHECK to disable")
	}
}

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	path, err := update.StatePath()
	if err != nil {
		t.Fatal(err)
	}
	want := update.State{LastCheckedAt: time.Unix(1700000000, 0).UTC(), LatestSeen: "2.0.0"}
	if err := update.SaveState(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := update.LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastCheckedAt.Equal(want.LastCheckedAt) || got.LatestSeen != want.LatestSeen {
		t.Errorf("round-trip mismatch: got %+v want %+v", got, want)
	}
}

func TestDueForCheck(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name     string
		state    update.State
		interval time.Duration
		want     bool
	}{
		{"never checked", update.State{}, time.Hour, true},
		{"recent check", update.State{LastCheckedAt: now.Add(-30 * time.Minute)}, time.Hour, false},
		{"old check", update.State{LastCheckedAt: now.Add(-2 * time.Hour)}, time.Hour, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := update.DueForCheck(tc.state, now, tc.interval); got != tc.want {
				t.Errorf("DueForCheck = %v want %v", got, tc.want)
			}
		})
	}
}

// helpers ---------------------------------------------------------------

func newReleaseServer(t *testing.T, tag string, assets map[string][]byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("/repos/owner/gt/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		base := server.URL
		rel := map[string]any{
			"tag_name": tag,
			"html_url": base,
			"assets":   []map[string]string{},
		}
		entries := []map[string]string{}
		var checksumLines strings.Builder
		for name, data := range assets {
			entries = append(entries, map[string]string{
				"name":                 name,
				"browser_download_url": base + "/dl/" + name,
			})
			fmt.Fprintf(&checksumLines, "%s  %s\n", sha256Hex(data), name)
		}
		entries = append(entries, map[string]string{
			"name":                 "checksums.txt",
			"browser_download_url": base + "/dl/checksums.txt",
		})
		rel["assets"] = entries
		_ = json.NewEncoder(w).Encode(rel)
		_ = checksumLines.String()
	})
	for name, data := range assets {
		data := data
		mux.HandleFunc("/dl/"+name, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(data)
		})
	}
	mux.HandleFunc("/dl/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		for name, data := range assets {
			fmt.Fprintf(w, "%s  %s\n", sha256Hex(data), name)
		}
	})
	server = httptest.NewServer(mux)
	return server
}

func tarballWithBinary(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{
		Name:     "gt",
		Mode:     0o755,
		Size:     int64(len(payload)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

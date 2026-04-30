package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const binaryName = "gt"

func downloadAndReplace(ctx context.Context, client HTTPClient, available *Available, exePath string) error {
	tmp, err := os.MkdirTemp("", "gt-update-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	archive := filepath.Join(tmp, available.AssetName)
	if err := download(ctx, client, available.AssetURL, archive); err != nil {
		return fmt.Errorf("download %s: %w", available.AssetName, err)
	}

	checksums := filepath.Join(tmp, "checksums.txt")
	if err := download(ctx, client, available.Checksums, checksums); err != nil {
		return fmt.Errorf("download checksums.txt: %w", err)
	}

	expected, err := lookupChecksum(checksums, available.AssetName)
	if err != nil {
		return err
	}
	actual, err := sha256File(archive)
	if err != nil {
		return fmt.Errorf("hash %s: %w", available.AssetName, err)
	}
	if expected != actual {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", available.AssetName, expected, actual)
	}

	binPath := filepath.Join(tmp, binaryName)
	if err := extractBinary(archive, binPath); err != nil {
		return fmt.Errorf("extract %s: %w", binaryName, err)
	}

	return swapBinary(binPath, exePath)
}

func download(ctx context.Context, client HTTPClient, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	return f.Close()
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func lookupChecksum(path, asset string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read checksums: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if fields[1] == asset {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("checksum for %s not found", asset)
}

func extractBinary(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("archive does not contain %s", binaryName)
		}
		if err != nil {
			return err
		}
		if filepath.Base(hdr.Name) != binaryName || hdr.Typeflag != tar.TypeReg {
			continue
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	}
}

func swapBinary(src, dest string) error {
	info, err := os.Stat(dest)
	if err != nil {
		return fmt.Errorf("stat %s: %w", dest, err)
	}
	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0o755
	}

	dir := filepath.Dir(dest)
	staged, err := os.CreateTemp(dir, ".gt-update-*")
	if err != nil {
		return fmt.Errorf("create staged file in %s: %w", dir, err)
	}
	stagedPath := staged.Name()
	_ = staged.Close()

	if err := copyFile(src, stagedPath, mode); err != nil {
		_ = os.Remove(stagedPath)
		return err
	}
	if err := os.Rename(stagedPath, dest); err != nil {
		_ = os.Remove(stagedPath)
		return fmt.Errorf("replace %s: %w", dest, err)
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	// #nosec G304 -- writes the download to a path gt built under its own temp directory.
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
	// #nosec G304 -- reads back that same temp file to checksum it.
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
	// #nosec G304 -- reads the checksums.txt gt just downloaded to its temp directory.
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

// maxBinaryBytes caps what extractBinary will write. gt's own binary is a few
// tens of megabytes; 512 MiB leaves room for it to grow by an order of
// magnitude while still bounding a hostile archive.
const maxBinaryBytes = 512 << 20

func extractBinary(archive, dest string) error {
	// #nosec G304 -- opens the verified archive from gt's temp directory.
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
		// 0755 because this is the gt binary and it has to be executable, and
		// dest is gt's own install path resolved from the running binary.
		// #nosec G302,G304 -- an executable that is not executable is not a binary.
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		// Bounded copy rather than io.Copy. The archive's checksum is verified
		// before this runs, so a decompression bomb needs a release that is
		// both malicious and correctly checksummed — but "we checked earlier"
		// is a weaker guarantee than a limit that cannot be argued with, and
		// the cost of the limit is one call.
		//
		// io.CopyN rather than io.Copy over an io.LimitReader: the two are
		// equivalent here, but only the former is legible to the decompression
		// scanners as a bound, and a mitigation a reader cannot see is half a
		// mitigation. Short input returns io.EOF, which is the success case.
		written, err := io.CopyN(out, tr, maxBinaryBytes+1)
		if err != nil && !errors.Is(err, io.EOF) {
			_ = out.Close()
			return err
		}
		if written > maxBinaryBytes {
			_ = out.Close()
			return fmt.Errorf("%s exceeds the %d byte limit; refusing to install", binaryName, maxBinaryBytes)
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
	// #nosec G304 -- copies the extracted binary from gt's temp directory.
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	// #nosec G304 -- destination is gt's own install path.
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

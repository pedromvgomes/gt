package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeArchive(t *testing.T, path string, payload []byte) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: binaryName, Mode: 0o755, Size: int64(len(payload)), Typeflag: tar.TypeReg}); err != nil {
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
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A normal binary extracts byte-for-byte. io.CopyN's io.EOF on short input is
// the success case, not an error, and getting that backwards would break every
// self-update.
func TestExtractBinaryRoundTrips(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.tar.gz")
	payload := []byte(strings.Repeat("gt-binary-content", 4096))
	writeArchive(t, archive, payload)

	dest := filepath.Join(dir, "gt")
	if err := extractBinary(archive, dest); err != nil {
		t.Fatalf("extractBinary() error = %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("extracted %d bytes, want %d", len(got), len(payload))
	}
}

// The bound has to actually reject, or it is decoration.
func TestExtractBinaryRejectsOversizedEntry(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.tar.gz")
	writeArchive(t, archive, bytes.Repeat([]byte{0}, maxBinaryBytes+1))

	err := extractBinary(archive, filepath.Join(dir, "gt"))
	if err == nil {
		t.Fatal("extractBinary() accepted an entry over the limit")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("error = %v, want it to mention the limit", err)
	}
}

package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// buildTarGz builds an in-memory gzipped tar archive containing a single
// regular file named name with the given contents, mirroring goreleaser's
// release archives closely enough for ExtractSingleFromTarGz.
// Moved from internal/upgrade/upgrade_test.go's buildTarGz.
func buildTarGz(t *testing.T, name string, contents []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(contents)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("tar WriteHeader: %v", err)
	}
	if _, err := tw.Write(contents); err != nil {
		t.Fatalf("tar Write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}
	return buf.Bytes()
}

// buildZip builds an in-memory zip archive containing a single regular file
// named name with the given contents.
func buildZip(t *testing.T, name string, contents []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatalf("zip Create: %v", err)
	}
	if _, err := w.Write(contents); err != nil {
		t.Fatalf("zip Write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip Close: %v", err)
	}
	return buf.Bytes()
}

// zipWithEntry builds an in-memory zip archive with a single header exactly
// as given (so tests can set arbitrary, malicious names and modes) and the
// given contents.
func zipWithEntry(t *testing.T, hdr *zip.FileHeader, contents []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		t.Fatalf("zip CreateHeader: %v", err)
	}
	if _, err := w.Write(contents); err != nil {
		t.Fatalf("zip Write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip Close: %v", err)
	}
	return buf.Bytes()
}

// tarGzWithEntry builds an in-memory gzipped tar archive with a single
// header exactly as given.
func tarGzWithEntry(t *testing.T, hdr *tar.Header, contents []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr.Size = int64(len(contents))
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("tar WriteHeader: %v", err)
	}
	if _, err := tw.Write(contents); err != nil {
		t.Fatalf("tar Write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}
	return buf.Bytes()
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// TestDownloadToFileReturnsKnownDigest proves DownloadToFile returns the
// known sha256 of an httptest.Server's response body and writes that body
// to path.
func TestDownloadToFileReturnsKnownDigest(t *testing.T) {
	body := []byte("release archive bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "downloaded.bin")
	got, err := DownloadToFile(context.Background(), srv.Client(), srv.URL, dest)
	if err != nil {
		t.Fatalf("DownloadToFile: %v", err)
	}
	if want := sha256Hex(body); got != want {
		t.Errorf("DownloadToFile digest = %s, want %s", got, want)
	}
	gotBody, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if !bytes.Equal(gotBody, body) {
		t.Errorf("downloaded file content = %q, want %q", gotBody, body)
	}
}

// TestDownloadToFileRejectsNonOKStatus proves a non-200 response is treated
// as a failure rather than silently writing an error page to disk.
func TestDownloadToFileRejectsNonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "downloaded.bin")
	if _, err := DownloadToFile(context.Background(), srv.Client(), srv.URL, dest); err == nil {
		t.Fatal("DownloadToFile against a 404: want an error, got nil")
	}
}

func TestExtractSingleFromTarGzFindsNamedFile(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := os.WriteFile(archivePath, buildTarGz(t, "grid", []byte("binary-content")), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path, err := ExtractSingleFromTarGz(archivePath, dir, "grid")
	if err != nil {
		t.Fatalf("ExtractSingleFromTarGz: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "binary-content" {
		t.Errorf("extracted content = %q", got)
	}
}

func TestExtractSingleFromZipFindsNamedFile(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "a.zip")
	if err := os.WriteFile(archivePath, buildZip(t, "grid.exe", []byte("binary-content")), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path, err := ExtractSingleFromZip(archivePath, dir, "grid.exe")
	if err != nil {
		t.Fatalf("ExtractSingleFromZip: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "binary-content" {
		t.Errorf("extracted content = %q", got)
	}
}

func TestExtractZipToWritesWholeTree(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	mustWrite := func(name string, contents []byte) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(contents); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("README.md", []byte("hello"))
	mustWrite("src/main.go", []byte("package main"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(t.TempDir(), "bundle.zip")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	destDir := t.TempDir()
	if err := ExtractZipTo(archivePath, destDir); err != nil {
		t.Fatalf("ExtractZipTo: %v", err)
	}
	readme, err := os.ReadFile(filepath.Join(destDir, "README.md"))
	if err != nil || string(readme) != "hello" {
		t.Errorf("README.md = %q, %v", readme, err)
	}
	main, err := os.ReadFile(filepath.Join(destDir, "src", "main.go"))
	if err != nil || string(main) != "package main" {
		t.Errorf("src/main.go = %q, %v", main, err)
	}
}

// TestExtractZipToRejectsPathTraversal is an attack case: a zip-slip entry
// (criterion 5) must be rejected with ErrEntryEscapesArchive and must not
// write anything into destDir, even though the archive also contains one
// perfectly legitimate entry.
func TestExtractZipToRejectsPathTraversal(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, contents := range map[string]string{
		"README.md":      "hello",
		"../evil.txt":    "escaped",
		"legitimate.txt": "fine",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(t.TempDir(), "evil.zip")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	destDir := t.TempDir()
	err := ExtractZipTo(archivePath, destDir)
	if !errors.Is(err, ErrEntryEscapesArchive) {
		t.Fatalf("ExtractZipTo on a zip containing ../evil.txt: err = %v, want ErrEntryEscapesArchive", err)
	}
	entries, err := os.ReadDir(destDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("destDir has %d entries after a rejected extraction, want 0: %v", len(entries), entries)
	}
}

// TestExtractZipToRejectsAbsolutePath is an attack case: an absolute entry
// name must be rejected the same way a ".." entry is.
func TestExtractZipToRejectsAbsolutePath(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "evil.zip")
	if err := os.WriteFile(archivePath, buildZip(t, "/etc/passwd", []byte("evil")), 0o644); err != nil {
		t.Fatal(err)
	}
	destDir := t.TempDir()
	err := ExtractZipTo(archivePath, destDir)
	if !errors.Is(err, ErrEntryEscapesArchive) {
		t.Fatalf("ExtractZipTo on a zip containing an absolute path: err = %v, want ErrEntryEscapesArchive", err)
	}
	entries, _ := os.ReadDir(destDir)
	if len(entries) != 0 {
		t.Errorf("destDir has %d entries after a rejected extraction, want 0", len(entries))
	}
}

// TestExtractZipToRejectsSymlink is an attack case (criterion 6): a symlink
// entry must be rejected with ErrArchiveContainsSymlink and must not write
// anything into destDir.
func TestExtractZipToRejectsSymlink(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("README.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	linkHdr := &zip.FileHeader{Name: "link-to-secrets"}
	linkHdr.SetMode(os.ModeSymlink | 0o777)
	lw, err := zw.CreateHeader(linkHdr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lw.Write([]byte("/etc/shadow")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(t.TempDir(), "evil.zip")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	destDir := t.TempDir()
	err = ExtractZipTo(archivePath, destDir)
	if !errors.Is(err, ErrArchiveContainsSymlink) {
		t.Fatalf("ExtractZipTo on a zip containing a symlink: err = %v, want ErrArchiveContainsSymlink", err)
	}
	entries, _ := os.ReadDir(destDir)
	if len(entries) != 0 {
		t.Errorf("destDir has %d entries after a rejected extraction, want 0", len(entries))
	}
}

// TestWriteCappedEnforcesSizeCeiling is the decompression-bomb attack case:
// an entry whose actual content exceeds maxExtractedEntrySize must abort
// rather than exhaust disk space. maxExtractedEntrySize is a var precisely
// so this test can shrink it instead of allocating a real 1 GiB buffer —
// the boundary logic writeCapped applies is identical regardless of the
// cap's size.
func TestWriteCappedEnforcesSizeCeiling(t *testing.T) {
	old := maxExtractedEntrySize
	maxExtractedEntrySize = 8
	t.Cleanup(func() { maxExtractedEntrySize = old })

	dir := t.TempDir()

	okPath := filepath.Join(dir, "ok")
	if err := writeCapped(okPath, bytes.NewReader(make([]byte, 8)), 0o644); err != nil {
		t.Fatalf("writeCapped at exactly the cap: %v", err)
	}
	info, err := os.Stat(okPath)
	if err != nil || info.Size() != 8 {
		t.Fatalf("writeCapped at the cap wrote %v bytes (err %v), want 8", info, err)
	}

	overPath := filepath.Join(dir, "over")
	err = writeCapped(overPath, bytes.NewReader(make([]byte, 9)), 0o644)
	if err == nil {
		t.Fatal("writeCapped one byte over the cap: want an error, got nil")
	}
}

// TestCleanEntryPathRejectsEscapes is a table test over the path-escape
// guard both extractors share.
func TestCleanEntryPathRejectsEscapes(t *testing.T) {
	cases := []struct {
		name    string
		wantErr bool
	}{
		{"normal/file.txt", false},
		{".", false},
		{"../escape.txt", true},
		{"nested/../../escape.txt", true},
		{"/absolute/path", true},
		{"a/b/../c", false}, // cleans to a/c, still inside the root
	}
	for _, c := range cases {
		_, err := cleanEntryPath(c.name)
		if c.wantErr && !errors.Is(err, ErrEntryEscapesArchive) {
			t.Errorf("cleanEntryPath(%q) = %v, want ErrEntryEscapesArchive", c.name, err)
		}
		if !c.wantErr && err != nil {
			t.Errorf("cleanEntryPath(%q) = %v, want no error", c.name, err)
		}
	}
}

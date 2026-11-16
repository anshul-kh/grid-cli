package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildTarGz builds an in-memory gzipped tar archive containing a single
// regular file named name with the given contents, mirroring goreleaser's
// release archives closely enough for grid upgrade's extraction logic.
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

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// newReleaseServer serves a fake "GitHub releases" API: one release
// ("v1.2.0") with a linux/amd64 archive and a checksums.txt. checksumHex
// overrides the published digest for the archive asset, so tests can
// corrupt it to exercise the checksum-mismatch path.
func newReleaseServer(t *testing.T, archive []byte, checksumHex string) (*httptest.Server, string) {
	t.Helper()
	const assetName = "grid-cli_linux_amd64.tar.gz"
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/repos/acme/grid-cli/releases", func(w http.ResponseWriter, r *http.Request) {
		releases := []Release{{
			TagName: "v1.2.0",
			Assets: []Asset{
				{Name: assetName, BrowserDownloadURL: srv.URL + "/dl/" + assetName},
				{Name: "checksums.txt", BrowserDownloadURL: srv.URL + "/dl/checksums.txt"},
			},
		}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(releases)
	})
	mux.HandleFunc("/dl/"+assetName, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	})
	mux.HandleFunc("/dl/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", checksumHex, assetName)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, assetName
}

func TestRunDownloadsVerifiesAndReplacesAtomically(t *testing.T) {
	archive := buildTarGz(t, "grid", []byte("new-binary-content-v1.2.0"))
	srv, assetName := newReleaseServer(t, archive, sha256Hex(archive))

	dir := t.TempDir()
	execPath := filepath.Join(dir, "grid")
	if err := os.WriteFile(execPath, []byte("old-binary-content"), 0o755); err != nil {
		t.Fatalf("seeding execPath: %v", err)
	}

	opts := Options{
		HTTPClient:     srv.Client(),
		APIBase:        srv.URL,
		RepoURL:        "https://github.com/acme/grid-cli",
		CurrentVersion: "1.0.0",
		OS:             "linux",
		Arch:           "amd64",
		ExecPath:       execPath,
	}
	res, err := Run(context.Background(), opts, false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Upgraded {
		t.Error("Upgraded = false, want true")
	}
	if res.LatestVersion != "v1.2.0" {
		t.Errorf("LatestVersion = %q, want v1.2.0", res.LatestVersion)
	}
	wantAssetURL := srv.URL + "/dl/" + assetName
	if res.AssetURL != wantAssetURL {
		t.Errorf("AssetURL = %q, want %q", res.AssetURL, wantAssetURL)
	}

	got, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatalf("reading execPath after upgrade: %v", err)
	}
	if string(got) != "new-binary-content-v1.2.0" {
		t.Errorf("execPath content = %q, want the new binary's content", got)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "grid" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("target directory left stray files after a successful upgrade: %v, want only [grid]", names)
	}
}

func TestRunChecksumMismatchAbortsWithoutReplacing(t *testing.T) {
	archive := buildTarGz(t, "grid", []byte("new-binary-content-v1.2.0"))
	// Publish a checksum that does not match the archive.
	srv, _ := newReleaseServer(t, archive, strings.Repeat("0", 64))

	dir := t.TempDir()
	execPath := filepath.Join(dir, "grid")
	const original = "old-binary-content"
	if err := os.WriteFile(execPath, []byte(original), 0o755); err != nil {
		t.Fatalf("seeding execPath: %v", err)
	}

	opts := Options{
		HTTPClient:     srv.Client(),
		APIBase:        srv.URL,
		RepoURL:        "https://github.com/acme/grid-cli",
		CurrentVersion: "1.0.0",
		OS:             "linux",
		Arch:           "amd64",
		ExecPath:       execPath,
	}
	res, err := Run(context.Background(), opts, false)
	if err == nil {
		t.Fatal("Run with a mismatched checksum: want an error, got nil")
	}
	if !errors.Is(err, ErrInstallFailed) {
		t.Errorf("error = %v, want it to wrap ErrInstallFailed", err)
	}
	if res.Upgraded {
		t.Error("Upgraded = true after a checksum mismatch, want false")
	}

	got, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatalf("reading execPath after the aborted upgrade: %v", err)
	}
	if string(got) != original {
		t.Errorf("execPath content = %q, want the untouched original %q", got, original)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "grid" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("target directory left a partial file after a checksum mismatch: %v, want only [grid]", names)
	}
}

// erroringTransport always fails — used to prove a code path never issues
// any HTTP request at all.
type erroringTransport struct{}

func (erroringTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network must not be used on this path")
}

func TestRunPackageManagedSkipsNetworkAndSelfReplace(t *testing.T) {
	dir := t.TempDir()
	// The Homebrew check is path-string-based; the file need not exist.
	execPath := filepath.Join(dir, "opt", "homebrew", "bin", "grid")

	opts := Options{
		HTTPClient:     &http.Client{Transport: erroringTransport{}},
		RepoURL:        "https://github.com/acme/grid-cli",
		CurrentVersion: "1.0.0",
		OS:             "linux",
		Arch:           "amd64",
		ExecPath:       execPath,
	}
	res, err := Run(context.Background(), opts, false)
	if err != nil {
		t.Fatalf("Run on a package-managed install: want no error (and no network call), got %v", err)
	}
	if res.PackageManaged != "Homebrew" {
		t.Errorf("PackageManaged = %q, want Homebrew", res.PackageManaged)
	}
	if res.Upgraded {
		t.Error("Upgraded = true, want false — a package-manager install must never self-replace")
	}
	if res.AssetURL != "" {
		t.Errorf("AssetURL = %q, want empty — nothing was ever fetched", res.AssetURL)
	}
}

func TestRunAlreadyUpToDate(t *testing.T) {
	archive := buildTarGz(t, "grid", []byte("irrelevant"))
	srv, _ := newReleaseServer(t, archive, sha256Hex(archive))

	dir := t.TempDir()
	execPath := filepath.Join(dir, "grid")
	if err := os.WriteFile(execPath, []byte("current-binary"), 0o755); err != nil {
		t.Fatalf("seeding execPath: %v", err)
	}

	opts := Options{
		HTTPClient:     srv.Client(),
		APIBase:        srv.URL,
		RepoURL:        "https://github.com/acme/grid-cli",
		CurrentVersion: "9.9.9", // already newer than the published v1.2.0
		OS:             "linux",
		Arch:           "amd64",
		ExecPath:       execPath,
	}
	res, err := Run(context.Background(), opts, false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Upgraded {
		t.Error("Upgraded = true, want false when already newer than the latest release")
	}
	if res.LatestVersion != "9.9.9" {
		t.Errorf("LatestVersion = %q, want the unchanged current version", res.LatestVersion)
	}
}

func TestRunDryRunChecksButDoesNotInstall(t *testing.T) {
	archive := buildTarGz(t, "grid", []byte("new-binary-content-v1.2.0"))
	srv, _ := newReleaseServer(t, archive, sha256Hex(archive))

	dir := t.TempDir()
	execPath := filepath.Join(dir, "grid")
	if err := os.WriteFile(execPath, []byte("old-binary-content"), 0o755); err != nil {
		t.Fatalf("seeding execPath: %v", err)
	}

	opts := Options{
		HTTPClient:     srv.Client(),
		APIBase:        srv.URL,
		RepoURL:        "https://github.com/acme/grid-cli",
		CurrentVersion: "1.0.0",
		OS:             "linux",
		Arch:           "amd64",
		ExecPath:       execPath,
	}
	res, err := Run(context.Background(), opts, true)
	if err != nil {
		t.Fatalf("Run --check: %v", err)
	}
	if res.Upgraded {
		t.Error("Upgraded = true during a dry run, want false")
	}
	if res.LatestVersion != "v1.2.0" || res.AssetURL == "" {
		t.Errorf("dry run should still report what's available: %+v", res)
	}
	got, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatalf("reading execPath: %v", err)
	}
	if string(got) != "old-binary-content" {
		t.Error("dry run must not touch execPath")
	}
}

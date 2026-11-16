package upgrade

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/anshul-kh/grid-cli/internal/archive"
)

// applyUpgrade downloads asset into a scratch directory, verifies it
// against checksums (asset.Name -> lowercase hex sha256) before anything
// under execPath's directory is touched, extracts the binary, and
// atomically replaces execPath. A checksum mismatch aborts here — no
// partial or replaced file is ever left behind.
func applyUpgrade(ctx context.Context, client *http.Client, asset Asset, checksums map[string]string, execPath, goos string) error {
	want, ok := checksums[asset.Name]
	if !ok {
		return fmt.Errorf("%w: no published checksum for %s — refusing to install an unverified binary", ErrInstallFailed, asset.Name)
	}

	scratchDir, err := os.MkdirTemp("", "grid-upgrade-download-*")
	if err != nil {
		return fmt.Errorf("creating a scratch directory: %w", err)
	}
	defer os.RemoveAll(scratchDir)

	archivePath := filepath.Join(scratchDir, asset.Name)
	got, err := archive.DownloadToFile(ctx, client, asset.BrowserDownloadURL, archivePath)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("%w: checksum mismatch for %s: got %s, published %s", ErrInstallFailed, asset.Name, got, want)
	}

	targetDir := filepath.Dir(execPath)
	tmpBinary, err := extractBinary(archivePath, targetDir, goos)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInstallFailed, err)
	}
	if err := replaceBinary(tmpBinary, execPath); err != nil {
		return fmt.Errorf("%w: %s", ErrInstallFailed, err)
	}
	return nil
}

// binaryName is the executable grid upgrade looks for inside a downloaded
// release archive: "grid" on unix, "grid.exe" on windows (main.go: "Command
// grid is the companion CLI..."; pass 9's .goreleaser.yaml names the built
// binary to match).
func binaryName(goos string) string {
	if goos == "windows" {
		return "grid.exe"
	}
	return "grid"
}

// extractBinary extracts binaryName(goos) from the archive at archivePath
// into a new temp file inside dir — dir is execPath's own directory, so
// replaceBinary's final rename stays on one filesystem and is therefore
// atomic. ".zip" uses internal/archive's zip extractor; anything else is
// treated as a gzipped tarball. The actual extraction lives in
// internal/archive so the starter-bundle fetch (a later pass) uses the same
// code path rather than a second copy.
func extractBinary(archivePath, dir, goos string) (string, error) {
	if strings.HasSuffix(strings.ToLower(archivePath), ".zip") {
		return archive.ExtractSingleFromZip(archivePath, dir, binaryName(goos))
	}
	return archive.ExtractSingleFromTarGz(archivePath, dir, binaryName(goos))
}

// replaceBinary atomically swaps execPath for tmpPath, which must already
// be in execPath's directory: rename execPath aside as a backup, rename
// tmpPath into execPath's place, then drop the backup. If the second
// rename fails, the backup is restored — grid upgrade never leaves the
// install without a working executable.
func replaceBinary(tmpPath, execPath string) error {
	backup := execPath + ".old"
	os.Remove(backup) // a leftover backup from a prior upgrade must not block this one
	if err := os.Rename(execPath, backup); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("backing up %s before replacing it: %w", execPath, err)
	}
	if err := os.Rename(tmpPath, execPath); err != nil {
		if restoreErr := os.Rename(backup, execPath); restoreErr != nil {
			return fmt.Errorf("replacing %s failed (%s) and restoring the original also failed (%s) — reinstall grid manually", execPath, err, restoreErr)
		}
		return fmt.Errorf("replacing %s: %w (the original binary was restored)", execPath, err)
	}
	os.Remove(backup)
	return nil
}

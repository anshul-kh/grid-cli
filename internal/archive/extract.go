package archive

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrEntryEscapesArchive is returned by ExtractZipTo when
// an archive entry's name, once cleaned, is absolute or contains a ".."
// component and would therefore resolve outside destDir — the classic
// zip-slip path-traversal bug. Same trust boundary, same fix, as
// internal/workspace/gitclone.go's ErrSubdirEscapesRepo: once these
// extractors handle server-supplied archives (the starter-bundle pass), a
// malicious or buggy entry name must never let extraction write outside
// destDir.
var ErrEntryEscapesArchive = errors.New("archive entry escapes the destination directory")

// ErrArchiveContainsSymlink is returned by ExtractZipTo
// when an entry is a symlink (or hard link). GRID_SPEC §4 cross-platform
// rule 5 already bars checks from depending on symlinks; the security
// reasoning is internal/workspace/gitclone.go's ErrStarterRepoContainsSymlink
// one level earlier — a symlink entry committed into a server-supplied
// archive could point anywhere on the local filesystem once followed.
var ErrArchiveContainsSymlink = errors.New("archive contains a symlink")

// maxExtractedEntrySize caps how many bytes a single archive entry may
// expand to during ExtractZipTo. The cap is enforced
// against bytes actually copied, not a header-declared size (which a
// malicious archive can lie about), so a zip/tar bomb cannot fill destDir's
// disk. A var, not a const, so archive_test.go can shrink it to exercise
// the boundary without allocating a real 1 GiB buffer.
var maxExtractedEntrySize int64 = 1 << 30 // 1 GiB

// isAbsoluteEntryPath reports whether a slash- or OS-separated path is
// absolute in a host-independent sense. filepath.IsAbs alone is not enough:
// on Windows a POSIX-absolute name like "/etc/passwd" is not considered
// absolute, so an archive entry or spec path could slip through the escape
// check and be written inside the destination with different semantics.
// Archive entry names and spec paths are always forward-slash paths, so a
// leading "/" is absolute on every host; a leading `\` or a volume name
// ("C:", "\\host\share") covers Windows-style names.
func isAbsoluteEntryPath(p string) bool {
	return strings.HasPrefix(p, "/") ||
		strings.HasPrefix(p, `\`) ||
		filepath.IsAbs(p) ||
		filepath.VolumeName(p) != ""
}

// cleanEntryPath validates a slash-separated archive entry name and returns
// its OS-native, cleaned form relative to the extraction root. It rejects
// an absolute path or one containing a ".." component — the same check
// internal/workspace's rejectEscapingPath applies to server-controlled
// workspace paths, applied here to server-controlled archive entry names.
func cleanEntryPath(name string) (string, error) {
	if isAbsoluteEntryPath(name) {
		return "", fmt.Errorf("%w: %q is an absolute path", ErrEntryEscapesArchive, name)
	}
	rel := filepath.FromSlash(name)
	if isAbsoluteEntryPath(rel) {
		return "", fmt.Errorf("%w: %q is an absolute path", ErrEntryEscapesArchive, name)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", ErrEntryEscapesArchive, name)
	}
	return clean, nil
}

// writeCapped copies r into a new file at dest with the given permissions,
// aborting once more than maxExtractedEntrySize bytes have been copied —
// the decompression-bomb guard shared by ExtractZipTo and ExtractTarGzTo.
func writeCapped(dest string, r io.Reader, mode os.FileMode) error {
	perm := mode.Perm()
	if perm == 0 {
		perm = 0o644
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return fmt.Errorf("creating %s: %w", dest, err)
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(r, maxExtractedEntrySize+1))
	if err != nil {
		return fmt.Errorf("extracting %s: %w", dest, err)
	}
	if n > maxExtractedEntrySize {
		return fmt.Errorf("extracting %s: entry exceeds %d bytes, refusing to continue (possible decompression bomb)", dest, maxExtractedEntrySize)
	}
	return nil
}

// ExtractZipTo extracts every entry of the zip archive at archivePath into
// destDir, which must already exist. Every entry is validated — cleaned
// path must stay inside destDir, and the entry must not be a symlink —
// before any entry is written: zip's central directory lists every entry's
// name and mode up front (archive/zip reads it eagerly in OpenReader), so
// validation never needs to touch the filesystem to run to completion.
// The first invalid entry aborts the whole call with destDir left exactly
// as it was found.
func ExtractZipTo(archivePath, destDir string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("opening zip archive: %w", err)
	}
	defer zr.Close()

	type entry struct {
		rel string
		zf  *zip.File
	}
	entries := make([]entry, 0, len(zr.File))
	for _, zf := range zr.File {
		rel, err := cleanEntryPath(zf.Name)
		if err != nil {
			return err
		}
		if zf.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrArchiveContainsSymlink, zf.Name)
		}
		if rel == "." {
			continue // the archive root itself, nothing to create
		}
		entries = append(entries, entry{rel: rel, zf: zf})
	}

	for _, e := range entries {
		dest := filepath.Join(destDir, e.rel)
		if e.zf.FileInfo().IsDir() {
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return fmt.Errorf("creating directory %s: %w", e.rel, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("creating directory for %s: %w", e.rel, err)
		}
		rc, err := e.zf.Open()
		if err != nil {
			return fmt.Errorf("reading %s from zip: %w", e.zf.Name, err)
		}
		err = writeCapped(dest, rc, e.zf.Mode())
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// ExtractSingleFromTarGz extracts the first regular file in the gzipped tar
// archive at archivePath whose base name equals want into a new temp file
// inside dir, and returns that temp file's path. Moved verbatim from
// internal/upgrade's unexported extractFromTarGz — grid upgrade uses this
// to pull just the "grid"/"grid.exe" binary out of a release archive, so it
// never needs the whole-archive guarantees ExtractZipTo provides: a release
// archive's path escaping or containing a symlink would be an authoring bug
// on grid-cli's own release pipeline, not an attacker surface.
func ExtractSingleFromTarGz(archivePath, dir, want string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("opening gzip archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("reading tar archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != want {
			continue
		}
		return writeTemp(dir, tr)
	}
	return "", fmt.Errorf("the downloaded archive does not contain %q", want)
}

// ExtractSingleFromZip extracts the first file in the zip archive at
// archivePath whose base name equals want into a new temp file inside dir,
// and returns that temp file's path. Moved verbatim from internal/upgrade's
// unexported extractFromZip; see ExtractSingleFromTarGz for why this does
// not need ExtractZipTo's whole-archive traversal/symlink guards.
func ExtractSingleFromZip(archivePath, dir, want string) (string, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", fmt.Errorf("opening zip archive: %w", err)
	}
	defer zr.Close()
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() || filepath.Base(zf.Name) != want {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return "", fmt.Errorf("reading %s from zip: %w", zf.Name, err)
		}
		defer rc.Close()
		return writeTemp(dir, rc)
	}
	return "", fmt.Errorf("the downloaded archive does not contain %q", want)
}

// writeTemp copies r into a new temp file in dir, made executable, and
// returns its path. Moved verbatim from internal/upgrade.
func writeTemp(dir string, r io.Reader) (string, error) {
	tmp, err := os.CreateTemp(dir, ".grid-archive-*.tmp")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	defer tmp.Close()
	if _, err := io.Copy(tmp, r); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("extracting binary: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("making the extracted binary executable: %w", err)
	}
	return tmp.Name(), nil
}

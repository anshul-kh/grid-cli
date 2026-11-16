// Package archive implements the checksum-verified download and
// zip-slip/symlink-safe extraction logic that `grid upgrade` already relied
// on (internal/upgrade, v1 pass 10). It is factored out here so a second
// consumer — the starter-bundle fetch coming in a later pass — reuses this
// exact code path instead of growing its own copy (decision L). Once
// archives `grid run` extracts are server-supplied rather than
// author-published GitHub releases, this package is the trust boundary:
// every exported extractor rejects path-traversal and symlink entries
// before writing a single file, and caps how large any one entry may
// expand to.
package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/anshul-kh/grid-cli/internal/buildinfo"
)

// DownloadToFile downloads url's body to path, returning the body's
// lowercase hex sha256 digest. Moved verbatim from internal/upgrade's
// unexported downloadToFile: callers compare the returned digest against a
// published checksum themselves (checksum mismatch is fatal per GRID_SPEC
// and decision 4 — this function only computes the digest, it never judges
// it, so both grid upgrade and the starter-bundle fetch can apply their own
// "no published checksum" policy around the same download).
func DownloadToFile(ctx context.Context, client *http.Client, url, path string) (sha256hex string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", buildinfo.UserAgent())
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: unexpected status %d", url, resp.StatusCode)
	}
	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("creating %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		return "", fmt.Errorf("downloading %s: %w", url, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

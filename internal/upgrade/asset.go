package upgrade

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/anshul-kh/grid-cli/internal/buildinfo"
)

// selectAsset finds the release asset for goos/goarch: its filename must
// contain goos and goarch as separate tokens (split on "_", "-", "."),
// case-insensitively, and end in the archive extension that os uses
// (".zip" on windows, ".tar.gz" everywhere else) — goreleaser's default
// naming (pass 9's .goreleaser.yaml produces "<project>_<os>_<arch>.<ext>"
// archives; this matches on tokens rather than an exact template so a minor
// naming difference there doesn't break grid upgrade).
func selectAsset(assets []Asset, goos, goarch string) (Asset, bool) {
	wantExt := ".tar.gz"
	if goos == "windows" {
		wantExt = ".zip"
	}
	for _, a := range assets {
		name := strings.ToLower(a.Name)
		if !strings.HasSuffix(name, wantExt) {
			continue
		}
		tokens := strings.FieldsFunc(name, func(r rune) bool {
			return r == '_' || r == '-' || r == '.'
		})
		if containsToken(tokens, goos) && containsToken(tokens, goarch) {
			return a, true
		}
	}
	return Asset{}, false
}

func containsToken(tokens []string, want string) bool {
	for _, t := range tokens {
		if t == want {
			return true
		}
	}
	return false
}

// findChecksumsAsset finds the release's published checksums file
// (goreleaser's default name, "checksums.txt").
func findChecksumsAsset(assets []Asset) (Asset, bool) {
	for _, a := range assets {
		if strings.EqualFold(a.Name, "checksums.txt") {
			return a, true
		}
	}
	return Asset{}, false
}

// parseChecksums parses a goreleaser-style checksums.txt ("<hex>  <filename>"
// per line, two-space-separated) into a filename -> lowercase hex sha256
// map.
func parseChecksums(data []byte) map[string]string {
	sums := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		sums[fields[len(fields)-1]] = strings.ToLower(fields[0])
	}
	return sums
}

// fetchChecksums downloads and parses a release's checksums.txt.
func fetchChecksums(ctx context.Context, client *http.Client, url string) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", buildinfo.UserAgent())
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading checksums.txt: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading checksums.txt from %s: unexpected status %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("downloading checksums.txt: %w", err)
	}
	return parseChecksums(data), nil
}

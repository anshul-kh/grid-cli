package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/anshul-kh/grid-cli/internal/buildinfo"
)

// Asset is one downloadable file attached to a GitHub release — the
// subset of the real API's fields grid upgrade needs.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Release is one GitHub release — the subset of the real API's fields grid
// upgrade needs.
type Release struct {
	TagName    string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

// parseRepo extracts "<owner>/<repo>" from a public GitHub repo URL
// (buildinfo.RepoURL: "https://github.com/<owner>/<repo>").
func parseRepo(repoURL string) (owner, repo string, err error) {
	u, err := url.Parse(repoURL)
	if err != nil {
		return "", "", fmt.Errorf("parsing repo URL %q: %w", repoURL, err)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("repo URL %q is not shaped like https://github.com/<owner>/<repo>", repoURL)
	}
	return parts[0], parts[1], nil
}

// fetchReleases lists every release (GET /repos/{owner}/{repo}/releases) —
// not just GitHub's own notion of "latest" — so selectRelease can apply its
// own semver/draft/prerelease rule uniformly whether talking to the real
// API or the httptest stand-in.
func fetchReleases(ctx context.Context, client *http.Client, apiBase, owner, repo string) ([]Release, error) {
	endpoint := strings.TrimRight(apiBase, "/") + "/repos/" + owner + "/" + repo + "/releases"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", buildinfo.UserAgent())
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching releases from %s: unexpected status %d", endpoint, resp.StatusCode)
	}
	var releases []Release
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("decoding releases: %w", err)
	}
	return releases, nil
}

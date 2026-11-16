// Package upgrade implements `grid upgrade`'s self-update: check GitHub
// releases for a newer semver than the running build, download the release
// asset matching this os/arch, verify it against the release's published
// checksums.txt, and atomically replace the running executable.
//
// Every side effect is injectable through Options (the HTTP client, the
// GitHub API origin, the target os/arch, the executable path) so Run is
// fully unit-testable against an httptest stand-in for GitHub — it never
// needs the real network or a real installed binary to be exercised.
package upgrade

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// ErrInstallFailed marks a failure that happened after a candidate release
// was already found — a checksum mismatch, a malformed archive, or the
// final replace — as opposed to a network/server problem reaching GitHub.
// cmd/upgrade.go maps this to exit 1; an unwrapped error (fetching releases
// or checksums) maps to exit 4.
var ErrInstallFailed = errors.New("could not install the release")

// Options configures a Run. HTTPClient and APIBase default to the real
// GitHub API when left zero; the rest describe the install being upgraded.
type Options struct {
	// HTTPClient issues every request (release list, checksums, asset
	// download). Defaults to http.DefaultClient.
	HTTPClient *http.Client
	// APIBase is the GitHub API origin ("https://api.github.com" in
	// production; an httptest.Server URL in tests).
	APIBase string
	// RepoURL is the public repository URL (buildinfo.RepoURL):
	// "https://github.com/<owner>/<repo>".
	RepoURL string
	// CurrentVersion is the running binary's semver (buildinfo.Version).
	CurrentVersion string
	// OS and Arch select the release asset (runtime.GOOS/GOARCH in
	// production).
	OS, Arch string
	// ExecPath is the path of the running executable to replace
	// (os.Executable() in production).
	ExecPath string
}

// Result is the stable shape `grid upgrade --json` renders (the exact
// currentVersion/latestVersion/upgraded/assetUrl fields the plan fixes);
// PackageManaged is additional, human-output-only context cmd/upgrade.go
// does not put in --json.
type Result struct {
	CurrentVersion string
	LatestVersion  string
	Upgraded       bool
	AssetURL       string
	// PackageManaged names the detected package manager when the install
	// is managed (e.g. "Homebrew"), empty otherwise. grid upgrade refuses
	// to self-replace when this is set.
	PackageManaged string
}

const defaultAPIBase = "https://api.github.com"

// Run checks GitHub releases for a version newer than opts.CurrentVersion
// and, unless dryRun or the install is package-manager-managed, downloads,
// checksum-verifies, and atomically installs it in place of opts.ExecPath.
//
// "Already up to date", "nothing published for this os/arch", and
// "package-manager managed" are informational Results, not errors — only a
// genuine fetch/download/verify/replace failure returns a non-nil error.
func Run(ctx context.Context, opts Options, dryRun bool) (Result, error) {
	res := Result{CurrentVersion: opts.CurrentVersion, LatestVersion: opts.CurrentVersion}

	// A package-manager-managed install is never touched — not even a
	// network call to see what's newer, so `grid upgrade` on a Homebrew
	// install is instant and silent about the rest of this logic.
	if manager, managed := DetectPackageManager(opts.ExecPath); managed {
		res.PackageManaged = manager
		return res, nil
	}

	owner, repo, err := parseRepo(opts.RepoURL)
	if err != nil {
		return res, err
	}
	apiBase := opts.APIBase
	if apiBase == "" {
		apiBase = defaultAPIBase
	}
	client := opts.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	releases, err := fetchReleases(ctx, client, apiBase, owner, repo)
	if err != nil {
		return res, err
	}

	rel, asset, found := selectRelease(releases, opts.CurrentVersion, opts.OS, opts.Arch)
	if !found {
		return res, nil
	}
	res.LatestVersion = rel.TagName
	res.AssetURL = asset.BrowserDownloadURL

	if dryRun {
		return res, nil
	}

	checksumsAsset, ok := findChecksumsAsset(rel.Assets)
	if !ok {
		return res, fmt.Errorf("%w: release %s has no checksums.txt — refusing to install an unverified binary", ErrInstallFailed, rel.TagName)
	}
	checksums, err := fetchChecksums(ctx, client, checksumsAsset.BrowserDownloadURL)
	if err != nil {
		return res, err
	}

	if err := applyUpgrade(ctx, client, asset, checksums, opts.ExecPath, opts.OS); err != nil {
		return res, err
	}
	res.Upgraded = true
	return res, nil
}

// selectRelease picks the non-draft, non-prerelease release with the
// greatest valid semver tag that is newer than current and publishes an
// asset for goos/goarch, skipping every release that doesn't qualify on
// either count.
func selectRelease(releases []Release, current, goos, goarch string) (Release, Asset, bool) {
	var best Release
	var bestAsset Asset
	found := false
	for _, r := range releases {
		if r.Draft || r.Prerelease {
			continue
		}
		if !newerThan(r.TagName, current) {
			continue
		}
		if found && !newerThan(r.TagName, best.TagName) {
			continue
		}
		asset, ok := selectAsset(r.Assets, goos, goarch)
		if !ok {
			continue
		}
		best, bestAsset, found = r, asset, true
	}
	return best, bestAsset, found
}

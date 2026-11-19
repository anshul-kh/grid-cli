package extensions

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// EnvMirror is the dev mirror override: a local directory laid out like the
// frozen mirror contract (the grid-extensions `make sign` dist/ directory).
// Without it the manager points at the future GitHub Releases layout, which
// has no remote yet, and fails with a message that says so.
const EnvMirror = "GRID_EXT_MIRROR"

// EnvCacheDir overrides the cache root for tests (the GRID_DEVICE_KEY_FILE
// precedent: subprocess-touching code never runs against the real home).
const EnvCacheDir = "GRID_EXT_CACHE_DIR"

// DefaultMirrorBase is the GitHub Releases download root for extension
// binaries. A release for tag `v<version>` carries flat, uniquely named
// assets (see remoteAssetName); grid-extensions' release workflow publishes
// exactly that layout.
const DefaultMirrorBase = "https://github.com/anshul-kh/grid-extensions/releases/download"

// httpTimeout bounds one remote mirror fetch.
const httpTimeout = 30 * time.Second

// Manager resolves, downloads, verifies, and caches official extension
// binaries. All methods are safe to call concurrently for different
// extensions; Ensure of one extension is not internally concurrent.
type Manager struct {
	// cacheDir is the cache root (~/.grid/extensions by default).
	cacheDir string
	// getenv is injected (os.Getenv in production) for the mirror and
	// cache-dir seams.
	getenv func(string) string
	// verify checks a candidate binary against its detached signature.
	verify func(bin, sig []byte) error
	// httpGet fetches remote mirror files; injectable for tests.
	httpGet func(url string) ([]byte, error)
}

// NewManager returns the production manager: the default cache root and
// the embedded release key.
func NewManager() (*Manager, error) {
	cacheDir, err := defaultCacheDir(os.Getenv)
	if err != nil {
		return nil, err
	}
	return newManager(cacheDir), nil
}

// NewManagerWithCache is NewManager pointed at an explicit cache root —
// the test seam (the GRID_DEVICE_KEY_FILE precedent: tests never touch the
// real home). Mirror resolution stays env-driven.
func NewManagerWithCache(cacheDir string) *Manager {
	return newManager(cacheDir)
}

func newManager(cacheDir string) *Manager {
	return &Manager{
		cacheDir: cacheDir,
		getenv:   os.Getenv,
		verify:   VerifyBinary,
		httpGet:  httpGetBytes,
	}
}

// defaultCacheDir resolves the cache root: the EnvCacheDir seam, else
// ~/.grid/extensions (ARCH §3.2's fixed location — the extension cache is
// deliberately not under the per-OS config dir, so it survives config
// resets and reads identically in every shell).
func defaultCacheDir(getenv func(string) string) (string, error) {
	if dir := getenv(EnvCacheDir); dir != "" {
		return dir, nil
	}
	home, err := homeDir(getenv)
	if err != nil {
		return "", fmt.Errorf("resolving the extension cache directory: %w", err)
	}
	return filepath.Join(home, ".grid", "extensions"), nil
}

func homeDir(getenv func(string) string) (string, error) {
	if h := getenv("HOME"); h != "" {
		return h, nil
	}
	if h := getenv("USERPROFILE"); h != "" {
		return h, nil
	}
	return "", fmt.Errorf("neither HOME nor USERPROFILE is set; cannot resolve the extension cache directory")
}

// BinaryName is the executable filename for an extension on this platform
// ("grid-ext-loadtest", "grid-ext-loadtest.exe" on Windows).
func BinaryName(name string) string {
	if runtime.GOOS == "windows" {
		return "grid-ext-" + name + ".exe"
	}
	return "grid-ext-" + name
}

// extDirReplacer rejects anything that could escape its cache/mirror
// directory: extension and version strings become path segments.
func validSegment(s string) bool {
	return s != "" && s != "." && s != ".." &&
		!strings.ContainsAny(s, `/\`) && !strings.ContainsRune(s, 0)
}

// Ensure returns the cache path of the official, signature-verified binary
// for name@version, downloading it from the mirror first if the cache has
// no verified copy. The returned bool reports whether this call downloaded
// (vs served a verified cache hit). A binary that fails verification is
// deleted immediately and never cached or executed (ARCH §3.2 step 4).
func (m *Manager) Ensure(name, version string) (path string, downloaded bool, err error) {
	if !validSegment(name) || !validSegment(version) {
		return "", false, fmt.Errorf("invalid extension pin %q@%q: name and version must be plain directory-safe segments", name, version)
	}
	dir := filepath.Join(m.cacheDir, name, version)
	path = filepath.Join(dir, BinaryName(name))
	sigPath := path + ".sig"

	if bin, sig, ok := readCached(path, sigPath); ok {
		if err := m.verify(bin, sig); err == nil {
			return path, false, nil
		}
		// Tampered or unsigned cache entry: delete it immediately, then
		// re-derive from the mirror below.
		_ = os.Remove(path)
		_ = os.Remove(sigPath)
	}

	bin, sig, err := m.fetch(name, version)
	if err != nil {
		return "", false, err
	}
	if err := m.verify(bin, sig); err != nil {
		// Never written anywhere; nothing to delete — the tampered copy
		// stays only in the mirror it came from.
		return "", false, fmt.Errorf("grid-ext-%s %s: %w", name, version, err)
	}
	if err := writeExecutable(dir, path, bin); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(sigPath, sig, 0o644); err != nil {
		return "", false, fmt.Errorf("writing the extension signature cache: %w", err)
	}
	return path, true, nil
}

// readCached reports whether a complete, non-empty cache entry exists.
func readCached(path, sigPath string) ([]byte, []byte, bool) {
	bin, err := os.ReadFile(path)
	if err != nil || len(bin) == 0 {
		return nil, nil, false
	}
	sig, err := os.ReadFile(sigPath)
	if err != nil || len(sig) == 0 {
		return nil, nil, false
	}
	return bin, sig, true
}

// writeExecutable atomically installs a verified binary: temp file in the
// target directory, chmod +x, rename (store.go's temp+rename discipline —
// a partially written extension must never exist at the cache path).
func writeExecutable(dir, path string, bin []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating the extension cache directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".grid-ext-*")
	if err != nil {
		return fmt.Errorf("staging the extension download: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("staging the extension download: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("staging the extension download: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("marking the extension executable: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("installing the extension into the cache: %w", err)
	}
	return nil
}

// fetch reads the binary and its detached signature from the configured
// mirror: a local directory when GRID_EXT_MIRROR is set (the dev loop
// points it at grid-extensions' signed dist/), else the future GitHub
// Releases layout.
func (m *Manager) fetch(name, version string) ([]byte, []byte, error) {
	if mirror := m.getenv(EnvMirror); mirror != "" {
		return m.fetchLocal(mirror, name, version)
	}
	return m.fetchRemote(name, version)
}

// mirrorRelPath is the frozen mirror layout (Pass 6 executor instruction 1,
// produced by grid-extensions' `make sign`):
//
//	<root>/grid-ext-<name>/<version>/<os>-<arch>/grid-ext-<name>[.exe]
//
// with the detached .sig as a sibling of the binary. Forward slashes only —
// it doubles as the remote URL path; local use converts per platform.
func mirrorRelPath(name, version string) (string, string) {
	bin := "grid-ext-" + name + "/" + version + "/" + runtime.GOOS + "-" + runtime.GOARCH + "/" + BinaryName(name)
	return bin, bin + ".sig"
}

func (m *Manager) fetchLocal(mirror, name, version string) ([]byte, []byte, error) {
	binRel, sigRel := mirrorRelPath(name, version)
	bin, err := os.ReadFile(filepath.Join(mirror, filepath.FromSlash(binRel)))
	if err != nil {
		return nil, nil, fmt.Errorf("reading grid-ext-%s %s from the extension mirror %s: %w (the mirror must follow the make sign dist layout, e.g. grid-ext-%s/%s/%s-%s/...)",
			name, version, mirror, err, name, version, runtime.GOOS, runtime.GOARCH)
	}
	sig, err := os.ReadFile(filepath.Join(mirror, filepath.FromSlash(sigRel)))
	if err != nil {
		return nil, nil, fmt.Errorf("reading grid-ext-%s %s's detached signature from the extension mirror %s: %w", name, version, mirror, err)
	}
	return bin, sig, nil
}

func (m *Manager) fetchRemote(name, version string) ([]byte, []byte, error) {
	binAsset, sigAsset := remoteAssetName(name, version, runtime.GOOS, runtime.GOARCH)
	base := DefaultMirrorBase + "/" + remoteTag(version) + "/"
	bin, err := m.httpGet(base + binAsset)
	if err != nil {
		return nil, nil, fmt.Errorf("downloading grid-ext-%s %s from %s%s: %w — check that release %s has that asset, or for development set %s to a signed dist directory (e.g. ../grid-extensions/dist)",
			name, version, base, binAsset, err, remoteTag(version), EnvMirror)
	}
	sig, err := m.httpGet(base + sigAsset)
	if err != nil {
		return nil, nil, fmt.Errorf("downloading grid-ext-%s %s's detached signature from %s%s: %w — check that release %s has that asset, or for development set %s to a signed dist directory",
			name, version, base, sigAsset, err, remoteTag(version), EnvMirror)
	}
	return bin, sig, nil
}

// remoteTag turns a pinned version ("0.1.0") into the release tag it is
// published under ("v0.1.0"); a version already carrying the prefix is used
// as given.
func remoteTag(version string) string {
	if strings.HasPrefix(version, "v") {
		return version
	}
	return "v" + version
}

// remoteAssetName is the flat GitHub release asset name for a binary and its
// detached signature. Release assets have no directory structure, so os/arch
// live in the filename; the tag is in the URL, not the name.
func remoteAssetName(name, version, goos, goarch string) (bin, sig string) {
	base := fmt.Sprintf("grid-ext-%s_%s_%s-%s", name, version, goos, goarch)
	return base, base + ".sig"
}

func httpGetBytes(url string) ([]byte, error) {
	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<20))
	if err != nil {
		return nil, err
	}
	return body, nil
}

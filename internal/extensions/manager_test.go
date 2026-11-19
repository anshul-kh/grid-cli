package extensions

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/anshul-kh/grid-extensions/pkg/attest"
)

// newTestManager builds a Manager over a temp cache dir and a temp mirror
// dir whose binaries the test key signs — the same shape as production
// (mirror → verify → cache) with only the key injected, since the real
// release private half is never committed.
func newTestManager(t *testing.T, key ed25519.PrivateKey) (*Manager, string) {
	t.Helper()
	cache := t.TempDir()
	mirror := t.TempDir()
	m := &Manager{
		cacheDir: cache,
		getenv: func(k string) string {
			if k == EnvMirror {
				return mirror
			}
			return ""
		},
		verify:  func(bin, sig []byte) error { return VerifySignature(key.Public().(ed25519.PublicKey), bin, sig) },
		httpGet: httpGetBytes,
	}
	return m, mirror
}

// writeMirror installs a signed binary (+ detached sig) into the mirror in
// the frozen `make sign` dist layout for this platform.
func writeMirror(t *testing.T, mirror string, name, version string, bin []byte, key ed25519.PrivateKey) string {
	t.Helper()
	rel := fmt.Sprintf("grid-ext-%s/%s/%s-%s", name, version, runtime.GOOS, runtime.GOARCH)
	dir := filepath.Join(mirror, filepath.FromSlash(rel))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(dir, BinaryName(name))
	if err := os.WriteFile(binPath, bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binPath+".sig", attest.Sign(key, bin), 0o644); err != nil {
		t.Fatal(err)
	}
	return binPath
}

// cachedPaths are the cache locations Ensure must manage.
func cachedPaths(cache, name, version string) (string, string) {
	dir := filepath.Join(cache, name, version)
	bin := filepath.Join(dir, BinaryName(name))
	return bin, bin + ".sig"
}

func testBin(t *testing.T) []byte {
	t.Helper()
	return []byte("fake grid-ext binary payload \x00\x01")
}

func TestEnsureDownloadsVerifiesAndCaches(t *testing.T) {
	key := attest.DeterministicTestKey()
	m, mirror := newTestManager(t, key)
	if err := os.WriteFile(filepath.Join(mirror, "unrelated.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeMirror(t, mirror, "loadtest", "0.1.0", testBin(t), key)

	path, downloaded, err := m.Ensure("loadtest", "0.1.0")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !downloaded {
		t.Error("first Ensure reports downloaded=false, want true (cache was empty)")
	}
	if filepath.Base(path) != BinaryName("loadtest") {
		t.Errorf("path = %s, want the cache path under ~/.grid/extensions layout", path)
	}
	bin, sigPath := cachedPaths(m.cacheDir, "loadtest", "0.1.0")
	if path != bin {
		t.Errorf("path = %s, want %s", path, bin)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("cached binary missing: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		t.Errorf("cached binary mode %v, want executable", info.Mode())
	}
	if _, err := os.Stat(sigPath); err != nil {
		t.Errorf("cached .sig missing: %v", err)
	}

	// Cache hit: remove the mirror entirely — a second Ensure must succeed
	// without anything to download from.
	os.RemoveAll(mirror)
	path2, downloaded2, err := m.Ensure("loadtest", "0.1.0")
	if err != nil {
		t.Fatalf("second Ensure (cache hit): %v", err)
	}
	if downloaded2 {
		t.Error("second Ensure reports downloaded=true, want false (verified cache hit must not re-download)")
	}
	if path2 != path {
		t.Errorf("second Ensure path = %s, want %s", path2, path)
	}
}

func TestEnsureRejectsTamperedMirrorBinary(t *testing.T) {
	key := attest.DeterministicTestKey()
	m, mirror := newTestManager(t, key)
	binPath := writeMirror(t, mirror, "loadtest", "0.1.0", testBin(t), key)
	tampered := append([]byte(nil), testBin(t)...)
	tampered[len(tampered)-1] ^= 0xff
	if err := os.WriteFile(binPath, tampered, 0o755); err != nil {
		t.Fatal(err)
	}

	_, _, err := m.Ensure("loadtest", "0.1.0")
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("Ensure error = %v, want ErrBadSignature", err)
	}
	binPathCached, _ := cachedPaths(m.cacheDir, "loadtest", "0.1.0")
	if _, statErr := os.Stat(binPathCached); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("tampered binary was cached: stat = %v, want ErrNotExist", statErr)
	}
}

func TestEnsureDeletesTamperedCachedBinary(t *testing.T) {
	key := attest.DeterministicTestKey()
	m, mirror := newTestManager(t, key)
	mirrorBinPath := writeMirror(t, mirror, "loadtest", "0.1.0", testBin(t), key)
	path, _, err := m.Ensure("loadtest", "0.1.0")
	if err != nil {
		t.Fatalf("first Ensure: %v", err)
	}

	// Flip one byte in the cached binary — and in the mirror source too, so
	// the only copies left are tampered: the cache entry must be deleted,
	// the mirror copy rejected, and ErrBadSignature surfaced. (With a good
	// mirror the tampered cache is instead silently replaced — the
	// re-download assertion in TestEnsureDownloadsVerifiesAndCaches covers
	// that half.)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 0xff
	if err := os.WriteFile(path, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	mirrorRaw, err := os.ReadFile(mirrorBinPath)
	if err != nil {
		t.Fatal(err)
	}
	mirrorRaw[len(mirrorRaw)/2] ^= 0xff
	if err := os.WriteFile(mirrorBinPath, mirrorRaw, 0o755); err != nil {
		t.Fatal(err)
	}

	_, _, err = m.Ensure("loadtest", "0.1.0")
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("Ensure error = %v, want ErrBadSignature for the flipped byte", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("tampered cached binary still present after Ensure: stat = %v, want deleted", statErr)
	}
}

func TestEnsureRejectsUnsignedBinary(t *testing.T) {
	key := attest.DeterministicTestKey()
	m, mirror := newTestManager(t, key)
	binPath := writeMirror(t, mirror, "loadtest", "0.1.0", testBin(t), key)

	// A garbage signature fails the verifier.
	if err := os.WriteFile(binPath+".sig", []byte("not a signature"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := m.Ensure("loadtest", "0.1.0")
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("Ensure error = %v, want ErrBadSignature for the unsigned binary", err)
	}
	binPathCached, _ := cachedPaths(m.cacheDir, "loadtest", "0.1.0")
	if _, statErr := os.Stat(binPathCached); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("unsigned binary was cached: stat = %v, want ErrNotExist", statErr)
	}

	// A mirror with no signature at all is also refused (a different error
	// class — missing data, not a bad one) and equally never cached.
	if err := os.Remove(binPath + ".sig"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Ensure("loadtest", "0.1.0"); err == nil {
		t.Error("Ensure with a missing .sig: want an error, got nil")
	}
}

func TestEnsureRejectsPathEscapingPins(t *testing.T) {
	key := attest.DeterministicTestKey()
	m, _ := newTestManager(t, key)
	for _, pin := range [][2]string{{"../evil", "0.1.0"}, {"loadtest", ".."}, {"a/b", "0.1.0"}} {
		if _, _, err := m.Ensure(pin[0], pin[1]); err == nil {
			t.Errorf("Ensure(%q, %q): want an error, got nil", pin[0], pin[1])
		}
	}
}

func TestEnsureRemoteMirrorLayout(t *testing.T) {
	key := attest.DeterministicTestKey()
	cache := t.TempDir()
	var gotURLs []string
	m := &Manager{
		cacheDir: cache,
		getenv:   func(string) string { return "" },
		verify:   func(bin, sig []byte) error { return VerifySignature(key.Public().(ed25519.PublicKey), bin, sig) },
		httpGet: func(url string) ([]byte, error) {
			gotURLs = append(gotURLs, url)
			if strings.HasSuffix(url, ".sig") {
				return attest.Sign(key, testBin(t)), nil
			}
			return testBin(t), nil
		},
	}

	path, downloaded, err := m.Ensure("loadtest", "0.1.0")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !downloaded {
		t.Error("downloaded = false, want true")
	}
	rel := fmt.Sprintf("/v0.1.0/grid-ext-loadtest_0.1.0_%s-%s", runtime.GOOS, runtime.GOARCH)
	if len(gotURLs) != 2 {
		t.Fatalf("fetched %d URLs, want 2 (binary + signature): %v", len(gotURLs), gotURLs)
	}
	for i, url := range gotURLs {
		want := DefaultMirrorBase + rel
		if i == 1 {
			want += ".sig"
		}
		if url != want {
			t.Errorf("fetched %q, want the frozen mirror layout %q", url, want)
		}
	}
	if path != filepath.Join(cache, "loadtest", "0.1.0", BinaryName("loadtest")) {
		t.Errorf("path = %s, want the cache path", path)
	}
}

func TestEnsureRemoteWithoutRemoteNamesDevMirror(t *testing.T) {
	cache := t.TempDir()
	m := &Manager{
		cacheDir: cache,
		getenv:   func(string) string { return "" },
		verify:   VerifyBinary,
		httpGet:  func(string) ([]byte, error) { return nil, errors.New("no such host") },
	}
	_, _, err := m.Ensure("loadtest", "0.1.0")
	if err == nil {
		t.Fatal("Ensure with no mirror configured: want an error, got nil")
	}
	if !strings.Contains(err.Error(), EnvMirror) {
		t.Errorf("error %v does not mention %s — the dev mirror hint is the whole point", err, EnvMirror)
	}
}

// TestVerifyBinaryAgainstRealReleaseKey is the live half: the real signed
// dist binary produced by grid-extensions' `make build && make sign`
// verifies against the embedded dev root key — and a flipped byte does
// not. Skipped where the sibling dist/ has not been built.
func TestVerifyBinaryAgainstRealReleaseKey(t *testing.T) {
	rel := filepath.Join("..", "..", "grid-extensions", "dist", "grid-ext-loadtest", "0.1.0",
		runtime.GOOS+"-"+runtime.GOARCH, BinaryName("loadtest"))
	bin, err := os.ReadFile(rel)
	if err != nil {
		t.Skipf("no signed dist binary at %s — run make sign in grid-extensions (Cluster M artifacts): %v", rel, err)
	}
	sig, err := os.ReadFile(rel + ".sig")
	if err != nil {
		t.Fatalf("dist binary exists but its .sig is missing: %v", err)
	}
	if err := VerifyBinary(bin, sig); err != nil {
		t.Errorf("the real dist binary fails the embedded release key: %v", err)
	}
	tampered := append([]byte(nil), bin...)
	tampered[len(tampered)/2] ^= 0xff
	if err := VerifyBinary(tampered, sig); !errors.Is(err, ErrBadSignature) {
		t.Errorf("flipped-byte binary: error = %v, want ErrBadSignature", err)
	}
}

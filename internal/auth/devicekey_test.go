package auth

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestDeviceKeyFileRoundTrip proves the file store's §5.1 contract: first
// load generates and persists, second load returns the same keypair.
func TestDeviceKeyFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", deviceKeyFile)

	key1, err := LoadOrCreateDeviceKeyFile(path)
	if err != nil {
		t.Fatalf("first LoadOrCreateDeviceKeyFile: %v", err)
	}
	if len(key1) != ed25519.PrivateKeySize {
		t.Fatalf("generated key is %d bytes, want %d", len(key1), ed25519.PrivateKeySize)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("device key file not persisted: %v", err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("device key mode = %o, want 600", fi.Mode().Perm())
	}

	key2, err := LoadOrCreateDeviceKeyFile(path)
	if err != nil {
		t.Fatalf("second LoadOrCreateDeviceKeyFile: %v", err)
	}
	if !key1.Equal(key2) {
		t.Error("re-login must reuse the existing device keypair, not generate a new one")
	}
}

// TestDeviceKeyFileRegisteredPubkeyMatches drives the re-login contract one
// step further: the public half derived from the reloaded key is the same
// one that was registered at device/start the first time.
func TestDeviceKeyFileRegisteredPubkeyMatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), deviceKeyFile)

	key1, err := LoadOrCreateDeviceKeyFile(path)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	key2, err := LoadOrCreateDeviceKeyFile(path)
	if err != nil {
		t.Fatalf("re-login load: %v", err)
	}
	pub1, ok := key1.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatalf("public half is %T, want ed25519.PublicKey", key1.Public())
	}
	pub2, ok := key2.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatalf("public half is %T, want ed25519.PublicKey", key2.Public())
	}
	if !pub1.Equal(pub2) {
		t.Error("the reloaded keypair's public half differs from the registered one")
	}
}

// TestDeviceKeyFileCorruptIsAnError: a corrupt file must fail loudly, never
// silently generate a replacement — the server would reject every later
// envelope signed by the new key with 422 invalid_device_signature.
func TestDeviceKeyFileCorruptIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), deviceKeyFile)
	if err := os.WriteFile(path, []byte("not-base64-or-a-key!!"), 0o600); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	if _, err := LoadOrCreateDeviceKeyFile(path); err == nil {
		t.Fatal("a corrupt device key file must be an error, not a silent regeneration")
	}
}

// TestLoadOrCreateDeviceKeyHonorsEnvOverride pins the EnvDeviceKeyFile seam:
// with it set, the default loader uses the file store (so sandboxed tests
// and CI never touch a real keychain).
func TestLoadOrCreateDeviceKeyHonorsEnvOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), deviceKeyFile)
	t.Setenv(EnvDeviceKeyFile, path)

	key1, err := LoadOrCreateDeviceKey()
	if err != nil {
		t.Fatalf("LoadOrCreateDeviceKey with %s set: %v", EnvDeviceKeyFile, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("key must land at the override path: %v", err)
	}
	key2, err := LoadOrCreateDeviceKey()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !key1.Equal(key2) {
		t.Error("env-overridden store must reuse the persisted keypair")
	}
}

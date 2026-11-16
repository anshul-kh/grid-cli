// Package auth: devicekey.go owns the GRID_SPEC §5.1 device keypair —
// the Ed25519 key that signs every submission envelope (§5.3). Generated
// at first `grid login`, reused on every re-login; the private half never
// leaves this machine's keychain/config dir and is never printed or logged.
package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/anshul-kh/grid-cli/internal/config"
)

// EnvDeviceKeyFile, when set, stores the device key at that path instead of
// the OS default (Keychain on darwin, config dir elsewhere) — the test/dev
// seam that keeps sandboxed homes off the real keychain.
const EnvDeviceKeyFile = "GRID_DEVICE_KEY_FILE"

const (
	keychainService  = "grid"
	keychainAccount  = "device-key"
	keychainNotFound = 44 // security(1) exit code: item not in the keychain
	deviceKeyFile    = "device-key.pem"
	keyCmdTimeout    = 10 * time.Second
)

// LoadOrCreateDeviceKey returns this machine's Ed25519 device keypair,
// generating and persisting it on first use (GRID_SPEC §5.1). Re-login
// reuses the existing pair: the public half registered at device/start must
// keep verifying the envelopes this key signs.
func LoadOrCreateDeviceKey() (ed25519.PrivateKey, error) {
	if path := os.Getenv(EnvDeviceKeyFile); path != "" {
		return LoadOrCreateDeviceKeyFile(path)
	}
	if runtime.GOOS == "darwin" {
		return loadOrCreateKeychainDeviceKey()
	}
	dir, err := config.ConfigDir()
	if err != nil {
		return nil, err
	}
	return LoadOrCreateDeviceKeyFile(filepath.Join(dir, deviceKeyFile))
}

// LoadOrCreateDeviceKeyFile is the file-backed store: base64 of the raw
// 64-byte private key in a 0600 file, atomic write per store.go's
// temp+chmod+rename discipline.
func LoadOrCreateDeviceKeyFile(path string) (ed25519.PrivateKey, error) {
	key, err := readDeviceKeyFile(path)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	_, key, err = ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating the device keypair: %w", err)
	}
	if err := writeDeviceKeyFile(path, key); err != nil {
		return nil, err
	}
	return key, nil
}

func readDeviceKeyFile(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := decodeDeviceKey(data)
	if err != nil {
		return nil, fmt.Errorf("device key file at %s is corrupt: %w", path, err)
	}
	return key, nil
}

func decodeDeviceKey(data []byte) (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("device key is %d bytes, want %d", len(raw), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(raw), nil
}

func writeDeviceKeyFile(path string, key ed25519.PrivateKey) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating config directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".device-key-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp device key file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { os.Remove(tmpName) }()
	data := []byte(base64.StdEncoding.EncodeToString(key) + "\n")
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp device key file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing temp device key file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp device key file: %w", err)
	}
	// chmod after write: 0600 regardless of umask (store.go's discipline).
	if err := os.Chmod(tmpName, fs.FileMode(0o600)); err != nil {
		return fmt.Errorf("restricting device key file permissions: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("installing device key file: %w", err)
	}
	return nil
}

// loadOrCreateKeychainDeviceKey is the darwin store: `security
// add-generic-password -s grid -a device-key` / `find-generic-password`
// (GRID_SPEC §5.1).
func loadOrCreateKeychainDeviceKey() (ed25519.PrivateKey, error) {
	key, err := findKeychainDeviceKey()
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	_, key, err = ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating the device keypair: %w", err)
	}
	if err := addKeychainDeviceKey(key); err != nil {
		return nil, err
	}
	return key, nil
}

func findKeychainDeviceKey() (ed25519.PrivateKey, error) {
	ctx, cancel := context.WithTimeout(context.Background(), keyCmdTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "security", "find-generic-password",
		"-s", keychainService, "-a", keychainAccount, "-w").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == keychainNotFound {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("reading the device key from the Keychain: %w", err)
	}
	key, err := decodeDeviceKey(out)
	if err != nil {
		return nil, fmt.Errorf("the Keychain's grid device-key item is corrupt: %w", err)
	}
	return key, nil
}

func addKeychainDeviceKey(key ed25519.PrivateKey) error {
	ctx, cancel := context.WithTimeout(context.Background(), keyCmdTimeout)
	defer cancel()
	// -U: create the item, or update it if one already exists.
	err := exec.CommandContext(ctx, "security", "add-generic-password",
		"-U", "-s", keychainService, "-a", keychainAccount,
		"-w", base64.StdEncoding.EncodeToString(key)).Run()
	if err != nil {
		return fmt.Errorf("storing the device key in the Keychain: %w", err)
	}
	return nil
}

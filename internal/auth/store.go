// Package auth implements the GRID_SPEC §5.1 device-code login flow and the
// cross-platform credential store it writes.
//
// This file (store.go) owns credentials.json: read/write at the
// internal/config/paths.go location, atomic writes (temp file + rename),
// and mode 0600 on unix.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/anshul-kh/grid-cli/internal/api"
	"github.com/anshul-kh/grid-cli/internal/config"
)

// credentialFileMode is the credentials file permission on unix (TASK.md
// §5): owner read/write only. On Windows, POSIX mode bits do not govern
// access — ACLs do — and os.Chmod only toggles the read-only bit there; the
// file instead inherits the per-user ACL of %AppData%, which already keeps
// other accounts out. That Windows limitation is accepted and documented
// here per TASK.md §5 ("at minimum document the limitation").
const credentialFileMode fs.FileMode = 0o600

// ErrNotLoggedIn is returned by Store.Load when there are no stored
// credentials (the file is missing or unreadable as credentials).
var ErrNotLoggedIn = errors.New("not logged in")

// Credentials is the credentials.json payload: the session key from device
// pairing (GRID_SPEC §5.1), the paired user, and when the credentials were
// stored. The session key is a secret: it is written only to this 0600 file,
// sent only as the Bearer token / HMAC key, and never printed or logged.
type Credentials struct {
	SessionKey string    `json:"sessionKey"`
	User       api.User  `json:"user"`
	CreatedAt  time.Time `json:"createdAt"`
}

// Store reads and writes the credentials file at a fixed path. Writes are
// atomic (write a sibling temp file, sync, chmod, rename) so a crash never
// leaves a partial or world-readable credentials file behind.
type Store struct {
	path string
}

// NewStore returns a Store whose credentials file lives at path.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// DefaultStore returns the Store at the cross-platform config location from
// internal/config/paths.go (TASK.md §5): %AppData%\grid on Windows,
// $XDG_CONFIG_HOME/grid or ~/.config/grid on Linux,
// ~/Library/Application Support/grid on macOS.
func DefaultStore() (*Store, error) {
	path, err := config.CredentialsPath()
	if err != nil {
		return nil, err
	}
	return NewStore(path), nil
}

// Path returns the credentials file path.
func (s *Store) Path() string { return s.path }

// Load reads the credentials file. A missing file is ErrNotLoggedIn so
// callers can distinguish "no session yet" from a broken file.
func (s *Store) Load() (Credentials, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Credentials{}, fmt.Errorf("%w: no credentials at %s", ErrNotLoggedIn, s.path)
		}
		return Credentials{}, err
	}
	var c Credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return Credentials{}, fmt.Errorf("credentials file at %s is corrupt: %w", s.path, err)
	}
	return c, nil
}

// Save atomically writes the credentials file with mode 0600. The temp file
// is created in the same directory (so rename stays on one filesystem),
// written, synced, chmod'd after write (umask-safe creation per the plan),
// and renamed into place. A chmod failure or rename failure removes the temp
// file — the previous credentials, if any, are never lost.
func (s *Store) Save(c Credentials) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating config directory %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp credentials file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		// Best-effort cleanup; a no-op once the rename succeeded.
		os.Remove(tmpName)
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp credentials file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing temp credentials file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp credentials file: %w", err)
	}
	// Explicit chmod after write: the file must be 0600 no matter what the
	// process umask allowed at creation time.
	if err := os.Chmod(tmpName, credentialFileMode); err != nil {
		return fmt.Errorf("restricting credentials file permissions: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("installing credentials file: %w", err)
	}
	return nil
}

// Delete removes the credentials file. Deleting an absent file is not an
// error: logout is idempotent and must succeed locally even when there is
// nothing to remove.
func (s *Store) Delete() error {
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing credentials file %s: %w", s.path, err)
	}
	return nil
}

package auth

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/anshul-kh/grid-cli/internal/api"
)

// testCredentials builds a fixed credential set for round-trip tests. The
// session key is compared, never printed.
func testCredentials() Credentials {
	return Credentials{
		SessionKey: "test-session-key-do-not-print",
		User: api.User{
			ID:          "0d0a1f2e-5c4b-4a3d-9e8f-7a6b5c4d3e2f",
			Username:    "grid-tester",
			DisplayName: "Grid Tester",
			AvatarURL:   "https://grid.dev/img/tester.png",
		},
		CreatedAt: time.Date(2026, 11, 12, 0, 30, 0, 0, time.UTC),
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	s := NewStore(path)

	// A fresh store reports not-logged-in, not a generic error.
	if _, err := s.Load(); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("Load on missing file: err = %v, want ErrNotLoggedIn", err)
	}

	creds := testCredentials()
	if err := s.Save(creds); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Session keys are compared, never interpolated into failure messages.
	if got.SessionKey != creds.SessionKey {
		t.Error("SessionKey mismatch after round-trip (values not printed)")
	}
	if got.User != creds.User {
		t.Errorf("User = %+v, want %+v", got.User, creds.User)
	}
	if !got.CreatedAt.Equal(creds.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, creds.CreatedAt)
	}

	// Save overwrites in place and leaves no temp files behind.
	if err := s.Save(creds); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "credentials.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory contains %v, want exactly [credentials.json] (no leftover temp files)", names)
	}

	// Delete removes the file; deleting again is not an error (logout is
	// idempotent).
	if err := s.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Delete(); err != nil {
		t.Fatalf("second Delete: %v, want nil", err)
	}
	if _, err := s.Load(); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("Load after Delete: err = %v, want ErrNotLoggedIn", err)
	}
}

func TestStoreCredentialsFileModeIs0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows governs access with ACLs, not POSIX mode bits; the
		// assertion is unix-only (TASK.md §5, plan Assumption 11).
		t.Skip("mode bits are not meaningful on Windows; covered by code review + CI unix runs")
	}
	path := filepath.Join(t.TempDir(), "nested", "credentials.json")
	s := NewStore(path)
	if err := s.Save(testCredentials()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("credentials file mode = %o, want 600", got)
	}
}

func TestDefaultStoreUsesConfigPath(t *testing.T) {
	// The cross-platform directory resolution itself is unit-tested per GOOS
	// in internal/config (pass 1); here we pin that DefaultStore lands at
	// CredentialsPath with credentials.json inside a "grid" directory.
	// Setting every HOME-like variable makes the test behave identically on
	// darwin, linux, and windows hosts.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))

	s, err := DefaultStore()
	if err != nil {
		t.Fatalf("DefaultStore: %v", err)
	}
	dir := filepath.Dir(s.Path())
	if filepath.Base(dir) != "grid" {
		t.Errorf("credentials dir base = %q, want %q", filepath.Base(dir), "grid")
	}
	if filepath.Base(s.Path()) != "credentials.json" {
		t.Errorf("credentials file base = %q, want credentials.json", filepath.Base(s.Path()))
	}
	want := map[string]string{
		"darwin":  filepath.Join(home, "Library", "Application Support", "grid", "credentials.json"),
		"windows": filepath.Join(home, "AppData", "grid", "credentials.json"),
	}[runtime.GOOS]
	if want == "" {
		want = filepath.Join(home, ".config", "grid", "credentials.json")
	}
	if s.Path() != want {
		t.Errorf("DefaultStore path = %q, want %q", s.Path(), want)
	}

	// A save through the default store round-trips at that path.
	if err := s.Save(testCredentials()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

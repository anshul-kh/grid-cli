package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// ConfigDir returns the directory holding grid CLI state (credentials,
// settings). Resolution is cross-platform (TASK.md §5):
//
//	Windows:            %AppData%\grid
//	Linux and others:   $XDG_CONFIG_HOME/grid, else ~/.config/grid
//	macOS:              ~/Library/Application Support/grid
//
// The pure helper configDir(goos, getenv) exists so tests can inject env vars
// and target operating systems without depending on the host.
func ConfigDir() (string, error) {
	return configDir(runtime.GOOS, os.Getenv)
}

// CredentialsPath returns the path of the credentials file inside ConfigDir
// (credentials.json). Used by the auth store in a later pass.
func CredentialsPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "credentials.json"), nil
}

// AttemptsDir returns the directory holding per-lesson attempt state for
// workspace.mode: none lessons (GRID_SPEC §4) — the only mode that
// scaffolds no workspace directory, so `grid run` has nowhere local to
// write .grid/attempt.json. internal/workspace.StateDir joins a lesson id
// onto this to get that lesson's pseudo-workspace directory.
func AttemptsDir() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "attempts"), nil
}

// DefaultWebURL is the built-in base URL for the Grid web app (grid-ghost).
// It is only ever used as a last resort, when a CLI command has a message
// to print that points at the web app but has made no network call to ask
// the server for its real URL (e.g. "no open attempt anywhere" — there is
// no enrollUrl to echo back, because nothing was fetched).
const DefaultWebURL = "http://localhost:3000"

// EnvWebURL overrides DefaultWebURL for local dev against a non-default
// port, mirroring EnvAPIURL's precedence.
const EnvWebURL = "GRID_WEB_URL"

// WebURL resolves the effective web app base URL: EnvWebURL if set,
// otherwise DefaultWebURL. getenv is injected so callers (and tests) don't
// need a global flag for what is, today, a single hardcoded fallback.
func WebURL(getenv func(string) string) string {
	if v := getenv(EnvWebURL); v != "" {
		return v
	}
	return DefaultWebURL
}

func configDir(goos string, getenv func(string) string) (string, error) {
	switch goos {
	case "windows":
		// %AppData%\grid. Note: on Windows, file permissions are governed by
		// ACLs, not the POSIX mode bits; the auth store documents that
		// limitation where it writes the credentials file (TASK.md §5).
		appData := getenv("AppData")
		if appData == "" {
			return "", errors.New("AppData is not set; cannot resolve the grid config directory")
		}
		return filepath.Join(appData, "grid"), nil
	case "darwin":
		home, err := homeDir(getenv)
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "grid"), nil
	default:
		// XDG base directory: $XDG_CONFIG_HOME when set, else ~/.config.
		if xdg := getenv("XDG_CONFIG_HOME"); xdg != "" {
			return filepath.Join(xdg, "grid"), nil
		}
		home, err := homeDir(getenv)
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config", "grid"), nil
	}
}

func homeDir(getenv func(string) string) (string, error) {
	if h := getenv("HOME"); h != "" {
		return h, nil
	}
	// USERPROFILE is the Windows equivalent; accepted everywhere so the
	// Windows path can be exercised on any host in tests.
	if h := getenv("USERPROFILE"); h != "" {
		return h, nil
	}
	return "", errors.New("neither HOME nor USERPROFILE is set; cannot resolve the home directory")
}

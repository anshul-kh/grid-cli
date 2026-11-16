package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigDirLinuxDefaultsToDotConfig(t *testing.T) {
	dir, err := configDir("linux", func(k string) string {
		if k == "HOME" {
			return "/home/tester"
		}
		return ""
	})
	require.NoError(t, err)
	require.Equal(t, filepath.Join("/home/tester", ".config", "grid"), dir)
}

func TestConfigDirLinuxHonoursXDGConfigHome(t *testing.T) {
	dir, err := configDir("linux", func(k string) string {
		switch k {
		case "XDG_CONFIG_HOME":
			return "/xdg/conf"
		case "HOME":
			return "/home/tester"
		default:
			return ""
		}
	})
	require.NoError(t, err)
	require.Equal(t, filepath.Join("/xdg/conf", "grid"), dir)
}

func TestConfigDirDarwin(t *testing.T) {
	dir, err := configDir("darwin", func(k string) string {
		if k == "HOME" {
			return "/Users/tester"
		}
		return ""
	})
	require.NoError(t, err)
	require.Equal(t, filepath.Join("/Users/tester", "Library", "Application Support", "grid"), dir)
}

func TestConfigDirWindows(t *testing.T) {
	dir, err := configDir("windows", func(k string) string {
		if k == "AppData" {
			return `C:\Users\tester\AppData\Roaming`
		}
		return ""
	})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(`C:\Users\tester\AppData\Roaming`, "grid"), dir)
}

func TestConfigDirWindowsMissingAppData(t *testing.T) {
	_, err := configDir("windows", emptyEnv)
	require.Error(t, err)
}

func TestConfigDirMissingHome(t *testing.T) {
	_, err := configDir("linux", emptyEnv)
	require.Error(t, err)
	_, err = configDir("darwin", emptyEnv)
	require.Error(t, err)
}

func TestConfigDirOnHostIsAbsolute(t *testing.T) {
	dir, err := ConfigDir()
	require.NoError(t, err)
	require.True(t, filepath.IsAbs(dir), "config dir should be absolute, got %q", dir)
}

func TestCredentialsPathInsideConfigDir(t *testing.T) {
	dir, err := ConfigDir()
	require.NoError(t, err)
	creds, err := CredentialsPath()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "credentials.json"), creds)
}

func TestAttemptsDirInsideConfigDir(t *testing.T) {
	dir, err := ConfigDir()
	require.NoError(t, err)
	attempts, err := AttemptsDir()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "attempts"), attempts)
}

func TestWebURLDefaultAndOverride(t *testing.T) {
	require.Equal(t, DefaultWebURL, WebURL(func(string) string { return "" }))
	require.Equal(t, "https://grid.example", WebURL(func(k string) string {
		if k == EnvWebURL {
			return "https://grid.example"
		}
		return ""
	}))
}

package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func emptyEnv(string) string { return "" }

func TestResolveDefaults(t *testing.T) {
	c := Resolve(Flags{}, emptyEnv)
	require.Equal(t, DefaultAPIURL, c.APIURL)
	require.Equal(t, DefaultTimeout, c.Timeout)
	require.False(t, c.JSON)
	require.False(t, c.NoColor)
	require.False(t, c.Verbose)
	require.Empty(t, c.Dir)
}

func TestEnvOverridesDefault(t *testing.T) {
	getenv := func(k string) string {
		switch k {
		case EnvAPIURL:
			return "http://stub:4000/api/v1"
		case EnvDir:
			return "/tmp/lessons"
		case EnvTimeout:
			return "45s"
		default:
			return ""
		}
	}
	c := Resolve(Flags{Timeout: DefaultTimeout}, getenv)
	require.Equal(t, "http://stub:4000/api/v1", c.APIURL)
	require.Equal(t, "/tmp/lessons", c.Dir)
	require.Equal(t, 45*time.Second, c.Timeout)
}

func TestFlagBeatsEnv(t *testing.T) {
	getenv := func(k string) string {
		switch k {
		case EnvAPIURL:
			return "http://from-env:4000/api/v1"
		case EnvDir:
			return "/tmp/from-env"
		case EnvTimeout:
			return "10s"
		default:
			return ""
		}
	}
	f := Flags{
		APIURL:     "http://from-flag:4000/api/v1",
		APIURLSet:  true,
		Dir:        "/tmp/from-flag",
		DirSet:     true,
		Timeout:    2 * time.Minute,
		TimeoutSet: true,
		JSON:       true,
		Verbose:    true,
	}
	c := Resolve(f, getenv)
	require.Equal(t, "http://from-flag:4000/api/v1", c.APIURL)
	require.Equal(t, "/tmp/from-flag", c.Dir)
	require.Equal(t, 2*time.Minute, c.Timeout)
	require.True(t, c.JSON)
	require.True(t, c.Verbose)
}

func TestEnvNoColor(t *testing.T) {
	getenv := func(k string) string {
		if k == EnvNoColor {
			return "1"
		}
		return ""
	}
	require.True(t, Resolve(Flags{}, getenv).NoColor)
}

func TestStandardNoColorEnv(t *testing.T) {
	getenv := func(k string) string {
		if k == "NO_COLOR" {
			return "1"
		}
		return ""
	}
	require.True(t, Resolve(Flags{}, getenv).NoColor)
}

func TestNoColorFlag(t *testing.T) {
	require.True(t, Resolve(Flags{NoColor: true}, emptyEnv).NoColor)
}

func TestInvalidTimeoutEnvFallsBackToDefault(t *testing.T) {
	getenv := func(k string) string {
		if k == EnvTimeout {
			return "not-a-duration"
		}
		return ""
	}
	c := Resolve(Flags{Timeout: DefaultTimeout}, getenv)
	require.Equal(t, DefaultTimeout, c.Timeout)
}

func TestZeroTimeoutClampedToDefault(t *testing.T) {
	f := Flags{Timeout: 0, TimeoutSet: true}
	require.Equal(t, DefaultTimeout, Resolve(f, emptyEnv).Timeout)
}

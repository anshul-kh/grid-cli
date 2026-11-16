package ui

import (
	"testing"

	"github.com/fatih/color"
	"github.com/stretchr/testify/require"
)

func TestColorEnabled(t *testing.T) {
	tests := []struct {
		name        string
		noColorFlag bool
		getenv      func(string) string
		stdoutIsTTY bool
		want        bool
	}{
		{
			name:        "terminal with no overrides enables colour",
			getenv:      func(string) string { return "" },
			stdoutIsTTY: true,
			want:        true,
		},
		{
			name:        "piped stdout disables colour",
			getenv:      func(string) string { return "" },
			stdoutIsTTY: false,
			want:        false,
		},
		{
			name:        "no-color flag disables colour",
			noColorFlag: true,
			getenv:      func(string) string { return "" },
			stdoutIsTTY: true,
			want:        false,
		},
		{
			name: "GRID_NO_COLOR disables colour",
			getenv: func(k string) string {
				if k == "GRID_NO_COLOR" {
					return "1"
				}
				return ""
			},
			stdoutIsTTY: true,
			want:        false,
		},
		{
			// os.Getenv cannot distinguish set-and-empty from unset, so a
			// non-empty NO_COLOR value is what the runtime check sees.
			name: "NO_COLOR disables colour",
			getenv: func(k string) string {
				if k == "NO_COLOR" {
					return "1"
				}
				return ""
			},
			stdoutIsTTY: true,
			want:        false,
		},
		{
			name: "TERM=dumb disables colour",
			getenv: func(k string) string {
				if k == "TERM" {
					return "dumb"
				}
				return ""
			},
			stdoutIsTTY: true,
			want:        false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, colorEnabled(tt.noColorFlag, tt.getenv, tt.stdoutIsTTY))
		})
	}
}

func TestNewThemeDisabledIsPlain(t *testing.T) {
	theme := NewTheme(false)
	require.False(t, theme.Enabled())
	require.Equal(t, "plain", theme.Bold("plain"))
	require.Equal(t, "plain", theme.Green("plain"))
	require.Equal(t, "plain", theme.Dim("plain"))
}

func TestNewThemeEnabledUsesANSI(t *testing.T) {
	// fatih/color independently disables ANSI when the process output is
	// not a terminal (color.NoColor), which is the behaviour we want in
	// production. Force it on here so the theme's own gate is what's under
	// test.
	prev := color.NoColor
	color.NoColor = false
	t.Cleanup(func() { color.NoColor = prev })

	theme := NewTheme(true)
	require.True(t, theme.Enabled())
	for _, s := range []string{theme.Bold("x"), theme.Green("x"), theme.Red("x"), theme.Dim("x")} {
		require.Contains(t, s, "\x1b[")
	}
}

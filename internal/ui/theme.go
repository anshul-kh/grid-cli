// Package ui renders all human-facing output: colour theming, tables,
// spinners, verdict rendering, and the --json machine output helpers.
//
// This file (theme.go) owns colour detection only: colour is disabled when
// --no-color or GRID_NO_COLOR/NO_COLOR is set, when TERM=dumb, or when
// stdout is not a terminal (TASK.md §3). fatih/color handles the Windows
// console (VT processing) underneath.
package ui

import (
	"os"

	"github.com/fatih/color"
)

// ColorEnabled reports whether ANSI colour should be emitted on stdout.
func ColorEnabled(noColorFlag bool) bool {
	return colorEnabled(noColorFlag, os.Getenv, stdoutIsTerminal())
}

// colorEnabled is the pure, testable core of ColorEnabled.
func colorEnabled(noColorFlag bool, getenv func(string) string, stdoutIsTTY bool) bool {
	if noColorFlag {
		return false
	}
	if getenv("GRID_NO_COLOR") != "" || getenv("NO_COLOR") != "" {
		return false
	}
	if getenv("TERM") == "dumb" {
		return false
	}
	return stdoutIsTTY
}

// stdoutIsTerminal reports whether os.Stdout is attached to a character
// device (terminal). Piped and redirected output is not.
func stdoutIsTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// Theme wraps colour helpers. All rendering goes through the Theme so a
// disabled theme (piped output, NO_COLOR, --no-color) yields plain text and
// `--json | jq` output stays byte-stable.
type Theme struct {
	enabled bool
}

// NewTheme returns a Theme; when enabled is false every helper returns its
// input unchanged.
func NewTheme(enabled bool) *Theme {
	return &Theme{enabled: enabled}
}

// Enabled reports whether the theme emits ANSI sequences.
func (t *Theme) Enabled() bool { return t.enabled }

func (t *Theme) apply(attr color.Attribute, s string) string {
	if !t.enabled {
		return s
	}
	return color.New(attr).Sprint(s)
}

func (t *Theme) Bold(s string) string    { return t.apply(color.Bold, s) }
func (t *Theme) Dim(s string) string     { return t.apply(color.Faint, s) }
func (t *Theme) Green(s string) string   { return t.apply(color.FgGreen, s) }
func (t *Theme) Red(s string) string     { return t.apply(color.FgRed, s) }
func (t *Theme) Yellow(s string) string  { return t.apply(color.FgYellow, s) }
func (t *Theme) Cyan(s string) string    { return t.apply(color.FgCyan, s) }
func (t *Theme) Magenta(s string) string { return t.apply(color.FgMagenta, s) }

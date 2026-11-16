// Package config resolves the effective CLI configuration from, in
// precedence order (highest wins, TASK.md §3): command-line flags →
// environment variables → built-in defaults.
//
// A config-file layer may be added between env and defaults in a later pass;
// Resolve is the single place it would slot in.
package config

import (
	"os"
	"time"

	"github.com/spf13/cobra"
)

const (
	// DefaultAPIURL is the built-in Grid API base URL (TASK.md §6).
	DefaultAPIURL = "http://localhost:4000/api/v1"
	// DefaultTimeout is the default per-request timeout.
	DefaultTimeout = 30 * time.Second
)

// Environment variables consulted during resolution.
const (
	EnvAPIURL  = "GRID_API_URL"
	EnvNoColor = "GRID_NO_COLOR"
	EnvDir     = "GRID_DIR"
	EnvTimeout = "GRID_TIMEOUT"
)

// Config is the effective configuration for the running command.
type Config struct {
	// APIURL is the Grid API base URL.
	APIURL string
	// Dir is the working directory ("" means the current directory).
	Dir string
	// JSON selects machine-readable JSON output on stdout.
	JSON bool
	// NoColor disables ANSI colour output.
	NoColor bool
	// Verbose enables diagnostics on stderr.
	Verbose bool
	// Timeout bounds a single API request.
	Timeout time.Duration
}

// Flags captures the persistent global flags as parsed from the command
// line. The *Set fields record whether the flag was explicitly provided —
// only an explicitly provided flag beats the environment.
type Flags struct {
	JSON    bool
	NoColor bool
	Verbose bool

	APIURL    string
	APIURLSet bool

	Dir    string
	DirSet bool

	Timeout    time.Duration
	TimeoutSet bool
}

// FromFlags resolves the effective Config from the command's parsed
// persistent flags plus the process environment. It must be called after
// flag parsing (e.g. in PersistentPreRunE).
func FromFlags(cmd *cobra.Command) (Config, error) {
	fl := cmd.Flags()
	f := Flags{}
	var err error
	if f.JSON, err = fl.GetBool("json"); err != nil {
		return Config{}, err
	}
	if f.NoColor, err = fl.GetBool("no-color"); err != nil {
		return Config{}, err
	}
	if f.Verbose, err = fl.GetBool("verbose"); err != nil {
		return Config{}, err
	}
	if f.APIURL, err = fl.GetString("api"); err != nil {
		return Config{}, err
	}
	f.APIURLSet = fl.Changed("api")
	if f.Dir, err = fl.GetString("dir"); err != nil {
		return Config{}, err
	}
	f.DirSet = fl.Changed("dir")
	if f.Timeout, err = fl.GetDuration("timeout"); err != nil {
		return Config{}, err
	}
	f.TimeoutSet = fl.Changed("timeout")
	return Resolve(f, os.Getenv), nil
}

// Resolve computes the effective Config. It is pure: getenv injects the
// environment so precedence is unit-testable. Precedence per key, highest
// wins: explicit flag → environment variable → default.
func Resolve(f Flags, getenv func(string) string) Config {
	c := Config{
		JSON:    f.JSON,
		NoColor: f.NoColor,
		Verbose: f.Verbose,
		Dir:     f.Dir,
		Timeout: f.Timeout,
	}

	c.APIURL = DefaultAPIURL
	if v := getenv(EnvAPIURL); v != "" {
		c.APIURL = v
	}
	if f.APIURLSet {
		c.APIURL = f.APIURL
	}

	if !f.DirSet {
		if v := getenv(EnvDir); v != "" {
			c.Dir = v
		}
	}

	// Colour is disabled by --no-color, GRID_NO_COLOR, or NO_COLOR (the
	// standard https://no-color.org convention). TTY/TERM detection lives in
	// internal/ui so it can also account for the output stream.
	c.NoColor = f.NoColor || getenv(EnvNoColor) != "" || getenv("NO_COLOR") != ""

	if !f.TimeoutSet {
		if v := getenv(EnvTimeout); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				c.Timeout = d
			}
		}
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	return c
}

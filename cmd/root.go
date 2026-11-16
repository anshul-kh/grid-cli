// Package cmd implements the grid command surface. This file owns the cobra
// root command, the persistent global flags, config resolution, and the
// top-level error → exit-code mapping (TASK.md §3: 0 success, 1 failure,
// 2 invalid/setup error, 3 auth required, 4 network/server error, 5 usage
// error).
package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/anshul-kh/grid-cli/internal/api"
	"github.com/anshul-kh/grid-cli/internal/buildinfo"
	"github.com/anshul-kh/grid-cli/internal/config"
	"github.com/anshul-kh/grid-cli/internal/ui"
	"github.com/spf13/cobra"
)

// cfg holds the resolved configuration for the currently running command.
// It is populated in PersistentPreRunE from the persistent flags plus the
// environment (internal/config).
var cfg config.Config

// UsageError marks an error caused by the command line itself (unknown
// command, bad flag). Execute prints usage and exits 5.
type UsageError struct {
	err error
}

func (e *UsageError) Error() string { return e.err.Error() }
func (e *UsageError) Unwrap() error { return e.err }

func usageErrorf(format string, a ...any) error {
	return &UsageError{fmt.Errorf(format, a...)}
}

// ExitError carries an explicit TASK.md §3 exit code (3 auth required,
// 4 network/server error, ...) alongside an actionable message. Commands
// return it from RunE; Execute prints the message to stderr and exits with
// the code. Wrapped errors keep their chain so tests can assert codes.
type ExitError struct {
	Code int
	err  error
}

func (e *ExitError) Error() string { return e.err.Error() }
func (e *ExitError) Unwrap() error { return e.err }

func exitError(code int, err error) error {
	return &ExitError{Code: code, err: err}
}

// notEnrolledError prints the server's not_enrolled detail — its message,
// then the enrollUrl on its own line — and returns exit 1 (GRID_SPEC
// §5.2/§5.3/§6: "no auto-enroll", GRID_SYNC_CLAUDE.md 2026-11-13 09:30:
// "map 403 not_enrolled to exit 1, print the enrollUrl from the error
// detail, and do not retry or attempt to enroll on behalf of the user").
// Shared by `grid run` (runFetchError) and `grid submit` (submitError) —
// both hit the identical 403 shape, since enrollment is re-checked on
// every submit, not cached from `grid run`.
func notEnrolledError(err error) error {
	var se *api.StatusError
	if errors.As(err, &se) {
		if se.Message != "" {
			fmt.Fprintln(os.Stderr, se.Message)
		}
		if se.EnrollURL != "" {
			fmt.Fprintln(os.Stderr, se.EnrollURL)
		}
	}
	return exitError(1, fmt.Errorf("not enrolled in this lesson's course — enroll, then try again"))
}

var rootCmd = &cobra.Command{
	Use:   "grid",
	Short: "Grid lesson CLI — measure your workspace, earn your verdict",
	Long: `grid is the companion CLI for Grid lessons.

It scaffolds lesson workspaces, measures checks locally, and submits
evidence to the server for a verdict. The CLI measures; the server judges.`,
	SilenceErrors: true,
	SilenceUsage:  true,
	Version:       buildinfo.Version,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		resolved, err := config.FromFlags(cmd)
		if err != nil {
			return err
		}
		cfg = resolved
		return nil
	},
}

// Config returns the resolved configuration for the running command.
// Subcommands must call it from their Run* (i.e. after flag parsing), not at
// init time.
func Config() config.Config { return cfg }

// Theme returns a colour theme for the running command, honouring --no-color,
// NO_COLOR/GRID_NO_COLOR, TERM=dumb, and non-TTY stdout.
func Theme() *ui.Theme {
	return ui.NewTheme(ui.ColorEnabled(cfg.NoColor))
}

// newAPIClient builds the §5 API client from the resolved config, with an
// optional session key (empty while logging in; from the credential store
// afterwards).
func newAPIClient(sessionKey string) (*api.Client, error) {
	return api.New(cfg.APIURL, api.WithSessionKey(sessionKey), api.WithTimeout(cfg.Timeout))
}

// commandCtx returns the context for a single command invocation, bounded
// by the resolved per-request timeout. It is sized for one API request, not
// for `grid check`/`grid submit`'s local measurement phase — those use
// measurementCtx instead, since a spec's own whole-run budget can run far
// longer than cfg.Timeout's default.
func commandCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), cfg.Timeout)
}

// measurementCtx returns the context for `grid check`/`grid submit`'s local
// measurement phase (runner.Run): cancelled on SIGINT/SIGTERM so a real
// interrupt cleans up services and exits 130, per TASK.md §4 Services /
// GRID_SPEC §4 ("Trap SIGINT/SIGTERM at the top level: clean up services,
// exit 130"). Unlike commandCtx it carries no fixed timeout of its own —
// the spec's own whole-run budget (runner.Run wrapping cs.Timeout) is what
// bounds it.
func measurementCtx() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// interrupted reports whether ctx was cancelled by the signal
// measurementCtx watches for, as opposed to an ordinary deadline the spec's
// own whole-run budget imposed internally (a child context derived from
// ctx, whose expiry never cancels ctx itself).
func interrupted(ctx context.Context) bool {
	return ctx.Err() != nil
}

func init() {
	pf := rootCmd.PersistentFlags()
	pf.Bool("json", false, "emit machine-readable JSON on stdout")
	pf.Bool("no-color", false, "disable colour output")
	pf.Bool("verbose", false, "print verbose diagnostics to stderr")
	pf.String("dir", "", "working directory (default: current directory)")
	pf.String("api", "", "Grid API base URL (overrides GRID_API_URL)")
	pf.Duration("timeout", config.DefaultTimeout, "API request timeout (overrides GRID_TIMEOUT)")

	// Flag parse failures are usage errors: name the flag, show usage, exit 5.
	rootCmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return &UsageError{err}
	})
}

// Execute runs the root command and returns the process exit code — os.Exit
// itself is main's job alone (golang-cli review checklist item 2), so
// Execute stays callable, and testable, without ever terminating the test
// binary. It uses ExecuteC rather than Execute so a usage error reports the
// usage of whichever subcommand actually produced it (e.g. `grid run`'s own
// `Use`/flags) instead of always the root's full command list — the
// executed command is known no matter how deep cobra's Find/Traverse
// resolved it.
func Execute() int {
	cmd, err := rootCmd.ExecuteC()
	if err == nil {
		return 0
	}
	code, showUsage := exitCodeFor(err)
	fmt.Fprintln(os.Stderr, "Error:", err.Error())
	if showUsage {
		// Diagnostics and usage go to stderr; stdout stays clean for --json.
		fmt.Fprint(os.Stderr, cmd.UsageString())
	}
	return code
}

// exitCodeFor maps err to its TASK.md §3 exit code and whether the
// offending command's usage should be shown alongside it — the pure core
// Execute wraps, so the mapping itself is unit-testable without going
// through cobra or os.Exit at all.
func exitCodeFor(err error) (code int, showUsage bool) {
	var usageErr *UsageError
	if errors.As(err, &usageErr) || isCobraUsageError(err) {
		return 5, true
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code, false
	}
	return 1, false
}

// isCobraUsageError recognises usage errors cobra produces before our flag
// error func can wrap them — notably the root-level "unknown command" from
// Find, which a silenced root never decorates.
func isCobraUsageError(err error) bool {
	return strings.HasPrefix(err.Error(), "unknown command")
}

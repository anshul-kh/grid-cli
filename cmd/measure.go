// Package cmd — this file (measure.go) owns the setup shared by `grid
// check` and `grid submit`: loading the on-disk attempt record, parsing and
// validating its check-spec, and building the runner.Runner that measures
// it. Both commands diverge only in what they do with the result (print a
// measurement table vs. sign and submit it), so the loading step is shared
// rather than duplicated (TASK.md §3's missing-attempt.json contract —
// exit 5, "run `grid run <id>` first" — is identical for both).
package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/anshul-kh/grid-cli/internal/config"
	"github.com/anshul-kh/grid-cli/internal/extensions"
	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-cli/internal/runner/checks"
	"github.com/anshul-kh/grid-cli/internal/spec"
	"github.com/anshul-kh/grid-cli/internal/ui"
	"github.com/anshul-kh/grid-cli/internal/workspace"
)

// loadedSpec bundles what `grid check`/`grid submit` need before measuring:
// the on-disk attempt record, its parsed, validated check-spec, and two
// directories that are the same for every mode except none:
//   - dir is the execution root — where checks actually run (buildRunner's
//     workdir joins onto it, submit's Fingerprint measures it). For a
//     workspace.mode: none attempt there is no project directory
//     (GRID_SPEC §4), so this is instead a fresh, empty temp directory,
//     removed by cleanup once the command returns.
//   - bookDir is the bookkeeping root — where .grid/attempt.json,
//     last-run.json, last-verdict.json persist. It is af.DirPath always:
//     the scaffolded workspace for every mode except none, or the CLI's
//     own per-lesson state pseudo-directory (workspace.StateDir) for none.
type loadedSpec struct {
	dir     string
	bookDir string
	af      workspace.AttemptFile
	cs      *spec.CheckSpec
	cleanup func()
}

// loadSpecForMeasurement resolves the open attempt — a local workspace's
// .grid/attempt.json under the --dir global flag/current directory, or (for
// a workspace.mode: none lesson, which never scaffolded one) the CLI's own
// state directory (workspace.FindAttempt) — and reads its check-spec.
func loadSpecForMeasurement() (loadedSpec, error) {
	cwd, err := workingDir()
	if err != nil {
		return loadedSpec{}, err
	}
	af, err := workspace.FindAttempt(cwd)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return loadedSpec{}, noOpenAttemptError()
		}
		return loadedSpec{}, exitError(5, fmt.Errorf("could not read the open attempt: %w", err))
	}

	bookDir := af.DirPath
	root := bookDir
	cleanup := func() {}
	if workspace.ParseMode(af.Mode) == workspace.ModeNone {
		// GRID_SPEC §4: a mode: none lesson never gets a project directory
		// — checks run in a fresh temp cwd, deleted once this command
		// returns, never the state pseudo-directory that only exists to
		// hold the attempt's local bookkeeping.
		tmp, err := os.MkdirTemp("", "grid-none-")
		if err != nil {
			return loadedSpec{}, exitError(1, fmt.Errorf("creating a temporary workspace for a mode: none lesson: %w", err))
		}
		root = tmp
		cleanup = func() { os.RemoveAll(tmp) }
	}

	cs, err := spec.Parse(af.Spec)
	if err != nil {
		cleanup()
		return loadedSpec{}, exitError(4, fmt.Errorf("could not parse the lesson's check spec: %w", err))
	}
	if err := spec.Validate(cs); err != nil {
		cleanup()
		return loadedSpec{}, exitError(4, fmt.Errorf("the lesson's check spec is invalid: %w", err))
	}
	return loadedSpec{dir: root, bookDir: bookDir, af: af, cs: cs, cleanup: cleanup}, nil
}

// noOpenAttemptError is the shared "nothing to act on" failure for
// grid check/grid status/grid submit/grid open when FindAttempt finds no
// local .grid/attempt.json and no workspace.mode: none attempt in the
// CLI's state directory either — there is no open attempt anywhere
// `grid run` could have left one.
func noOpenAttemptError() error {
	webURL := config.WebURL(os.Getenv)
	return exitError(5, fmt.Errorf("no open attempt — enroll at %s/courses, then run `grid run <lessonId>`", webURL))
}

// buildRunner constructs the runner.Runner for a loaded spec. The spec's
// top-level workdir resolves against the workspace directory — spec paths
// use forward slashes, converted with filepath.FromSlash and joined with
// filepath.Join, never hand-concatenated (GRID_SPEC §4 rule 2) — and its
// top-level env is applied to every command the spec declares.
func buildRunner(ls loadedSpec) *runner.Runner {
	root := filepath.Join(ls.dir, filepath.FromSlash(ls.cs.Workdir))
	return runner.New(runner.NewEnv(root, ls.cs.Env))
}

// buildRegistry builds the measurement registry for a loaded spec: the 15
// §4 builtin types, plus the ext.<name>.<action> namespace (§4/A1) wired to
// the extension manager and this attempt's identity when (and only when)
// the spec declares ext checks — lessons without extensions never touch
// the cache directory or the manager at all.
func buildRegistry(ls loadedSpec) (*checks.Registry, error) {
	reg := checks.NewDefaultRegistry()
	hasExt := false
	for _, c := range ls.cs.Checks {
		if _, _, ok := spec.ParseExtType(c.Type); ok {
			hasExt = true
			break
		}
	}
	if !hasExt {
		return reg, nil
	}
	mgr, err := extensions.NewManager()
	if err != nil {
		return nil, exitError(1, err)
	}
	reg.RegisterPrefix(spec.ExtTypePrefix, checks.NewExtFactory(ls.cs, mgr, ls.af.AttemptID, ls.af.Nonce))
	return reg, nil
}

// attachSpinner wires rn's Progress hook to a stderr spinner showing the
// check currently being measured (TASK.md §7: "Spinners while running
// checks, with the current check title. ... Suppressed when not a TTY"),
// shared by `grid check`/`grid submit` since both measure the same way.
// The returned Spinner is already started; the caller defers Stop so it
// clears cleanly on every exit path, including a measurement error.
func attachSpinner(rn *runner.Runner, theme *ui.Theme, initialTitle string) *ui.Spinner {
	sp := ui.NewSpinner(os.Stderr, theme, ui.StderrIsTerminal())
	rn.Progress = func(c spec.Check) {
		title := c.Title
		if title == "" {
			title = c.ID
		}
		sp.SetTitle(title)
	}
	sp.Start(initialTitle)
	return sp
}

// renderBlock captures render's output into a string with its trailing
// newline trimmed, so `grid check`/`grid submit`/`grid status` can hand a
// ui renderer's multi-line output (a measurement table, a verdict banner)
// to a single ui.Flow.Line call rather than re-plumbing that renderer to
// take a Flow itself.
func renderBlock(render func(io.Writer)) string {
	var buf bytes.Buffer
	render(&buf)
	return strings.TrimRight(buf.String(), "\n")
}

// checkTitles maps every declared check's id to its display title, falling
// back to the id when a check has no title (run.go's task-list uses the
// same fallback).
func checkTitles(cs *spec.CheckSpec) map[string]string {
	titles := make(map[string]string, len(cs.Checks))
	for _, c := range cs.Checks {
		title := c.Title
		if title == "" {
			title = c.ID
		}
		titles[c.ID] = title
	}
	return titles
}

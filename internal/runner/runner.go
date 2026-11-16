// Package runner owns the measurement side of grid (GRID_SPEC §4): process
// execution with process-group timeouts, byte-identical output
// normalisation (the cross-repo contract with grid-backend), evidence
// building and capping (§5.4), and the check-orchestration skeleton.
//
// The runner measures; it never judges. A Result's Status is exactly one of
// ok | error | skipped — never pass/fail. Whether a measurement *satisfies*
// a check is the server's call alone (§1 principle 1).
package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/anshul-kh/grid-cli/internal/spec"
	"golang.org/x/sync/errgroup"
)

// Status is a check measurement outcome (§1 / §5.3). Exactly ok | error |
// skipped; there is no pass or fail anywhere in this package.
type Status string

const (
	// StatusOK: the measurement was taken successfully (its content may
	// still be wrong — that is for the server to judge).
	StatusOK Status = "ok"
	// StatusError: the measurement could not be taken (command not found,
	// timeout, unreadable file, cancelled budget).
	StatusError Status = "error"
	// StatusSkipped: a depends_on check did not produce ok (pass 7 wires
	// the skipping; the value exists now so the wire enum is complete).
	StatusSkipped Status = "skipped"
)

// DefaultCheckTimeout is §4's per-check default ("per-check default 30s").
const DefaultCheckTimeout = 30 * time.Second

// Result is one check's measurement outcome, the local shape behind
// §5.3's results[] entry: {checkId, status: ok|error|skipped, durationMs,
// evidence}. Err carries the measurement error when Status is error — it is
// display-only (cmd layer); only CheckID/Status/DurationMS/Evidence go on
// the wire.
type Result struct {
	CheckID    string
	Status     Status
	DurationMS int64
	Evidence   Evidence
	Err        error
	// Ext is non-nil only for an ext.* check (GRID_SPEC §4's extension
	// namespace): the A4 ext-result fields that ride the wire *instead of*
	// evidence (extension, extBinaryHash, telemetry, extSignature). Set by
	// MeasureCheck from the checker's ExtResult — a checker instance that
	// carries its own result, so parallel groups never share state.
	Ext *ExtResult
}

// ExtResult is the A4 ext-result payload for an ext.* check: Telemetry is
// the extension's signed measurement, Extension is "loadtest@0.1.0", and
// ExtBinaryHash/ExtSignature bind it to the official signed binary that
// produced it (GRID_SPEC §5.3 result kinds — ext results omit evidence).
type ExtResult struct {
	Extension     string
	ExtBinaryHash string
	Telemetry     map[string]any
	ExtSignature  string
}

// ExtResultSource is implemented by checkers whose measurement is an ext.*
// result (internal/runner/checks' ext checker). MeasureCheck picks the
// result up after Measure returns; the checker hands out its own per-check
// instance, so the value is not shared across parallel measurements.
type ExtResultSource interface {
	Checker
	ExtResult() *ExtResult
}

// Env is the measurement environment shared by every check in a run: the
// workspace root (spec workdir resolves against it), the spec-level env
// applied to every command, the per-check default timeout, the capture cap,
// and the verbose-warning channel (nil = silent; the cmd layer wires it to
// stderr only under --verbose).
type Env struct {
	// Workdir is the absolute workspace root.
	Workdir string
	// SpecEnv is the spec's top-level env map, applied to every cmd/service
	// on top of the parent environment.
	SpecEnv map[string]string
	// ParentEnv is the CLI process's environment (os.Environ() by default;
	// tests override it to inject or isolate).
	ParentEnv []string
	// DefaultTimeout applies to checks that do not set their own timeout.
	DefaultTimeout time.Duration
	// CaptureCap bounds captured stdout/stderr per stream.
	CaptureCap int
	// Warnf is the --verbose diagnostics channel; nil discards.
	Warnf func(format string, args ...any)
}

// NewEnv builds an Env rooted at workdir (made absolute if relative) with
// the spec's top-level env map.
func NewEnv(workdir string, env map[string]string) *Env {
	if abs, err := filepath.Abs(workdir); err == nil {
		workdir = abs
	}
	timeout := DefaultCheckTimeout
	return &Env{
		Workdir:        workdir,
		SpecEnv:        env,
		ParentEnv:      os.Environ(),
		DefaultTimeout: timeout,
		CaptureCap:     DefaultCaptureCap,
	}
}

// warnf emits a verbose diagnostic if a channel is wired.
func (e *Env) warnf(format string, args ...any) {
	if e.Warnf != nil {
		e.Warnf(format, args...)
	}
}

// Dir resolves a spec path against the workspace root: spec paths use
// forward slashes (§4 rule 2), so they are converted with filepath.FromSlash
// and joined — never concatenated with a separator by hand.
func (e *Env) Dir(cwd string) string {
	if cwd == "" {
		return e.Workdir
	}
	return filepath.Join(e.Workdir, filepath.FromSlash(cwd))
}

// Environ builds a command's environment: parent + spec env + the check's
// own env, later entries winning (check over spec over parent).
func (e *Env) Environ(extra map[string]string) []string {
	env := make([]string, 0, len(e.ParentEnv)+len(e.SpecEnv)+len(extra))
	env = append(env, e.ParentEnv...)
	for k, v := range e.SpecEnv {
		env = append(env, k+"="+v)
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

// Checker performs a single measurement (TASK.md §4): it returns evidence
// and an error only when the measurement itself could not be taken — a
// "wrong answer" is NOT an error, it is evidence for the server to judge.
// The identical interface is declared in internal/runner/checks (as an
// alias) so the registry can reference it without an import cycle.
type Checker interface {
	Measure(ctx context.Context, c spec.Check, env *Env) (Evidence, error)
}

// CheckLookup resolves a check type string to its Checker; checks.Registry
// satisfies it structurally.
type CheckLookup interface {
	Lookup(typeName string) (Checker, error)
}

// Runner orchestrates a spec's measurements. Pass 4 provides the
// sequential skeleton with whole-run budget plumbing; the full orchestration
// (setup, services, depends_on skipping, parallel_safe grouping, signal
// handling) lands in pass 7 on these same types.
type Runner struct {
	Env *Env
	// Progress, when set, is called just before each declared check begins
	// measuring (never for setup[] steps, which have no title to show) —
	// the cmd layer wires this to a stderr spinner (internal/ui/spinner.go,
	// pass 9: TASK.md §7 "Spinners while running checks, with the current
	// check title"). nil, the default, does nothing extra. A parallel_safe
	// group calls it concurrently from multiple goroutines, one per check
	// about to start, so Progress itself must be safe for that.
	Progress func(c spec.Check)
}

// reportProgress calls Progress when set; a no-op otherwise.
func (r *Runner) reportProgress(c spec.Check) {
	if r.Progress != nil {
		r.Progress(c)
	}
}

// New returns a Runner measuring inside env.
func New(env *Env) *Runner { return &Runner{Env: env} }

// MeasureCheck measures one check with the §4 time-budget plumbing:
//
//   - the check's own timeout, defaulting to DefaultCheckTimeout (30s)
//   - clipped to the whole-run budget remaining in ctx, so a run-level
//     deadline expires mid-check and is reported as an error measurement —
//     a timeout is status "error", never a verdict (§4 rule 6)
//   - an unknown check type is an error measurement whose message names
//     the CLI version and suggests `grid upgrade`.
//
// The result is a named return so the deferred duration assignment lands
// on the value the caller receives (an unnamed return copies before the
// defer runs — the 0ms-duration bug the live ext probe exposed).
func (r *Runner) MeasureCheck(ctx context.Context, c spec.Check, reg CheckLookup) (res Result) {
	start := time.Now()
	res = Result{CheckID: c.ID, Status: StatusError}
	defer func() { res.DurationMS = time.Since(start).Milliseconds() }()

	timeout := c.Timeout.D()
	if timeout <= 0 {
		timeout = r.Env.DefaultTimeout
	}
	if dl, ok := ctx.Deadline(); ok {
		if remain := time.Until(dl); remain < timeout {
			timeout = remain
		}
	}
	if ctx.Err() != nil || timeout <= 0 {
		res.Err = fmt.Errorf("run budget exhausted before %q could be measured: %w", c.ID, context.DeadlineExceeded)
		return res
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	checker, err := reg.Lookup(c.Type)
	if err != nil {
		res.Err = err
		return res
	}

	ev, err := checker.Measure(cctx, c, r.Env)
	if ev != nil {
		res.Evidence = ev
	}
	if src, ok := checker.(ExtResultSource); ok {
		res.Ext = src.ExtResult()
	}
	if err != nil {
		res.Err = err
		return res
	}
	res.Status = StatusOK
	return res
}

// maxParallelChecks bounds a parallel_safe group's concurrency to
// min(4, NumCPU) (TASK.md §4 Concurrency).
func maxParallelChecks() int {
	if n := runtime.NumCPU(); n < 4 {
		return n
	}
	return 4
}

// RunChecks measures the spec's checks in order, honouring depends_on and
// parallel_safe (TASK.md §4 Concurrency):
//
//   - Checks run sequentially by default — order matters for teaching, and
//     parallel checks would fight over ports and files.
//   - Consecutive checks marked parallel_safe: true are grouped and run
//     concurrently, bounded to maxParallelChecks().
//   - A check whose depends_on names a check that did not produce "ok" is
//     reported "skipped" without being measured at all. The dependency must
//     already have a known result — one earlier in sequence, or in an
//     earlier parallel group — by the time this check is reached; a
//     depends_on naming another member of the *same* parallel group (whose
//     result isn't decided yet, by construction, since they run
//     concurrently) is treated the same as a dependency that didn't produce
//     "ok": skipped, not raced.
//
// The whole-run timeout budget (cs.Timeout) is enforced by ctx cancellation
// in Run, one layer up: budget exhaustion surfaces here as ordinary
// per-check timeouts, which MeasureCheck already reports as "error", never
// "skipped" or a verdict (§4 rule 6).
func (r *Runner) RunChecks(ctx context.Context, checks []spec.Check, reg CheckLookup) []Result {
	results := make([]Result, 0, len(checks))
	known := make(map[string]Status, len(checks))

	for i := 0; i < len(checks); {
		if !checks[i].ParallelSafe {
			c := checks[i]
			res := r.measureOrSkip(ctx, c, reg, known)
			results = append(results, res)
			known[c.ID] = res.Status
			i++
			continue
		}
		j := i
		for j < len(checks) && checks[j].ParallelSafe {
			j++
		}
		group := checks[i:j]
		groupResults := r.runParallelGroup(ctx, group, reg, known)
		for k, res := range groupResults {
			results = append(results, res)
			known[group[k].ID] = res.Status
		}
		i = j
	}
	return results
}

// measurable reports whether c's declared dependencies have all already
// produced StatusOK in known — the gate RunChecks applies before spending a
// measurement on a check at all.
func measurable(c spec.Check, known map[string]Status) bool {
	for _, dep := range c.DependsOn {
		if known[dep] != StatusOK {
			return false
		}
	}
	return true
}

// measureOrSkip measures c, or reports it "skipped" without measuring it at
// all when a depends_on target did not produce "ok".
func (r *Runner) measureOrSkip(ctx context.Context, c spec.Check, reg CheckLookup, known map[string]Status) Result {
	if !measurable(c, known) {
		return Result{CheckID: c.ID, Status: StatusSkipped}
	}
	r.reportProgress(c)
	return r.MeasureCheck(ctx, c, reg)
}

// runParallelGroup measures a group of consecutive parallel_safe checks
// concurrently, bounded to maxParallelChecks(). known is the snapshot of
// results from checks strictly before this group — the only dependencies
// that can be honoured without racing the group's own members.
func (r *Runner) runParallelGroup(ctx context.Context, group []spec.Check, reg CheckLookup, known map[string]Status) []Result {
	results := make([]Result, len(group))
	var g errgroup.Group
	g.SetLimit(maxParallelChecks())
	for idx, c := range group {
		if !measurable(c, known) {
			results[idx] = Result{CheckID: c.ID, Status: StatusSkipped}
			continue
		}
		idx, c := idx, c
		g.Go(func() error {
			r.reportProgress(c)
			// Every measurement, including its own failure, is reported as
			// a Result — never as a Go error — so one check's failure never
			// cancels its siblings via errgroup's shared context.
			results[idx] = r.MeasureCheck(ctx, c, reg)
			return nil
		})
	}
	_ = g.Wait()
	return results
}

// ErrSetupFailed marks a run where a setup[] step could not be executed, or
// a services[] entry failed to start or become ready — the whole submission
// is a setup error (verdict INVALID per GRID_SPEC §4), never a judged check
// failure. It is never derived from a setup step's exit code: expect_exit is
// judge-only and stripped from the public spec the CLI receives (§5.2), so
// the CLI has no expected value to compare against. The only locally
// detectable failure is the measurement itself failing — the step's command
// could not even be run (not found, timed out, cancelled) — which is
// exactly the ok/error distinction every other measurement in this package
// uses (§1 principle 1).
var ErrSetupFailed = errors.New("setup failed")

// setupResultID is the §5.3 synthetic checkId convention for reporting a
// setup[] step's outcome: there is no dedicated wire field for it (a
// SetupStep carries no id), so it rides results[] under this id, 0-based in
// declaration order.
func setupResultID(index int) string {
	return fmt.Sprintf("__setup__#%d", index)
}

// Outcome is the full measured result of one spec run: setup, services, and
// checks. SetupErr non-nil means the run is a setup error — CheckResults is
// then every declared check reported "skipped" without being measured, and
// SetupResults carries the setup steps actually attempted (ok, until the one
// that errored).
type Outcome struct {
	SetupResults []Result
	SetupErr     error
	CheckResults []Result
}

// Run orchestrates one full spec run (GRID_SPEC §4 / TASK.md §4): setup[]
// first, then services[] (readiness-probed, always stopped in reverse order
// on the way out — including on panic or ctx cancellation, via a deferred
// StopServices), then RunChecks. The whole-run budget (cs.Timeout) wraps ctx
// for every stage; per-check timeouts inside RunChecks clip to whatever of
// that budget remains (MeasureCheck). The caller measures only — Run never
// decides a verdict; it only decides whether there is anything meaningful to
// submit measurements for at all.
func (r *Runner) Run(ctx context.Context, cs *spec.CheckSpec, reg CheckLookup) Outcome {
	if d := cs.Timeout.D(); d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}

	setupResults, setupErr := r.runSetup(ctx, cs.Setup)
	if setupErr == nil && len(cs.Services) > 0 {
		services, startErr := StartServices(ctx, r.Env, cs.Services)
		if startErr != nil {
			setupErr = fmt.Errorf("%w: %s", ErrSetupFailed, startErr)
		} else {
			defer StopServices(services)
		}
	}
	if setupErr != nil {
		return Outcome{
			SetupResults: setupResults,
			SetupErr:     setupErr,
			CheckResults: skippedResults(cs.Checks),
		}
	}
	return Outcome{
		SetupResults: setupResults,
		CheckResults: r.RunChecks(ctx, cs.Checks, reg),
	}
}

// skippedResults reports every check "skipped" without measuring any of
// them — Run's response to a setup error: the environment was never ready,
// so nothing here reflects it either way.
func skippedResults(checks []spec.Check) []Result {
	out := make([]Result, len(checks))
	for i, c := range checks {
		out[i] = Result{CheckID: c.ID, Status: StatusSkipped}
	}
	return out
}

// runSetup runs the spec's setup[] steps in order, in the workspace root,
// reporting each one under its synthetic __setup__#<index> id with
// cmd.exit_code-shaped evidence ({exitCode, stdout, stderr, durationMs} —
// §5.3 addendum). A step's own exit code never decides anything here — the
// server compares it against expect_exit, which the CLI's public spec does
// not even carry (see ErrSetupFailed). The first step whose command could
// not be run at all (status "error") stops the sequence; every step after
// it is reported "skipped" rather than attempted against an environment a
// prior step already failed to bring up.
func (r *Runner) runSetup(ctx context.Context, steps []spec.SetupStep) ([]Result, error) {
	if len(steps) == 0 {
		return nil, nil
	}
	results := make([]Result, 0, len(steps))
	failed := false
	for i, step := range steps {
		id := setupResultID(i)
		if failed {
			results = append(results, Result{CheckID: id, Status: StatusSkipped})
			continue
		}

		start := time.Now()
		res, runErr := r.Env.Run(ctx, CmdSpec{Run: step.Run, Dir: r.Env.Workdir, Timeout: r.Env.DefaultTimeout})
		durationMS := time.Since(start).Milliseconds()

		if res == nil {
			// The command could not even be described (empty argv) — never
			// attempted at all.
			results = append(results, Result{CheckID: id, Status: StatusError, DurationMS: durationMS, Err: runErr})
			failed = true
			continue
		}
		ev := BuildEvidence(map[string]any{
			"exitCode":   res.ExitCode,
			"stdout":     string(NormalizeOutput(res.Stdout)),
			"stderr":     string(NormalizeOutput(res.Stderr)),
			"durationMs": res.DurationMS,
		})
		if res.StdoutTruncated || res.StderrTruncated {
			ev["truncated"] = true
		}
		var exitErr *exec.ExitError
		if runErr != nil && !errors.As(runErr, &exitErr) {
			// Timeout, cancellation, or the command never started (not
			// found, permission denied) — the measurement itself failed. A
			// plain nonzero exit (*exec.ExitError) is excluded here — the
			// command ran and reported evidence; mirrors checks/cmd.go's
			// cmdChecker, which treats that case as a measurement, not an
			// error (§1 principle 1).
			results = append(results, Result{CheckID: id, Status: StatusError, DurationMS: durationMS, Evidence: ev, Err: runErr})
			failed = true
			continue
		}
		// A nonzero exit (exitErr != nil) is a successful measurement: the
		// step ran and reported its exit code as evidence. expect_exit is
		// judge-only and never in the CLI's public spec (spec.SetupStep's
		// doc comment), so the CLI has no local basis to call this a
		// failure — only the server compares exitCode against expect_exit.
		results = append(results, Result{CheckID: id, Status: StatusOK, DurationMS: durationMS, Evidence: ev})
	}
	if failed {
		return results, fmt.Errorf("%w: a setup step could not be executed", ErrSetupFailed)
	}
	return results, nil
}

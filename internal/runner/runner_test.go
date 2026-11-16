package runner

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anshul-kh/grid-cli/internal/spec"
)

// checkerFunc adapts a func to the Checker interface for tests, mirroring
// internal/runner/checks' identical test helper.
type checkerFunc func(ctx context.Context, c spec.Check, env *Env) (Evidence, error)

func (f checkerFunc) Measure(ctx context.Context, c spec.Check, env *Env) (Evidence, error) {
	return f(ctx, c, env)
}

// fakeRegistry resolves every check by its Type field to a fixed Checker,
// so a test can plug in arbitrary measurement behaviour without a real
// checks.Registry (which would import this package — an import cycle).
type fakeRegistry map[string]Checker

func (r fakeRegistry) Lookup(typeName string) (Checker, error) {
	c, ok := r[typeName]
	if !ok {
		return nil, fmt.Errorf("fakeRegistry: no checker registered for %q", typeName)
	}
	return c, nil
}

// okChecker always measures successfully, recording which check IDs it was
// actually invoked for — the thing depends_on/parallel_safe tests care
// about: was a given check ever measured at all.
type okChecker struct {
	mu      sync.Mutex
	invoked []string
	delay   time.Duration
}

func (c *okChecker) Measure(ctx context.Context, chk spec.Check, env *Env) (Evidence, error) {
	c.mu.Lock()
	c.invoked = append(c.invoked, chk.ID)
	c.mu.Unlock()
	if c.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.delay):
		}
	}
	return Evidence{"ok": true}, nil
}

func (c *okChecker) invokedIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.invoked...)
}

// errChecker always fails the measurement (status error).
type errChecker struct{}

func (errChecker) Measure(context.Context, spec.Check, *Env) (Evidence, error) {
	return nil, errors.New("measurement could not be taken")
}

func newTestRunner(t *testing.T) *Runner {
	t.Helper()
	return New(NewEnv(t.TempDir(), nil))
}

// nonZeroExitArgv is a portable "ran fine, exited nonzero" command — the
// same runtime.GOOS switch exec.go's shellArgs already uses, kept to a
// single file rather than split unix/windows test files since it is pure
// argv selection, no OS-specific syscalls.
func nonZeroExitArgv() []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/c", "exit 3"}
	}
	return []string{"sh", "-c", "exit 3"}
}

// TestRunChecksDependsOnSkipsWithoutMeasuring proves a check whose
// depends_on target did not produce "ok" is reported "skipped" — and, the
// stronger claim the plan asks for, never measured at all.
func TestRunChecksDependsOnSkipsWithoutMeasuring(t *testing.T) {
	r := newTestRunner(t)
	dependent := &okChecker{}
	reg := fakeRegistry{"fails": errChecker{}, "ok": dependent}

	checks := []spec.Check{
		{ID: "a", Type: "fails"},
		{ID: "b", Type: "ok", DependsOn: []string{"a"}},
	}
	results := r.RunChecks(context.Background(), checks, reg)
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if results[0].Status != StatusError {
		t.Errorf("a.Status = %v, want StatusError", results[0].Status)
	}
	if results[1].Status != StatusSkipped {
		t.Errorf("b.Status = %v, want StatusSkipped", results[1].Status)
	}
	if ids := dependent.invokedIDs(); len(ids) != 0 {
		t.Errorf("dependent checker was invoked (%v) — a skipped check must never be measured", ids)
	}
}

// TestRunChecksDependsOnRunsWhenDependencyOK is the positive case: a
// dependency that produced "ok" lets the dependent check run normally.
func TestRunChecksDependsOnRunsWhenDependencyOK(t *testing.T) {
	r := newTestRunner(t)
	dependent := &okChecker{}
	reg := fakeRegistry{"ok": dependent}

	checks := []spec.Check{
		{ID: "a", Type: "ok"},
		{ID: "b", Type: "ok", DependsOn: []string{"a"}},
	}
	results := r.RunChecks(context.Background(), checks, reg)
	if results[0].Status != StatusOK || results[1].Status != StatusOK {
		t.Fatalf("statuses = %v, %v, want both StatusOK", results[0].Status, results[1].Status)
	}
	if ids := dependent.invokedIDs(); len(ids) != 2 {
		t.Errorf("invoked = %v, want both a and b measured", ids)
	}
}

// TestRunChecksReportsProgressBeforeEachMeasuredCheck proves Progress (the
// hook the cmd layer's spinner, pass 9, attaches to) is called once for
// every check actually measured, in order, naming it by its spec.Check —
// and never for a check RunChecks skips without measuring (depends_on).
func TestRunChecksReportsProgressBeforeEachMeasuredCheck(t *testing.T) {
	r := newTestRunner(t)
	reg := fakeRegistry{"ok": &okChecker{}, "fails": errChecker{}}

	var mu sync.Mutex
	var reported []string
	r.Progress = func(c spec.Check) {
		mu.Lock()
		reported = append(reported, c.ID)
		mu.Unlock()
	}

	checks := []spec.Check{
		{ID: "a", Type: "fails"},
		{ID: "b", Type: "ok", DependsOn: []string{"a"}}, // skipped: never reported
		{ID: "c", Type: "ok"},
	}
	r.RunChecks(context.Background(), checks, reg)

	if want := []string{"a", "c"}; !slices.Equal(reported, want) {
		t.Errorf("Progress reported = %v, want %v", reported, want)
	}
}

// TestRunChecksReportsProgressConcurrentlyForAParallelGroup proves Progress
// is called for every member of a parallel_safe group too — from whichever
// goroutine is about to measure it, which must survive -race since a
// spinner's SetTitle is exactly what is on the other end of this hook.
func TestRunChecksReportsProgressConcurrentlyForAParallelGroup(t *testing.T) {
	r := newTestRunner(t)
	reg := fakeRegistry{"ok": &okChecker{}}

	var mu sync.Mutex
	reported := map[string]bool{}
	r.Progress = func(c spec.Check) {
		mu.Lock()
		reported[c.ID] = true
		mu.Unlock()
	}

	checks := []spec.Check{
		{ID: "a", Type: "ok", ParallelSafe: true},
		{ID: "b", Type: "ok", ParallelSafe: true},
		{ID: "c", Type: "ok", ParallelSafe: true},
	}
	r.RunChecks(context.Background(), checks, reg)

	for _, id := range []string{"a", "b", "c"} {
		if !reported[id] {
			t.Errorf("Progress was never called for check %q", id)
		}
	}
}

// TestRunChecksSequentialPreservesOrder proves checks without parallel_safe
// run strictly in order, not by whichever finishes first (a slow earlier
// check must not let a fast later one jump ahead).
func TestRunChecksSequentialPreservesOrder(t *testing.T) {
	r := newTestRunner(t)
	var mu sync.Mutex
	var order []string
	reg := fakeRegistry{"slow": checkerFunc(func(ctx context.Context, c spec.Check, env *Env) (Evidence, error) {
		time.Sleep(30 * time.Millisecond)
		mu.Lock()
		order = append(order, c.ID)
		mu.Unlock()
		return Evidence{}, nil
	}), "fast": checkerFunc(func(ctx context.Context, c spec.Check, env *Env) (Evidence, error) {
		mu.Lock()
		order = append(order, c.ID)
		mu.Unlock()
		return Evidence{}, nil
	})}

	checks := []spec.Check{
		{ID: "a", Type: "slow"},
		{ID: "b", Type: "fast"},
		{ID: "c", Type: "fast"},
	}
	r.RunChecks(context.Background(), checks, reg)
	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	want := []string{"a", "b", "c"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("completion order = %v, want %v (sequential, not completion-order)", got, want)
	}
}

// TestRunChecksParallelSafeBoundedConcurrency proves consecutive
// parallel_safe checks run concurrently, bounded to maxParallelChecks().
func TestRunChecksParallelSafeBoundedConcurrency(t *testing.T) {
	r := newTestRunner(t)
	limit := maxParallelChecks()

	var current, peak int64
	reg := fakeRegistry{"slow": checkerFunc(func(ctx context.Context, c spec.Check, env *Env) (Evidence, error) {
		n := atomic.AddInt64(&current, 1)
		for {
			p := atomic.LoadInt64(&peak)
			if n <= p || atomic.CompareAndSwapInt64(&peak, p, n) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		atomic.AddInt64(&current, -1)
		return Evidence{}, nil
	})}

	const n = 10
	checks := make([]spec.Check, n)
	for i := range checks {
		checks[i] = spec.Check{ID: fmt.Sprintf("c%d", i), Type: "slow", ParallelSafe: true}
	}
	results := r.RunChecks(context.Background(), checks, reg)
	for _, res := range results {
		if res.Status != StatusOK {
			t.Errorf("%s: status = %v, want StatusOK", res.CheckID, res.Status)
		}
	}
	got := atomic.LoadInt64(&peak)
	if got > int64(limit) {
		t.Errorf("peak concurrency = %d, want <= %d (min(4, NumCPU)=%d)", got, limit, limit)
	}
	if limit > 1 && got <= 1 {
		t.Errorf("peak concurrency = %d, want > 1 — parallel_safe checks did not actually run concurrently (NumCPU=%d)", got, runtime.NumCPU())
	}
}

// TestRunChecksParallelGroupDependsOnSameGroupIsSkipped documents and pins
// the deliberate choice for a depends_on target inside the *same*
// parallel_safe group (whose result isn't decided yet, by construction,
// since the group runs concurrently): treated as not-yet-"ok", so the
// dependent is skipped rather than raced.
func TestRunChecksParallelGroupDependsOnSameGroupIsSkipped(t *testing.T) {
	r := newTestRunner(t)
	dependent := &okChecker{}
	reg := fakeRegistry{"ok": dependent}

	checks := []spec.Check{
		{ID: "a", Type: "ok", ParallelSafe: true},
		{ID: "b", Type: "ok", ParallelSafe: true, DependsOn: []string{"a"}},
	}
	results := r.RunChecks(context.Background(), checks, reg)
	if results[0].Status != StatusOK {
		t.Errorf("a.Status = %v, want StatusOK", results[0].Status)
	}
	if results[1].Status != StatusSkipped {
		t.Errorf("b.Status = %v, want StatusSkipped (same-group dependency is never raced)", results[1].Status)
	}
}

// TestRunWholeRunBudgetTurnsRemainingChecksIntoError pins §4 rule 6: budget
// exhaustion is reported as "error", never "skipped" and never a verdict.
func TestRunWholeRunBudgetTurnsRemainingChecksIntoError(t *testing.T) {
	r := newTestRunner(t)
	reg := fakeRegistry{"slow": checkerFunc(func(ctx context.Context, c spec.Check, env *Env) (Evidence, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
			return Evidence{}, nil
		}
	})}
	cs := &spec.CheckSpec{
		Version: 1,
		Timeout: spec.Duration(60 * time.Millisecond),
		Checks: []spec.Check{
			{ID: "a", Type: "slow"},
			{ID: "b", Type: "slow"},
		},
	}
	outcome := r.Run(context.Background(), cs, reg)
	if outcome.SetupErr != nil {
		t.Fatalf("SetupErr = %v, want nil", outcome.SetupErr)
	}
	for _, res := range outcome.CheckResults {
		if res.Status != StatusError {
			t.Errorf("%s: status = %v, want StatusError (budget exhaustion), never skipped/ok", res.CheckID, res.Status)
		}
	}
}

// TestRunSetupFailureSkipsEveryCheckWithoutMeasuring proves a setup step
// whose command cannot even be executed makes the whole run a setup error:
// ErrSetupFailed, and every declared check reported "skipped" without ever
// being measured.
func TestRunSetupFailureSkipsEveryCheckWithoutMeasuring(t *testing.T) {
	r := newTestRunner(t)
	checker := &okChecker{}
	reg := fakeRegistry{"ok": checker}

	cs := &spec.CheckSpec{
		Version: 1,
		Setup:   []spec.SetupStep{{Run: []string{"this-command-does-not-exist-grid-test-xyz"}}},
		Checks: []spec.Check{
			{ID: "a", Type: "ok"},
			{ID: "b", Type: "ok"},
		},
	}
	outcome := r.Run(context.Background(), cs, reg)
	if !errors.Is(outcome.SetupErr, ErrSetupFailed) {
		t.Fatalf("SetupErr = %v, want ErrSetupFailed", outcome.SetupErr)
	}
	if len(outcome.SetupResults) != 1 || outcome.SetupResults[0].Status != StatusError {
		t.Fatalf("SetupResults = %+v, want one StatusError entry", outcome.SetupResults)
	}
	if outcome.SetupResults[0].CheckID != "__setup__#0" {
		t.Errorf("setup result id = %q, want __setup__#0", outcome.SetupResults[0].CheckID)
	}
	if len(outcome.CheckResults) != 2 {
		t.Fatalf("len(CheckResults) = %d, want 2", len(outcome.CheckResults))
	}
	for _, res := range outcome.CheckResults {
		if res.Status != StatusSkipped {
			t.Errorf("%s: status = %v, want StatusSkipped", res.CheckID, res.Status)
		}
	}
	if ids := checker.invokedIDs(); len(ids) != 0 {
		t.Errorf("checks were invoked (%v) despite a setup failure", ids)
	}
}

// TestRunSetupStepNonZeroExitIsNotAFailure is the measure-don't-judge
// nuance: expect_exit is judge-only and never in the CLI's public spec
// (internal/spec.SetupStep's own doc comment), so a setup step that RAN
// successfully but exited nonzero is status "ok" — evidence, not a local
// failure — and the run proceeds to measure checks normally.
func TestRunSetupStepNonZeroExitIsNotAFailure(t *testing.T) {
	r := newTestRunner(t)
	checker := &okChecker{}
	reg := fakeRegistry{"ok": checker}

	cs := &spec.CheckSpec{
		Version: 1,
		Setup:   []spec.SetupStep{{Run: nonZeroExitArgv()}},
		Checks:  []spec.Check{{ID: "a", Type: "ok"}},
	}
	outcome := r.Run(context.Background(), cs, reg)
	if outcome.SetupErr != nil {
		t.Fatalf("SetupErr = %v, want nil (a ran-but-nonzero-exit step is not a setup failure)", outcome.SetupErr)
	}
	if len(outcome.SetupResults) != 1 || outcome.SetupResults[0].Status != StatusOK {
		t.Fatalf("SetupResults = %+v, want one StatusOK entry", outcome.SetupResults)
	}
	if ec, ok := outcome.SetupResults[0].Evidence["exitCode"]; !ok || ec == 0 {
		t.Errorf("evidence exitCode = %v, want a nonzero exit code reported as evidence", ec)
	}
	if len(outcome.CheckResults) != 1 || outcome.CheckResults[0].Status != StatusOK {
		t.Errorf("CheckResults = %+v, want the check measured normally", outcome.CheckResults)
	}
	if ids := checker.invokedIDs(); len(ids) != 1 {
		t.Errorf("invoked = %v, want the check measured exactly once", ids)
	}
}

// TestMeasureCheckReportsRealDuration pins the named-return fix: the
// deferred duration assignment must reach the caller — a slow measurement
// cannot report durationMs 0 on the wire (exposed by the Pass 6 ext probe).
func TestMeasureCheckReportsRealDuration(t *testing.T) {
	env := NewEnv(t.TempDir(), nil)
	slow := checkerFunc(func(ctx context.Context, c spec.Check, env *Env) (Evidence, error) {
		time.Sleep(50 * time.Millisecond)
		return nil, nil
	})
	res := New(env).MeasureCheck(context.Background(), spec.Check{ID: "x", Type: "slow"}, fakeRegistry{"slow": slow})
	if res.Status != StatusOK {
		t.Fatalf("status = %s, want ok", res.Status)
	}
	if res.DurationMS < 40 {
		t.Errorf("durationMs = %d, want ~50 (the deferred assignment must land on the returned value)", res.DurationMS)
	}
}

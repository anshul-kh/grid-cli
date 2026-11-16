package checks

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-cli/internal/spec"
)

// shCheck builds a spec.Check that runs a small script (or the Windows
// equivalent), the simplest portable way to control exit code / stdout /
// stderr from a test without depending on any one external binary.
func shCheck(typeName, script string) spec.Check {
	run := []string{"sh", "-c", script}
	if runtime.GOOS == "windows" {
		run = []string{"cmd", "/C", script}
	}
	return spec.Check{ID: "c", Type: typeName, Run: run}
}

// scriptFor returns the unix script on POSIX hosts and the cmd.exe script on
// Windows, so fixtures stay readable instead of branching inline.
func scriptFor(unix, windows string) string {
	if runtime.GOOS == "windows" {
		return windows
	}
	return unix
}

// TestCmdExitCodeReportsNonzeroAsEvidence proves §1 principle 1 for every
// cmd.* type: a command that exits nonzero is itself the measurement, not
// a failed one — Measure returns the evidence and a nil error, exitCode
// set to the real code.
func TestCmdExitCodeReportsNonzeroAsEvidence(t *testing.T) {
	env := runner.NewEnv(t.TempDir(), nil)
	for _, typeName := range []string{
		"cmd.exit_code", "cmd.stdout_matches", "cmd.stdout_equals", "cmd.stderr_matches",
	} {
		t.Run(typeName, func(t *testing.T) {
			ev, err := (cmdChecker{}).Measure(context.Background(), shCheck(typeName, "exit 7"), env)
			if err != nil {
				t.Fatalf("Measure: unexpected error for a plain nonzero exit: %v", err)
			}
			if ev["exitCode"] != 7 {
				t.Errorf("evidence[exitCode] = %v, want 7", ev["exitCode"])
			}
		})
	}
}

// TestCmdCapturesNormalisedStdoutStderr proves the evidence shape
// { exitCode, stdout, stderr, durationMs } and that stdout/stderr are
// normalised (CRLF folded, trailing blank lines trimmed) before upload.
func TestCmdCapturesNormalisedStdoutStderr(t *testing.T) {
	env := runner.NewEnv(t.TempDir(), nil)
	script := scriptFor(
		"printf 'out line\\r\\n\\r\\n' ; printf 'err line\\n' 1>&2",
		// cmd.exe built-ins: CRLF comes for free, `echo.` emits the trailing
		// blank line the normaliser must trim.
		"echo out line& echo.& echo err line 1>&2",
	)
	ev, err := (cmdChecker{}).Measure(context.Background(), shCheck("cmd.exit_code", script), env)
	if err != nil {
		t.Fatalf("Measure: unexpected error: %v", err)
	}
	if ev["stdout"] != "out line" {
		t.Errorf("evidence[stdout] = %q, want %q (CRLF folded, trailing blank line trimmed)", ev["stdout"], "out line")
	}
	if ev["stderr"] != "err line" {
		t.Errorf("evidence[stderr] = %q, want %q", ev["stderr"], "err line")
	}
	if ev["exitCode"] != 0 {
		t.Errorf("evidence[exitCode] = %v, want 0", ev["exitCode"])
	}
	if _, ok := ev["durationMs"]; !ok {
		t.Error("evidence is missing durationMs")
	}
}

// TestCmdTimeoutIsErrorNotFail proves §4 cross-platform rule 6: a command
// that outlives its timeout reports status error (a non-nil Measure error),
// never a fabricated exit code pretending to be a measurement.
func TestCmdTimeoutIsErrorNotFail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep-based timeout fixture is unix-assertive (plan Assumption 11)")
	}
	env := runner.NewEnv(t.TempDir(), nil)
	c := shCheck("cmd.exit_code", "sleep 5")
	c.Timeout = spec.Duration(100 * time.Millisecond)

	_, err := (cmdChecker{}).Measure(context.Background(), c, env)
	if err == nil {
		t.Fatal("Measure on a command that outlives its timeout: want error, got nil")
	}
}

// TestCmdCommandNotFoundIsError proves a command that never starts (not on
// PATH) is a measurement failure, not evidence of exitCode -1.
func TestCmdCommandNotFoundIsError(t *testing.T) {
	env := runner.NewEnv(t.TempDir(), nil)
	c := spec.Check{ID: "c", Type: "cmd.exit_code", Run: []string{"grid-cli-definitely-does-not-exist-xyz"}}
	if _, err := (cmdChecker{}).Measure(context.Background(), c, env); err == nil {
		t.Fatal("Measure on a nonexistent command: want error, got nil")
	}
}

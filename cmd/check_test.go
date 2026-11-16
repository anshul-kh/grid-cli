package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anshul-kh/grid-cli/internal/api/stub"
	"github.com/anshul-kh/grid-cli/internal/workspace"
)

// TestCheckMissingAttemptFileExitsUsageError pins the plan's exit-5
// contract: no .grid/attempt.json means "run `grid run <id>` first", not a
// network error or a bare panic.
func TestCheckMissingAttemptFileExitsUsageError(t *testing.T) {
	// FindAttempt's state-directory fallback must never see the real
	// developer machine's config dir in a test.
	isolateHome(t)
	setCmdCfg("stub://", false)
	cfg.Dir = t.TempDir()

	_, _, err := captureOutput(t, func() error {
		return runCheck(checkCmd, nil)
	})
	if code := exitCodeOf(t, err); code != 5 {
		t.Fatalf("check without attempt.json: exit code = %d (err %v), want 5", code, err)
	}
	if !strings.Contains(err.Error(), "grid run") {
		t.Errorf("error should suggest `grid run <id>`, got: %s", err.Error())
	}
}

// TestCheckNeverSubmitsAndNeedsNoAuth is the plan's core grid-check
// invariant: a live stub is wired up and the credential store is
// deliberately empty (never logged in), yet grid check still succeeds and
// never makes a single submission — proving both that it needs no
// authentication and that its stub submission handler hit-count is 0.
func TestCheckNeverSubmitsAndNeedsNoAuth(t *testing.T) {
	st, srv := newStubCmdServer(t)
	isolateHome(t) // sandboxed, deliberately empty — no grid login was run
	setCmdCfg(srv.URL+"/api/v1", false)
	dir := t.TempDir()
	cfg.Dir = dir
	writeTestAttempt(t, dir, stub.AttemptNonce, simpleCheckSpecFixture)

	stdout, _, err := captureOutput(t, func() error {
		return runCheck(checkCmd, nil)
	})
	if err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	if got := st.SubmissionCount(); got != 0 {
		t.Errorf("grid check submitted %d time(s), want 0", got)
	}
	if !strings.Contains(stdout, "not a verdict") {
		t.Errorf("stdout missing the explicit cannot-judge footer:\n%s", stdout)
	}
	for _, forbidden := range []string{"PASSED", "FAILED", "INVALID", "passed", "failed"} {
		if strings.Contains(stdout, forbidden) {
			t.Errorf("grid check output contains verdict word %q — it cannot judge:\n%s", forbidden, stdout)
		}
	}
	if !strings.Contains(stdout, "measured 3/3") {
		t.Errorf("stdout missing the measured-count footer:\n%s", stdout)
	}
}

// TestCheckExitsOneOnMeasurementError pins the plan's other exit
// condition: any check reported "error" (the measurement itself could not
// be taken) makes grid check exit 1, even though it otherwise always
// exits 0.
func TestCheckExitsOneOnMeasurementError(t *testing.T) {
	isolateHome(t)
	setCmdCfg("stub://", false)
	dir := t.TempDir()
	cfg.Dir = dir
	writeTestAttempt(t, dir, stub.AttemptNonce, failingCheckSpecFixture)

	stdout, _, err := captureOutput(t, func() error {
		return runCheck(checkCmd, nil)
	})
	if code := exitCodeOf(t, err); code != 1 {
		t.Fatalf("check with a measurement error: exit code = %d (err %v), want 1", code, err)
	}
	if !strings.Contains(stdout, "error") {
		t.Errorf("stdout should show the errored measurement:\n%s", stdout)
	}
}

// TestCheckRunsInATempCwdForNoneModeAndRecordsAgainstStateDir proves
// GRID_SPEC §4's "checks run in a temp cwd" for mode: none: grid check
// finds the attempt in the CLI's state directory (no local .grid
// anywhere), measures successfully, and records last-run.json against the
// state directory (not the throwaway temp cwd, which cleanup deletes
// before this command returns) — so a later `grid status` can still show
// it.
func TestCheckRunsInATempCwdForNoneModeAndRecordsAgainstStateDir(t *testing.T) {
	isolateHome(t)
	af := writeTestNoneAttempt(t, stub.AttemptNonce, noneModeCheckSpecFixture)
	setCmdCfg("stub://", false)
	cfg.Dir = t.TempDir() // an unrelated cwd — none-mode never needs a real one

	stdout, _, err := captureOutput(t, func() error {
		return runCheck(checkCmd, nil)
	})
	if err != nil {
		t.Fatalf("runCheck (mode: none): %v", err)
	}
	if !strings.Contains(stdout, "measured 3/3") {
		t.Errorf("stdout missing the measured-count footer:\n%s", stdout)
	}

	lastRun, err := workspace.LoadLastRun(af.DirPath)
	if err != nil {
		t.Fatalf("LoadLastRun against the state dir: %v", err)
	}
	if lastRun.Measured != 3 {
		t.Errorf("last-run.json Measured = %d, want 3", lastRun.Measured)
	}
}

// TestCheckJSONOutputShape proves --json emits the stable, documented
// schema with a row per check and the measured/total counts.
func TestCheckJSONOutputShape(t *testing.T) {
	isolateHome(t)
	setCmdCfg("stub://", true)
	dir := t.TempDir()
	cfg.Dir = dir
	writeTestAttempt(t, dir, stub.AttemptNonce, simpleCheckSpecFixture)

	stdout, _, err := captureOutput(t, func() error {
		return runCheck(checkCmd, nil)
	})
	if err != nil {
		t.Fatalf("runCheck --json: %v", err)
	}
	var out checkJSON
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("check --json stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if out.Measured != 3 || out.Total != 3 || len(out.Results) != 3 {
		t.Errorf("check --json payload = %+v, want 3 measured/total/results", out)
	}
}

package cmd

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/anshul-kh/grid-cli/internal/api"
	"github.com/anshul-kh/grid-cli/internal/workspace"
)

// unreachableAPIURL points at a port nothing listens on — any attempt by
// grid status to make an HTTP call would fail immediately, which is the
// tripwire TestStatusIsFullyOfflineAndShowsExpiryAndLastResults relies on:
// no httptest server is ever started for grid status's own tests.
const unreachableAPIURL = "http://127.0.0.1:1"

// TestStatusMissingAttemptFileExitsFive pins the plan's exit-5 contract for
// grid status: no .grid/attempt.json means "not in a grid workspace", not a
// network error or a bare panic.
func TestStatusMissingAttemptFileExitsFive(t *testing.T) {
	// FindAttempt's state-directory fallback must never see the real
	// developer machine's config dir in a test.
	isolateHome(t)
	setCmdCfg(unreachableAPIURL, false)
	cfg.Dir = t.TempDir()

	_, _, err := captureOutput(t, func() error {
		return runStatus(statusCmd, nil)
	})
	if code := exitCodeOf(t, err); code != 5 {
		t.Fatalf("status without attempt.json: exit code = %d (err %v), want 5", code, err)
	}
	if !strings.Contains(err.Error(), "grid run") {
		t.Errorf("error should suggest `grid run <id>`, got: %s", err.Error())
	}
}

// TestStatusIsFullyOfflineAndShowsExpiryAndLastResults proves grid status
// never touches the network — no httptest server is started at all, and
// cfg.APIURL points nowhere — while still rendering the attempt's expiry
// countdown and the last grid check/grid submit records from a fixture
// workspace.
func TestStatusIsFullyOfflineAndShowsExpiryAndLastResults(t *testing.T) {
	setCmdCfg(unreachableAPIURL, false)
	dir := t.TempDir()
	cfg.Dir = dir
	writeTestAttempt(t, dir, "nonce-1", simpleCheckSpecFixture)

	if err := workspace.WriteLastRun(dir, workspace.LastRun{
		RanAt:    time.Now().Add(-5 * time.Minute).UTC(),
		Measured: 2,
		Total:    3,
		Checks: []workspace.LastRunCheck{
			{CheckID: "resp-server-exists", Title: "The RESP server binary exists", Status: api.CheckStatusOK, DurationMS: 5},
			{CheckID: "resp-server-pings", Title: "RESP server answers PING", Status: api.CheckStatusError, DurationMS: 10},
		},
	}); err != nil {
		t.Fatalf("WriteLastRun: %v", err)
	}
	if err := workspace.WriteLastVerdict(dir, workspace.LastVerdict{
		SubmittedAt: time.Now().Add(-2 * time.Minute).UTC(),
		Verdict:     api.VerdictFailed,
		Checks: []api.SubmissionCheck{
			{CheckID: "resp-server-exists", Title: "The RESP server binary exists", Passed: true},
			{CheckID: "resp-server-pings", Title: "RESP server answers PING", Passed: false, Message: "exit code 1"},
		},
	}); err != nil {
		t.Fatalf("WriteLastVerdict: %v", err)
	}

	stdout, _, err := captureOutput(t, func() error {
		return runStatus(statusCmd, nil)
	})
	if err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	if !strings.Contains(stdout, "expires in") {
		t.Errorf("stdout missing the expiry countdown:\n%s", stdout)
	}
	if !strings.Contains(stdout, "ago") {
		t.Errorf("stdout missing the last-run/last-verdict relative timestamps:\n%s", stdout)
	}
	if !strings.Contains(stdout, "measured 2/3") {
		t.Errorf("stdout missing the last-run measured count:\n%s", stdout)
	}
	if !strings.Contains(stdout, "FAILED") {
		t.Errorf("stdout missing the last verdict:\n%s", stdout)
	}
}

// TestStatusNoLastResultsSaysSo: a fresh `grid run` with no check/submit
// yet shows explicit "none" hints, not an empty table.
func TestStatusNoLastResultsSaysSo(t *testing.T) {
	setCmdCfg(unreachableAPIURL, false)
	dir := t.TempDir()
	cfg.Dir = dir
	writeTestAttempt(t, dir, "nonce-1", simpleCheckSpecFixture)

	stdout, _, err := captureOutput(t, func() error {
		return runStatus(statusCmd, nil)
	})
	if err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	if !strings.Contains(stdout, "last check: none") || !strings.Contains(stdout, "last submit: none") {
		t.Errorf("stdout missing the no-local-results hints:\n%s", stdout)
	}
}

// TestStatusExpiredAttemptSaysSo proves an attempt past its expiry renders
// as expired, not as a positive countdown.
func TestStatusExpiredAttemptSaysSo(t *testing.T) {
	setCmdCfg(unreachableAPIURL, false)
	dir := t.TempDir()
	cfg.Dir = dir
	writeTestAttempt(t, dir, "nonce-1", simpleCheckSpecFixture)
	af, err := workspace.LoadAttemptFile(dir)
	if err != nil {
		t.Fatalf("LoadAttemptFile: %v", err)
	}
	af.ExpiresAt = time.Now().Add(-time.Hour).UTC()
	if err := workspace.WriteAttemptFile(dir, af); err != nil {
		t.Fatalf("WriteAttemptFile: %v", err)
	}

	stdout, _, err := captureOutput(t, func() error {
		return runStatus(statusCmd, nil)
	})
	if err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	if !strings.Contains(stdout, "expired") {
		t.Errorf("stdout should say the attempt expired:\n%s", stdout)
	}
}

// TestStatusFindsNoneModeAttemptFromAnyCwdAndShowsNoDirectory proves grid
// status finds a mode: none attempt via FindAttempt's state-directory
// fallback from a cwd that was never scaffolded, and renders something
// honest in place of a project directory path rather than the CLI's
// internal state pseudo-directory.
func TestStatusFindsNoneModeAttemptFromAnyCwdAndShowsNoDirectory(t *testing.T) {
	isolateHome(t)
	writeTestNoneAttempt(t, "nonce-1", simpleCheckSpecFixture)
	setCmdCfg(unreachableAPIURL, false)
	cfg.Dir = t.TempDir() // unrelated to the state dir the attempt lives in

	stdout, _, err := captureOutput(t, func() error {
		return runStatus(statusCmd, nil)
	})
	if err != nil {
		t.Fatalf("runStatus (mode: none): %v", err)
	}
	if !strings.Contains(stdout, "checks your machine") {
		t.Errorf("stdout should explain there is no project directory:\n%s", stdout)
	}
	if strings.Contains(stdout, "attempts") {
		t.Errorf("stdout should never leak the CLI's internal state directory path:\n%s", stdout)
	}
}

// TestStatusJSONOutputShape proves --json emits the stable schema with the
// expiry fields and nil last-run/last-verdict objects when neither exists.
func TestStatusJSONOutputShape(t *testing.T) {
	setCmdCfg(unreachableAPIURL, true)
	dir := t.TempDir()
	cfg.Dir = dir
	writeTestAttempt(t, dir, "nonce-1", simpleCheckSpecFixture)

	stdout, _, err := captureOutput(t, func() error {
		return runStatus(statusCmd, nil)
	})
	if err != nil {
		t.Fatalf("runStatus --json: %v", err)
	}
	var got statusJSON
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("status --json stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if got.Title != "Speak RESP" {
		t.Errorf("status --json Title = %q", got.Title)
	}
	if got.Expired {
		t.Error("status --json Expired = true, want false (a fresh attempt.json)")
	}
	if got.LastRun != nil || got.LastVerdict != nil {
		t.Errorf("status --json should omit lastRun/lastVerdict when neither exists: %+v", got)
	}
}

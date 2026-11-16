// Golden-file tests for grid run/check/submit/whoami/status/courses/version
// (TASK.md §7 pass 9 item 2): each captures the real human-rendered stdout
// (colour off — captureOutput's os.Pipe is never a terminal, so
// ui.ColorEnabled already disables it the same way a piped `grid ... |
// less` invocation would) against a fixture in testdata/golden/*.txt.
//
// Golden files must be deterministic: normalizeGolden strips every value
// this CLI computes from the wall clock, the local machine's speed, or the
// host triple (TASK.md §5 — this suite runs on Windows/macOS/Linux CI, and
// `go test` reruns at an arbitrary later wall-clock time) before comparing.
//
// Set UPDATE_GOLDEN=1 to (re)write every fixture from the current
// (normalised) output — review the diff before committing it.
package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/anshul-kh/grid-cli/internal/api"
	"github.com/anshul-kh/grid-cli/internal/api/stub"
	"github.com/anshul-kh/grid-cli/internal/workspace"
)

const updateGoldenEnv = "UPDATE_GOLDEN"

func goldenPath(name string) string {
	return filepath.Join("..", "testdata", "golden", name+".txt")
}

var (
	durationMSRe = regexp.MustCompile(`\b\d+ms\b`)
	agoRe        = regexp.MustCompile(`\b\d+[dhms] ago\b`)
	expiresInRe  = regexp.MustCompile(`expires in \d+[dhms]`)
	expiredAgoRe = regexp.MustCompile(`expired \d+[dhms] ago`)
)

// normalizeGolden replaces every value this CLI computes from the wall
// clock, the local machine's speed, or the host triple with a stable
// placeholder.
func normalizeGolden(s string) string {
	s = durationMSRe.ReplaceAllString(s, "<MS>ms")
	s = expiredAgoRe.ReplaceAllString(s, "expired <DURATION> ago")
	s = expiresInRe.ReplaceAllString(s, "expires in <DURATION>")
	s = strings.ReplaceAll(s, "just now", "<AGO>")
	s = agoRe.ReplaceAllString(s, "<AGO>")
	s = strings.ReplaceAll(s, runtime.GOOS, "<OS>")
	s = strings.ReplaceAll(s, runtime.GOARCH, "<ARCH>")
	s = strings.ReplaceAll(s, runtime.Version(), "<GOVERSION>")
	return s
}

// assertGolden normalises got, applies any test-specific replacements
// (e.g. a t.TempDir() path, which is random every run and cannot be
// matched by a generic regex), and compares the result to
// testdata/golden/<name>.txt.
func assertGolden(t *testing.T, name, got string, replacements ...string) {
	t.Helper()
	got = normalizeGolden(got)
	for i := 0; i+1 < len(replacements); i += 2 {
		got = strings.ReplaceAll(got, replacements[i], replacements[i+1])
	}
	path := goldenPath(name)
	if os.Getenv(updateGoldenEnv) != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("writing golden file %s: %v", path, err)
		}
		t.Skipf("UPDATE_GOLDEN=1: wrote %s — rerun without it to verify", path)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden file %s: %v (run with UPDATE_GOLDEN=1 to create it)", path, err)
	}
	if got != string(want) {
		t.Errorf("golden mismatch for %s (run with UPDATE_GOLDEN=1 to update if this change is intentional):\n--- got ---\n%s\n--- want ---\n%s", name, got, string(want))
	}
}

func TestGoldenVersion(t *testing.T) {
	setCmdCfg(unreachableAPIURL, false)
	stdout, _, err := captureOutput(t, func() error { return versionCmd.RunE(versionCmd, nil) })
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	assertGolden(t, "version", stdout)
}

func TestGoldenWhoami(t *testing.T) {
	_, srv := newStubCmdServer(t)
	credsPath := isolateHome(t)
	loginAsStubUser(t, credsPath)
	setCmdCfg(srv.URL+"/api/v1", false)

	stdout, _, err := captureOutput(t, func() error { return runWhoami(whoamiCmd, nil) })
	if err != nil {
		t.Fatalf("runWhoami: %v", err)
	}
	assertGolden(t, "whoami", stdout)
}

func TestGoldenCourses(t *testing.T) {
	_, srv := newStubCmdServer(t)
	credsPath := isolateHome(t)
	loginAsStubUser(t, credsPath)
	setCmdCfg(srv.URL+"/api/v1", false)

	stdout, _, err := captureOutput(t, func() error { return runCourses(coursesCmd, nil) })
	if err != nil {
		t.Fatalf("runCourses: %v", err)
	}
	assertGolden(t, "courses", stdout)
}

func TestGoldenRun(t *testing.T) {
	resetRunFlags(t)
	_, srv := newStubCmdServer(t)
	credsPath := isolateHome(t)
	loginAsStubUser(t, credsPath)
	base := t.TempDir()
	setCmdCfg(srv.URL+"/api/v1", false)
	cfg.Dir = base

	stdout, _, err := captureOutput(t, func() error { return runRun(runCmd, []string{stub.LessonID}) })
	if err != nil {
		t.Fatalf("runRun: %v", err)
	}
	assertGolden(t, "run", stdout)
}

func TestGoldenCheck(t *testing.T) {
	isolateHome(t)
	setCmdCfg(unreachableAPIURL, false)
	dir := t.TempDir()
	cfg.Dir = dir
	writeTestAttempt(t, dir, stub.AttemptNonce, simpleCheckSpecFixture)

	stdout, _, err := captureOutput(t, func() error { return runCheck(checkCmd, nil) })
	if err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	assertGolden(t, "check", stdout)
}

func TestGoldenSubmit(t *testing.T) {
	credsPath := isolateHome(t)
	loginAsStubUser(t, credsPath)
	_, srv := newStubCmdServer(t)
	setCmdCfg(srv.URL+"/api/v1", false)
	dir := t.TempDir()
	cfg.Dir = dir
	writeTestAttempt(t, dir, stub.AttemptNonce, simpleCheckSpecFixture)

	stdout, _, err := captureOutput(t, func() error { return runSubmit(submitCmd, nil) })
	if err != nil {
		t.Fatalf("runSubmit: %v", err)
	}
	assertGolden(t, "submit", stdout)
}

func TestGoldenStatus(t *testing.T) {
	isolateHome(t)
	setCmdCfg(unreachableAPIURL, false)
	dir := t.TempDir()
	cfg.Dir = dir
	writeTestAttempt(t, dir, stub.AttemptNonce, simpleCheckSpecFixture)

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

	stdout, _, err := captureOutput(t, func() error { return runStatus(statusCmd, nil) })
	if err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	assertGolden(t, "status", stdout, dir, "<DIR>")
}

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anshul-kh/grid-cli/internal/api"
	"github.com/anshul-kh/grid-cli/internal/api/stub"
	"github.com/anshul-kh/grid-cli/internal/workspace"
)

// resetOpenFlags restores grid open's package-level flag state between
// tests (matches the existing package-var + init() command style; see
// run_test.go's resetRunFlags).
func resetOpenFlags(t *testing.T) {
	t.Helper()
	openPrint = false
	t.Cleanup(func() { openPrint = false })
}

// stubOpenBrowser swaps openBrowser for a test double that records every
// call and returns err, and restores the real opener afterwards.
func stubOpenBrowser(t *testing.T, err error) *[]string {
	t.Helper()
	var calls []string
	old := openBrowser
	openBrowser = func(ctx context.Context, url string) error {
		calls = append(calls, url)
		return err
	}
	t.Cleanup(func() { openBrowser = old })
	return &calls
}

const testLessonURL = "http://localhost:3000/lessons/speak-resp"

func writeTestAttemptWithURL(t *testing.T, dir, url string) {
	t.Helper()
	af := workspace.AttemptFile{
		AttemptID:  stub.AttemptID,
		Nonce:      "nonce-1",
		ExpiresAt:  time.Now().Add(time.Hour).UTC(),
		Lesson:     api.Lesson{ID: stub.LessonID, Title: "Speak RESP", CourseSlug: stub.CourseSlug, LessonSlug: stub.LessonSlug},
		Spec:       json.RawMessage(simpleCheckSpecFixture),
		DirPath:    dir,
		CreatedAt:  time.Now().UTC(),
		CLIVersion: "test",
		LessonURL:  url,
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := workspace.WriteAttemptFile(dir, af); err != nil {
		t.Fatalf("WriteAttemptFile: %v", err)
	}
}

func TestOpenMissingAttemptFileExitsFive(t *testing.T) {
	resetOpenFlags(t)
	// FindAttempt's state-directory fallback must never see the real
	// developer machine's config dir in a test.
	isolateHome(t)
	setCmdCfg(unreachableAPIURL, false)
	cfg.Dir = t.TempDir()

	_, _, err := captureOutput(t, func() error {
		return runOpen(openCmd, nil)
	})
	if code := exitCodeOf(t, err); code != 5 {
		t.Fatalf("open without attempt.json: exit code = %d (err %v), want 5", code, err)
	}
	if !strings.Contains(err.Error(), "grid run") {
		t.Errorf("error should suggest `grid run <id>`, got: %s", err.Error())
	}
}

// TestOpenPrintOnlyPrintsAndNeverOpensABrowser proves --print prints the
// URL and never calls the browser opener at all.
func TestOpenPrintOnlyPrintsAndNeverOpensABrowser(t *testing.T) {
	resetOpenFlags(t)
	openPrint = true
	calls := stubOpenBrowser(t, nil)
	setCmdCfg(unreachableAPIURL, false)
	dir := t.TempDir()
	cfg.Dir = dir
	writeTestAttemptWithURL(t, dir, testLessonURL)

	stdout, _, err := captureOutput(t, func() error {
		return runOpen(openCmd, nil)
	})
	if err != nil {
		t.Fatalf("runOpen --print: %v", err)
	}
	if !strings.Contains(stdout, testLessonURL) {
		t.Errorf("stdout = %q, want it to contain the lesson URL", stdout)
	}
	if len(*calls) != 0 {
		t.Errorf("--print called the browser opener %d time(s), want 0", len(*calls))
	}
}

// TestOpenNeverFailsWhenBrowserOpenFails is the plan's core grid-open
// invariant: a browser command that fails (simulated here without actually
// shelling out) must never turn into a non-zero exit — the URL is always
// printed as the fallback.
func TestOpenNeverFailsWhenBrowserOpenFails(t *testing.T) {
	resetOpenFlags(t)
	calls := stubOpenBrowser(t, errors.New("no browser command available"))
	setCmdCfg(unreachableAPIURL, false)
	dir := t.TempDir()
	cfg.Dir = dir
	writeTestAttemptWithURL(t, dir, testLessonURL)

	stdout, _, err := captureOutput(t, func() error {
		return runOpen(openCmd, nil)
	})
	if err != nil {
		t.Fatalf("runOpen with a failing browser opener: want no error, got %v", err)
	}
	if !strings.Contains(stdout, testLessonURL) {
		t.Errorf("stdout = %q, want the URL printed as the fallback", stdout)
	}
	if len(*calls) != 1 {
		t.Errorf("browser opener called %d time(s), want exactly 1", len(*calls))
	}
}

// TestOpenFindsNoneModeAttemptFromAnyCwd proves grid open finds a
// mode: none attempt via FindAttempt's state-directory fallback from a cwd
// that was never scaffolded.
func TestOpenFindsNoneModeAttemptFromAnyCwd(t *testing.T) {
	resetOpenFlags(t)
	openPrint = true
	isolateHome(t)
	stateDir, err := workspace.StateDir(stub.LessonID)
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	af := workspace.AttemptFile{
		AttemptID:  stub.AttemptID,
		Nonce:      "nonce-1",
		ExpiresAt:  time.Now().Add(time.Hour).UTC(),
		Lesson:     api.Lesson{ID: stub.LessonID, Title: "Speak RESP", CourseSlug: stub.CourseSlug, LessonSlug: stub.LessonSlug},
		Spec:       []byte(simpleCheckSpecFixture),
		DirPath:    stateDir,
		CreatedAt:  time.Now().UTC(),
		CLIVersion: "test",
		LessonURL:  testLessonURL,
		Mode:       string(workspace.ModeNone),
	}
	if err := workspace.WriteAttemptFile(stateDir, af); err != nil {
		t.Fatalf("WriteAttemptFile: %v", err)
	}
	setCmdCfg(unreachableAPIURL, false)
	cfg.Dir = t.TempDir() // unrelated to the state dir the attempt lives in

	stdout, _, err := captureOutput(t, func() error {
		return runOpen(openCmd, nil)
	})
	if err != nil {
		t.Fatalf("runOpen (mode: none): %v", err)
	}
	if !strings.Contains(stdout, testLessonURL) {
		t.Errorf("stdout = %q, want it to contain the lesson URL", stdout)
	}
}

// TestOpenJSONOutputShape proves --json emits {url, opened}.
func TestOpenJSONOutputShape(t *testing.T) {
	resetOpenFlags(t)
	stubOpenBrowser(t, nil)
	setCmdCfg(unreachableAPIURL, true)
	dir := t.TempDir()
	cfg.Dir = dir
	writeTestAttemptWithURL(t, dir, testLessonURL)

	stdout, _, err := captureOutput(t, func() error {
		return runOpen(openCmd, nil)
	})
	if err != nil {
		t.Fatalf("runOpen --json: %v", err)
	}
	var got openJSON
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("open --json stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if got.URL != testLessonURL || !got.Opened {
		t.Errorf("open --json = %+v, want {%q, true}", got, testLessonURL)
	}
}

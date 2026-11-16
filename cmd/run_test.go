package cmd

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anshul-kh/grid-cli/internal/api/stub"
	"github.com/anshul-kh/grid-cli/internal/auth"
	"github.com/anshul-kh/grid-cli/internal/workspace"
)

// stubLessonDir is the workspace.dirName the stub's attempt fixture always
// returns (stub.go's attemptResponseTemplate: grid-<courseSlug>-<lessonSlug>).
const stubLessonDir = "grid-" + stub.CourseSlug + "-" + stub.LessonSlug

// resetRunFlags restores the run command's package-level flag state —
// these commands are called directly (runRun, not cmd.Execute()), so
// nothing else resets them between tests (plan Pass 6: match the existing
// package-var + init() command style already used by login/logout/whoami).
func resetRunFlags(t *testing.T) {
	t.Helper()
	runHere, runForce = false, false
	t.Cleanup(func() { runHere, runForce = false, false })
}

// loginAsStubUser stores credentials the stub server accepts.
func loginAsStubUser(t *testing.T, credsPath string) {
	t.Helper()
	if err := auth.NewStore(credsPath).Save(auth.Credentials{
		SessionKey: stub.SessionKey,
		User:       stubTestUser(),
	}); err != nil {
		t.Fatalf("Save credentials: %v", err)
	}
}

func TestRunCreatesWorkspaceAndAttemptFile(t *testing.T) {
	resetRunFlags(t)
	_, srv := newStubCmdServer(t)
	credsPath := isolateHome(t)
	loginAsStubUser(t, credsPath)

	base := t.TempDir()
	setCmdCfg(srv.URL+"/api/v1", false)
	cfg.Dir = base

	stdout, stderr, err := captureOutput(t, func() error {
		return runRun(runCmd, []string{stub.LessonID})
	})
	if err != nil {
		t.Fatalf("runRun: %v", err)
	}

	dir := filepath.Join(base, stubLessonDir)
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("reading scaffolded README.md: %v", err)
	}
	if strings.Contains(string(readme), "\r") {
		t.Errorf("README.md was not written with LF-only endings: %q", readme)
	}
	if !strings.Contains(string(readme), "Speak RESP") {
		t.Errorf("README.md missing expected starter content: %q", readme)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		t.Errorf("missing scaffolded go.mod: %v", err)
	}

	af, err := workspace.LoadAttemptFile(dir)
	if err != nil {
		t.Fatalf("LoadAttemptFile: %v", err)
	}
	if af.AttemptID != stub.AttemptID {
		t.Errorf("attempt.json attemptId = %q, want %q", af.AttemptID, stub.AttemptID)
	}
	if af.Nonce != stub.AttemptNonce {
		t.Errorf("attempt.json nonce = %q, want %q", af.Nonce, stub.AttemptNonce)
	}
	if af.ExpiresAt.IsZero() {
		t.Error("attempt.json expiresAt is zero")
	}
	if len(af.Spec) == 0 || !strings.Contains(string(af.Spec), "resp-server-exists") {
		t.Errorf("attempt.json spec missing public check-spec content: %s", af.Spec)
	}

	// In human mode the full numbered task list renders once, in the final
	// stdout block — stderr only gets a terse progress count, not the same
	// list again (pass 9: fixes the duplicate print pass 7's verifier
	// flagged; --json mode still puts the full list on stderr as progress,
	// covered separately by TestRunJSONStdoutIsMachineOnly).
	if !strings.Contains(stdout, "RESP server answers PING") {
		t.Errorf("stdout missing the task list: %s", stdout)
	}
	if strings.Contains(stderr, "RESP server answers PING") {
		t.Errorf("stderr duplicates the task list that stdout already renders: %s", stderr)
	}
	if !strings.Contains(stderr, "Found 3 task(s).") {
		t.Errorf("stderr missing the terse task-count progress line: %s", stderr)
	}
}

func TestRunRefusesNonEmptyDirWithoutForce(t *testing.T) {
	resetRunFlags(t)
	_, srv := newStubCmdServer(t)
	credsPath := isolateHome(t)
	loginAsStubUser(t, credsPath)

	base := t.TempDir()
	dir := filepath.Join(base, stubLessonDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stale := filepath.Join(dir, "keep-me.txt")
	if err := os.WriteFile(stale, []byte("do-not-touch\n"), 0o644); err != nil {
		t.Fatalf("seed stale file: %v", err)
	}

	setCmdCfg(srv.URL+"/api/v1", false)
	cfg.Dir = base

	_, _, err := captureOutput(t, func() error {
		return runRun(runCmd, []string{stub.LessonID})
	})
	if code := exitCodeOf(t, err); code != 1 {
		t.Fatalf("run on non-empty dir without --force: exit code = %d (err %v), want 1", code, err)
	}

	got, err := os.ReadFile(stale)
	if err != nil {
		t.Fatalf("re-reading stale file: %v", err)
	}
	if string(got) != "do-not-touch\n" {
		t.Errorf("existing file was modified: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".grid")); err == nil {
		t.Error(".grid was created despite the refusal")
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err == nil {
		t.Error("README.md was written despite the refusal")
	}
}

// TestRunRejectsEscapingDirNameEndToEnd proves the fix for the path-
// traversal defect found in verification, driven through the real
// `grid run` command over real HTTP (not just the exported workspace
// helpers in isolation): a malicious/escaping workspace.dirName from the
// server must be refused, with nothing written outside the invocation's
// base directory.
func TestRunRejectsEscapingDirNameEndToEnd(t *testing.T) {
	resetRunFlags(t)
	st, srv := newStubCmdServer(t)
	credsPath := isolateHome(t)
	loginAsStubUser(t, credsPath)

	// A nested base, so an escape would land somewhere clearly outside it
	// and be easy to assert never happened.
	root := t.TempDir()
	base := filepath.Join(root, "depth1", "depth2", "depth3")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir base: %v", err)
	}
	st.SetWorkspaceDirName("../../../escaped-dir")

	setCmdCfg(srv.URL+"/api/v1", false)
	cfg.Dir = base

	_, _, err := captureOutput(t, func() error {
		return runRun(runCmd, []string{stub.LessonID})
	})
	if code := exitCodeOf(t, err); code != 4 {
		t.Fatalf("run with an escaping dirName: exit code = %d (err %v), want 4", code, err)
	}
	if _, err := os.Stat(filepath.Join(root, "escaped-dir")); err == nil {
		t.Fatal("run escaped its base directory and wrote outside it")
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("ReadDir base: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("run left entries in base despite rejecting the dirName: %v", entries)
	}
}

func TestRunForceOverwritesStarterFiles(t *testing.T) {
	resetRunFlags(t)
	_, srv := newStubCmdServer(t)
	credsPath := isolateHome(t)
	loginAsStubUser(t, credsPath)

	base := t.TempDir()
	dir := filepath.Join(base, stubLessonDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stalePath := filepath.Join(dir, "README.md")
	if err := os.WriteFile(stalePath, []byte("stale content\n"), 0o644); err != nil {
		t.Fatalf("seed stale README: %v", err)
	}

	setCmdCfg(srv.URL+"/api/v1", false)
	cfg.Dir = base
	runForce = true

	_, _, err := captureOutput(t, func() error {
		return runRun(runCmd, []string{stub.LessonID})
	})
	if err != nil {
		t.Fatalf("runRun --force: %v", err)
	}

	got, err := os.ReadFile(stalePath)
	if err != nil {
		t.Fatalf("reading README.md after --force: %v", err)
	}
	if strings.Contains(string(got), "stale content") {
		t.Errorf("--force did not overwrite the stale starter file: %q", got)
	}
	if !strings.Contains(string(got), "Speak RESP") {
		t.Errorf("README.md after --force missing starter content: %q", got)
	}
}

func TestRunHereScaffoldsIntoCurrentDirectory(t *testing.T) {
	resetRunFlags(t)
	_, srv := newStubCmdServer(t)
	credsPath := isolateHome(t)
	loginAsStubUser(t, credsPath)

	here := t.TempDir()
	setCmdCfg(srv.URL+"/api/v1", false)
	cfg.Dir = here
	runHere = true

	_, _, err := captureOutput(t, func() error {
		return runRun(runCmd, []string{stub.LessonID})
	})
	if err != nil {
		t.Fatalf("runRun --here: %v", err)
	}

	if _, err := os.Stat(filepath.Join(here, "README.md")); err != nil {
		t.Errorf("--here did not scaffold into the current directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(here, stubLessonDir)); err == nil {
		t.Error("--here created an extra subdirectory instead of using the current directory")
	}
}

// TestRunNonUUIDExitsBeforeAnyHTTPRequest proves the plan's exit-5 contract:
// a malformed lesson id is rejected before the client ever dials out, not
// just before a successful attempt fetch.
func TestRunNonUUIDExitsBeforeAnyHTTPRequest(t *testing.T) {
	resetRunFlags(t)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	// Deliberately no isolateHome/credentials: a usage error must surface
	// before the command even looks for a logged-in session.
	setCmdCfg(srv.URL+"/api/v1", false)
	cfg.Dir = t.TempDir()

	_, _, err := captureOutput(t, func() error {
		return runRun(runCmd, []string{"not-a-uuid"})
	})
	var usageErr *UsageError
	if !errors.As(err, &usageErr) {
		t.Fatalf("run with a non-UUID lesson id: err = %v (%T), want *UsageError", err, err)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("non-UUID lesson id made %d HTTP request(s), want 0", got)
	}
}

// TestRunNeverLeaksSessionKey greps every surface the plan's criterion
// cares about — stdout, stderr, and .grid/attempt.json — for the stub's
// session key.
func TestRunNeverLeaksSessionKey(t *testing.T) {
	resetRunFlags(t)
	_, srv := newStubCmdServer(t)
	credsPath := isolateHome(t)
	loginAsStubUser(t, credsPath)

	base := t.TempDir()
	setCmdCfg(srv.URL+"/api/v1", false)
	cfg.Dir = base

	stdout, stderr, err := captureOutput(t, func() error {
		return runRun(runCmd, []string{stub.LessonID})
	})
	if err != nil {
		t.Fatalf("runRun: %v", err)
	}
	if strings.Contains(stdout, stub.SessionKey) || strings.Contains(stderr, stub.SessionKey) {
		t.Fatal("run leaked the session key into command output")
	}

	raw, err := os.ReadFile(workspace.AttemptFilePath(filepath.Join(base, stubLessonDir)))
	if err != nil {
		t.Fatalf("reading attempt.json: %v", err)
	}
	if strings.Contains(string(raw), stub.SessionKey) || strings.Contains(string(raw), "sessionKey") {
		t.Fatalf(".grid/attempt.json leaked a session key: %s", raw)
	}
}

// TestRunExits3On401 proves an expired/invalid session maps to the
// plan's "401 → exit 3 with `Run grid login`" instruction.
func TestRunExits3On401(t *testing.T) {
	resetRunFlags(t)
	_, srv := newStubCmdServer(t)
	credsPath := isolateHome(t)
	// A session key the stub does not accept → the attempt endpoint 401s.
	if err := auth.NewStore(credsPath).Save(auth.Credentials{
		SessionKey: "a-session-key-the-server-rejects",
		User:       stubTestUser(),
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	setCmdCfg(srv.URL+"/api/v1", false)
	cfg.Dir = t.TempDir()

	_, _, err := captureOutput(t, func() error {
		return runRun(runCmd, []string{stub.LessonID})
	})
	if code := exitCodeOf(t, err); code != 3 {
		t.Fatalf("run on 401: exit code = %d (err %v), want 3", code, err)
	}
	if !strings.Contains(err.Error(), "grid login") {
		t.Errorf("run 401 error should suggest grid login, got: %s", err.Error())
	}
}

// TestRunExits4On404 proves an unknown lesson id maps to the plan's
// "404 → exit 4 with clear message" instruction.
func TestRunExits4On404(t *testing.T) {
	resetRunFlags(t)
	st, srv := newStubCmdServer(t)
	credsPath := isolateHome(t)
	loginAsStubUser(t, credsPath)
	st.ForceStatus("/cli/lessons/"+stub.LessonID+"/attempt", http.StatusNotFound)

	setCmdCfg(srv.URL+"/api/v1", false)
	cfg.Dir = t.TempDir()

	_, _, err := captureOutput(t, func() error {
		return runRun(runCmd, []string{stub.LessonID})
	})
	if code := exitCodeOf(t, err); code != 4 {
		t.Fatalf("run on 404: exit code = %d (err %v), want 4", code, err)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("run 404 error should be a clear 'not found' message, got: %s", err.Error())
	}
}

// TestRunJSONStdoutIsMachineOnly proves --json keeps stdout pure JSON and
// pushes the task list to stderr as progress only.
func TestRunJSONStdoutIsMachineOnly(t *testing.T) {
	resetRunFlags(t)
	_, srv := newStubCmdServer(t)
	credsPath := isolateHome(t)
	loginAsStubUser(t, credsPath)

	base := t.TempDir()
	setCmdCfg(srv.URL+"/api/v1", true)
	cfg.Dir = base

	stdout, stderr, err := captureOutput(t, func() error {
		return runRun(runCmd, []string{stub.LessonID})
	})
	if err != nil {
		t.Fatalf("runRun --json: %v", err)
	}

	var out runJSON
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("run --json stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if out.LessonID == "" || len(out.Tasks) == 0 {
		t.Errorf("run --json payload missing data: %+v", out)
	}
	if out.DirPath == "" {
		t.Error("run --json payload missing dirPath")
	}
	if strings.Contains(stdout, "Tasks:") {
		t.Errorf("--json stdout must carry only the JSON object, not the human task list:\n%s", stdout)
	}
	if !strings.Contains(stderr, "Tasks:") {
		t.Errorf("task list should still appear on stderr as progress:\n%s", stderr)
	}
}

// TestRunFolderModeNeverCreatesGit pins today's default: a plain directory,
// starter files, .grid/attempt.json, and — the one thing this pass must
// never regress — no .git.
func TestRunFolderModeNeverCreatesGit(t *testing.T) {
	resetRunFlags(t)
	_, srv := newStubCmdServer(t)
	credsPath := isolateHome(t)
	loginAsStubUser(t, credsPath)

	base := t.TempDir()
	setCmdCfg(srv.URL+"/api/v1", false)
	cfg.Dir = base

	_, _, err := captureOutput(t, func() error {
		return runRun(runCmd, []string{stub.LessonID})
	})
	if err != nil {
		t.Fatalf("runRun: %v", err)
	}
	dir := filepath.Join(base, stubLessonDir)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		t.Error("mode: folder must never create .git")
	}
	af, err := workspace.LoadAttemptFile(dir)
	if err != nil {
		t.Fatalf("LoadAttemptFile: %v", err)
	}
	if af.Mode != string(workspace.ModeFolder) {
		t.Errorf("attempt.json mode = %q, want %q", af.Mode, workspace.ModeFolder)
	}
}

// TestRunNoneModeCreatesNothingAndIsDiscoverableAnywhere is the headline
// behaviour this pass exists for: a mode: none lesson ("is Docker
// installed") must litter nothing onto the filesystem — the invocation
// directory ends up with zero entries — yet the attempt it wrote is still
// discoverable by grid check/grid status/grid submit/grid open from any
// cwd (workspace.FindAttempt), because it lives in the CLI's own state
// directory, not a scaffolded workspace.
func TestRunNoneModeCreatesNothingAndIsDiscoverableAnywhere(t *testing.T) {
	resetRunFlags(t)
	st, srv := newStubCmdServer(t)
	st.SetWorkspaceMode("none")
	credsPath := isolateHome(t)
	loginAsStubUser(t, credsPath)

	base := t.TempDir()
	setCmdCfg(srv.URL+"/api/v1", false)
	cfg.Dir = base

	stdout, _, err := captureOutput(t, func() error {
		return runRun(runCmd, []string{stub.LessonID})
	})
	if err != nil {
		t.Fatalf("runRun (mode: none): %v", err)
	}
	if !strings.Contains(stdout, "nothing was created") {
		t.Errorf("stdout should explain that nothing was created:\n%s", stdout)
	}

	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("ReadDir(base): %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("mode: none created %d entries in the invocation directory, want 0: %v", len(entries), entries)
	}

	// Discoverable from a totally unrelated cwd, with no dirName/lesson id
	// to guide the lookup.
	af, err := workspace.FindAttempt(t.TempDir())
	if err != nil {
		t.Fatalf("FindAttempt from an unrelated cwd: %v", err)
	}
	if af.Mode != string(workspace.ModeNone) {
		t.Errorf("FindAttempt mode = %q, want %q", af.Mode, workspace.ModeNone)
	}
	if af.AttemptID != stub.AttemptID {
		t.Errorf("FindAttempt attemptId = %q, want %q", af.AttemptID, stub.AttemptID)
	}
}

// TestRunNoneModeJSONHasNoDirPath proves --json never invents a dirPath for
// a mode that created no directory.
func TestRunNoneModeJSONHasNoDirPath(t *testing.T) {
	resetRunFlags(t)
	st, srv := newStubCmdServer(t)
	st.SetWorkspaceMode("none")
	credsPath := isolateHome(t)
	loginAsStubUser(t, credsPath)

	setCmdCfg(srv.URL+"/api/v1", true)
	cfg.Dir = t.TempDir()

	stdout, _, err := captureOutput(t, func() error {
		return runRun(runCmd, []string{stub.LessonID})
	})
	if err != nil {
		t.Fatalf("runRun --json (mode: none): %v", err)
	}
	var out runJSON
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("run --json stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if out.Mode != string(workspace.ModeNone) {
		t.Errorf("run --json mode = %q, want %q", out.Mode, workspace.ModeNone)
	}
	if out.DirPath != "" {
		t.Errorf("run --json dirPath = %q, want empty for mode: none", out.DirPath)
	}
}

// TestRunRepoModeInitialisesGit proves mode: repo scaffolds the directory
// exactly like folder, plus a real git repo on top. Skipped if git isn't on
// PATH — the git-absent degrade path is covered separately below with a
// stubbed exec, mirroring gitclone_test.go's own split.
func TestRunRepoModeInitialisesGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed, skipping mode: repo test")
	}
	resetRunFlags(t)
	st, srv := newStubCmdServer(t)
	st.SetWorkspaceMode("repo")
	credsPath := isolateHome(t)
	loginAsStubUser(t, credsPath)

	base := t.TempDir()
	setCmdCfg(srv.URL+"/api/v1", false)
	cfg.Dir = base

	_, _, err := captureOutput(t, func() error {
		return runRun(runCmd, []string{stub.LessonID})
	})
	if err != nil {
		t.Fatalf("runRun (mode: repo): %v", err)
	}
	dir := filepath.Join(base, stubLessonDir)
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git rev-parse --git-dir in %s: %v\n%s", dir, err, out)
	}
	af, err := workspace.LoadAttemptFile(dir)
	if err != nil {
		t.Fatalf("LoadAttemptFile: %v", err)
	}
	if af.Mode != string(workspace.ModeRepo) {
		t.Errorf("attempt.json mode = %q, want %q", af.Mode, workspace.ModeRepo)
	}
}

// TestRunRepoModeWarnsAndContinuesWhenGitUnavailable proves the
// degrade-gracefully policy end to end: with no git on PATH, `grid run`
// still succeeds (the scaffold completes, exit 0), warns on stderr, and
// simply never creates .git — the git.* checks will then honestly report
// as unmeasurable, rather than this command failing the whole run over an
// optional tool.
func TestRunRepoModeWarnsAndContinuesWhenGitUnavailable(t *testing.T) {
	resetRunFlags(t)
	st, srv := newStubCmdServer(t)
	st.SetWorkspaceMode("repo")
	credsPath := isolateHome(t)
	loginAsStubUser(t, credsPath)

	base := t.TempDir()
	setCmdCfg(srv.URL+"/api/v1", false)
	cfg.Dir = base
	t.Setenv("PATH", t.TempDir()) // no git anywhere on PATH

	stdout, stderr, err := captureOutput(t, func() error {
		return runRun(runCmd, []string{stub.LessonID})
	})
	if err != nil {
		t.Fatalf("runRun (mode: repo, no git): %v", err)
	}
	if !strings.Contains(stderr, "git is not installed") {
		t.Errorf("stderr should warn that git is unavailable:\n%s", stderr)
	}
	dir := filepath.Join(base, stubLessonDir)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		t.Error("no .git should exist when git was unavailable")
	}
	if !strings.Contains(stdout, "Speak RESP") {
		t.Errorf("run should still complete and render its normal output:\n%s", stdout)
	}
}

// TestRunBundleModeFallsThroughToRepo proves P18's documented stub: until
// P20 implements the bundle download, mode: bundle gets the same
// directory-plus-git treatment as mode: repo, with an honest note that no
// starter bundle was actually fetched — never a fabricated URL.
func TestRunBundleModeFallsThroughToRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed, skipping mode: bundle test")
	}
	resetRunFlags(t)
	st, srv := newStubCmdServer(t)
	st.SetWorkspaceMode("bundle")
	credsPath := isolateHome(t)
	loginAsStubUser(t, credsPath)

	base := t.TempDir()
	setCmdCfg(srv.URL+"/api/v1", false)
	cfg.Dir = base

	stdout, _, err := captureOutput(t, func() error {
		return runRun(runCmd, []string{stub.LessonID})
	})
	if err != nil {
		t.Fatalf("runRun (mode: bundle): %v", err)
	}
	dir := filepath.Join(base, stubLessonDir)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Errorf("mode: bundle falling through to repo should still init git: %v", err)
	}
	if strings.Contains(stdout, "TODO") {
		t.Error("mode: bundle's message must not contain a literal TODO placeholder")
	}
	if !strings.Contains(stdout, "no starter bundle declared") {
		t.Errorf("stdout should honestly say no bundle was fetched:\n%s", stdout)
	}
}

// TestRunUnknownModeDefaultsToFolder proves ParseMode's safety net end to
// end: a server value this CLI build doesn't recognise degrades to the
// least-privileged default (folder: a plain directory, no git) rather than
// failing the run or escalating to repo/bundle behaviour.
func TestRunUnknownModeDefaultsToFolder(t *testing.T) {
	resetRunFlags(t)
	st, srv := newStubCmdServer(t)
	st.SetWorkspaceMode("some-future-mode-this-build-has-never-heard-of")
	credsPath := isolateHome(t)
	loginAsStubUser(t, credsPath)

	base := t.TempDir()
	setCmdCfg(srv.URL+"/api/v1", false)
	cfg.Dir = base

	_, _, err := captureOutput(t, func() error {
		return runRun(runCmd, []string{stub.LessonID})
	})
	if err != nil {
		t.Fatalf("runRun (unknown mode): %v", err)
	}
	dir := filepath.Join(base, stubLessonDir)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		t.Error("an unknown mode must never escalate to creating .git")
	}
	af, err := workspace.LoadAttemptFile(dir)
	if err != nil {
		t.Fatalf("LoadAttemptFile: %v", err)
	}
	if af.Mode != string(workspace.ModeFolder) {
		t.Errorf("attempt.json mode = %q, want %q (ParseMode's default)", af.Mode, workspace.ModeFolder)
	}
}

// TestRunExits1OnNotEnrolled proves the GRID_SPEC §5.2/§5.3/§6 "no
// auto-enroll" contract end to end: a 403 code=not_enrolled maps to exit 1
// (not 3/4, and never a silent enroll-on-behalf-of-the-user), and the
// server's enrollUrl is printed to stderr verbatim.
func TestRunExits1OnNotEnrolled(t *testing.T) {
	resetRunFlags(t)
	st, srv := newStubCmdServer(t)
	const wantURL = "http://localhost:3000/courses/build-a-redis-core-in-go"
	st.ForceNotEnrolled("/cli/lessons/"+stub.LessonID+"/attempt", stub.CourseSlug, wantURL)
	credsPath := isolateHome(t)
	loginAsStubUser(t, credsPath)

	setCmdCfg(srv.URL+"/api/v1", false)
	cfg.Dir = t.TempDir()

	_, stderr, err := captureOutput(t, func() error {
		return runRun(runCmd, []string{stub.LessonID})
	})
	if code := exitCodeOf(t, err); code != 1 {
		t.Fatalf("run on not_enrolled: exit code = %d (err %v), want 1", code, err)
	}
	if !strings.Contains(stderr, wantURL) {
		t.Errorf("stderr should contain the enrollUrl verbatim, got: %s", stderr)
	}
}

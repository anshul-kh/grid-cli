package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anshul-kh/grid-cli/internal/api"
	"github.com/anshul-kh/grid-cli/internal/api/stub"
	"github.com/anshul-kh/grid-cli/internal/workspace"
)

// simpleCheckSpecFixture is a check-spec that `grid check`/`grid submit`
// tests fully control: portable measurements (`go version` always exists
// and exits 0) under the stub's fixed Redis-lesson check ids, so the stub's
// canned submission fixtures — which reference those exact ids — line up
// with whatever this test package submits.
const simpleCheckSpecFixture = `{
  "version": 1,
  "workdir": ".",
  "checks": [
    {"id": "resp-server-exists", "title": "The RESP server binary exists", "type": "file.exists", "path": "README.md"},
    {"id": "resp-server-pings", "title": "RESP server answers PING", "type": "cmd.exit_code", "run": ["go", "version"]},
    {"id": "resp-server-handles-commands", "title": "Server handles the COMMAND listing", "type": "cmd.exit_code", "run": ["go", "version"]}
  ]
}`

// simpleCheckSpecWithFailingSetupFixture adds a setup[] step whose command
// cannot be executed at all — the local half of the §5.3 addendum's
// automatic INVALID rule (every declared check comes back "skipped", never
// measured).
const simpleCheckSpecWithFailingSetupFixture = `{
  "version": 1,
  "workdir": ".",
  "setup": [{"run": ["this-command-does-not-exist-grid-test-xyz"]}],
  "checks": [
    {"id": "resp-server-exists", "title": "The RESP server binary exists", "type": "file.exists", "path": "README.md"},
    {"id": "resp-server-pings", "title": "RESP server answers PING", "type": "cmd.exit_code", "run": ["go", "version"]},
    {"id": "resp-server-handles-commands", "title": "Server handles the COMMAND listing", "type": "cmd.exit_code", "run": ["go", "version"]}
  ]
}`

// noneModeCheckSpecFixture is a check-spec with no path/cwd reference at
// all — exactly the shape GRID_SPEC §4's seed-time rule requires for
// mode: none ("is Docker installed"-style lessons). Reuses the stub's
// fixed Redis-lesson check ids so the canned submission fixtures still
// line up.
const noneModeCheckSpecFixture = `{
  "version": 1,
  "checks": [
    {"id": "resp-server-exists", "title": "go is installed", "type": "cmd.exit_code", "run": ["go", "version"]},
    {"id": "resp-server-pings", "title": "go version prints something", "type": "cmd.exit_code", "run": ["go", "version"]},
    {"id": "resp-server-handles-commands", "title": "go env succeeds", "type": "cmd.exit_code", "run": ["go", "env"]}
  ]
}`

// failingCheckSpecFixture declares one check whose command cannot even be
// run — an ordinary measurement error (grid check's exit-1 condition),
// never a setup failure.
const failingCheckSpecFixture = `{
  "version": 1,
  "checks": [
    {"id": "a", "title": "a", "type": "cmd.exit_code", "run": ["this-command-does-not-exist-grid-test-xyz"]}
  ]
}`

// writeTestAttempt scaffolds a minimal workspace directly — no network
// round trip through `grid run` — wired to the stub's fixed attempt
// identity (stub.AttemptID) so handleSubmissions' nonce/attemptPassed
// gating, keyed by those constants, accepts whatever this test submits.
func writeTestAttempt(t *testing.T, dir, nonce, checkSpecJSON string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test workspace\n"), 0o644); err != nil {
		t.Fatalf("seed README.md: %v", err)
	}
	af := workspace.AttemptFile{
		AttemptID:  stub.AttemptID,
		Nonce:      nonce,
		ExpiresAt:  time.Now().Add(time.Hour).UTC(),
		Lesson:     api.Lesson{ID: stub.LessonID, Title: "Speak RESP", CourseSlug: stub.CourseSlug, LessonSlug: stub.LessonSlug},
		Spec:       json.RawMessage(checkSpecJSON),
		DirPath:    dir,
		CreatedAt:  time.Now().UTC(),
		CLIVersion: "test",
	}
	if err := workspace.WriteAttemptFile(dir, af); err != nil {
		t.Fatalf("WriteAttemptFile: %v", err)
	}
}

// writeTestNoneAttempt persists a workspace.mode: none attempt directly
// into the CLI's state directory (workspace.StateDir) — no network round
// trip through `grid run` — wired to the stub's fixed attempt identity,
// same as writeTestAttempt, for tests driving grid check/grid status/grid
// submit/grid open against a mode: none attempt. Callers must isolateHome
// first, same as any test exercising FindAttempt's state-directory
// fallback.
func writeTestNoneAttempt(t *testing.T, nonce, checkSpecJSON string) workspace.AttemptFile {
	t.Helper()
	dir, err := workspace.StateDir(stub.LessonID)
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	af := workspace.AttemptFile{
		AttemptID:  stub.AttemptID,
		Nonce:      nonce,
		ExpiresAt:  time.Now().Add(time.Hour).UTC(),
		Lesson:     api.Lesson{ID: stub.LessonID, Title: "Speak RESP", CourseSlug: stub.CourseSlug, LessonSlug: stub.LessonSlug},
		Spec:       json.RawMessage(checkSpecJSON),
		DirPath:    dir,
		CreatedAt:  time.Now().UTC(),
		CLIVersion: "test",
		Mode:       string(workspace.ModeNone),
	}
	if err := workspace.WriteAttemptFile(dir, af); err != nil {
		t.Fatalf("WriteAttemptFile (none-mode state dir): %v", err)
	}
	return af
}

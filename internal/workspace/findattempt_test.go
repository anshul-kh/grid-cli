package workspace

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anshul-kh/grid-cli/internal/api"
)

// isolateState points the CLI's config/state directory at a temp dir, so
// FindAttempt's state-directory fallback never touches the real developer
// machine's ~/.config/grid (or platform equivalent).
func isolateState(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
}

func testAttempt(lessonID string) api.AttemptResponse {
	return api.AttemptResponse{
		Attempt:   api.Attempt{ID: "attempt-1", Nonce: "nonce-1", ExpiresAt: time.Now().Add(time.Hour).UTC()},
		Lesson:    api.Lesson{ID: lessonID, Title: "Is Docker installed?", CourseSlug: "c", LessonSlug: "l"},
		CheckSpec: json.RawMessage(`{"version":1,"checks":[]}`),
	}
}

func TestFindAttemptPrefersLocalOverState(t *testing.T) {
	isolateState(t)
	dir := t.TempDir()
	local := NewAttemptFile(testAttempt("lesson-local"), ModeFolder, dir, "test", time.Now().UTC())
	if err := WriteAttemptFile(dir, local); err != nil {
		t.Fatalf("WriteAttemptFile: %v", err)
	}

	stateDir, err := StateDir("lesson-state")
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	state := NewAttemptFile(testAttempt("lesson-state"), ModeNone, stateDir, "test", time.Now().UTC())
	if err := WriteAttemptFile(stateDir, state); err != nil {
		t.Fatalf("WriteAttemptFile (state): %v", err)
	}

	got, err := FindAttempt(dir)
	if err != nil {
		t.Fatalf("FindAttempt: %v", err)
	}
	if got.Lesson.ID != "lesson-local" {
		t.Errorf("FindAttempt(dir) found %q, want the local attempt to win over the state one", got.Lesson.ID)
	}
}

// TestFindAttemptFallsBackToStateDirFromAnywhere proves the none-mode
// headline: `grid run` creates zero entries in the invocation directory,
// and the attempt it wrote under the state directory is still discoverable
// by FindAttempt from a totally unrelated cwd.
func TestFindAttemptFallsBackToStateDirFromAnywhere(t *testing.T) {
	isolateState(t)
	scaffoldTarget := t.TempDir()

	stateDir, err := StateDir("lesson-none")
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	af := NewAttemptFile(testAttempt("lesson-none"), ModeNone, stateDir, "test", time.Now().UTC())
	if err := WriteAttemptFile(stateDir, af); err != nil {
		t.Fatalf("WriteAttemptFile: %v", err)
	}

	// The headline behaviour this pass exists for: nothing was ever written
	// into the directory `grid run` was invoked against.
	entries, err := os.ReadDir(scaffoldTarget)
	if err != nil {
		t.Fatalf("ReadDir(scaffoldTarget): %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("scaffold target has %d entries, want 0: %v", len(entries), entries)
	}

	unrelatedCwd := t.TempDir()
	got, err := FindAttempt(unrelatedCwd)
	if err != nil {
		t.Fatalf("FindAttempt from an unrelated cwd: %v", err)
	}
	if got.Lesson.ID != "lesson-none" {
		t.Errorf("FindAttempt = %+v, want the state-dir attempt for lesson-none", got)
	}
	if got.Mode != string(ModeNone) {
		t.Errorf("FindAttempt mode = %q, want %q", got.Mode, ModeNone)
	}
}

// TestFindAttemptPicksMostRecentStateAttempt proves the "most recent wins"
// tie-break when more than one mode: none lesson has an open attempt and
// the caller gives FindAttempt no lesson id to disambiguate.
func TestFindAttemptPicksMostRecentStateAttempt(t *testing.T) {
	isolateState(t)

	olderDir, err := StateDir("lesson-older")
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	if err := WriteAttemptFile(olderDir, NewAttemptFile(testAttempt("lesson-older"), ModeNone, olderDir, "test", time.Now().UTC())); err != nil {
		t.Fatalf("WriteAttemptFile (older): %v", err)
	}
	// Force a distinguishable, strictly later mtime than whatever the first
	// write landed at (filesystem mtime resolution can be coarse).
	time.Sleep(10 * time.Millisecond)

	newerDir, err := StateDir("lesson-newer")
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	if err := WriteAttemptFile(newerDir, NewAttemptFile(testAttempt("lesson-newer"), ModeNone, newerDir, "test", time.Now().UTC())); err != nil {
		t.Fatalf("WriteAttemptFile (newer): %v", err)
	}

	got, err := FindAttempt(t.TempDir())
	if err != nil {
		t.Fatalf("FindAttempt: %v", err)
	}
	if got.Lesson.ID != "lesson-newer" {
		t.Errorf("FindAttempt = %+v, want the most recently written state attempt (lesson-newer)", got)
	}
}

// TestFindAttemptNoneAnywhereReportsNotExist proves the "nothing to find at
// all" case reports os.ErrNotExist, same as LoadAttemptFile, so every
// caller keeps one not-found branch.
func TestFindAttemptNoneAnywhereReportsNotExist(t *testing.T) {
	isolateState(t)
	_, err := FindAttempt(t.TempDir())
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("FindAttempt with nothing anywhere: err = %v, want os.ErrNotExist", err)
	}
}

func TestStateDirIsKeyedByLessonID(t *testing.T) {
	isolateState(t)
	a, err := StateDir("lesson-a")
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	b, err := StateDir("lesson-b")
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	if a == b {
		t.Errorf("StateDir for two different lesson ids collided: %q", a)
	}
	if filepath.Base(a) != "lesson-a" || filepath.Base(b) != "lesson-b" {
		t.Errorf("StateDir should end in the lesson id: %q / %q", a, b)
	}
}

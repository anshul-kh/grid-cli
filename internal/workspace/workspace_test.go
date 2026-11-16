package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anshul-kh/grid-cli/internal/api"
)

func TestIsValidLessonID(t *testing.T) {
	cases := []struct {
		id    string
		valid bool
	}{
		{"f2908a1e-6b6c-4e4d-9f6e-8b0a0d9a3f21", true},
		{"F2908A1E-6B6C-4E4D-9F6E-8B0A0D9A3F21", true}, // case-insensitive
		{"not-a-uuid", false},
		{"", false},
		{"f2908a1e-6b6c-4e4d-9f6e-8b0a0d9a3f2", false}, // one hex digit short
		{"f2908a1e6b6c4e4d9f6e8b0a0d9a3f21", false},    // missing hyphens
		{"../../../etc/passwd", false},
		{"f2908a1e-6b6c-4e4d-9f6e-8b0a0d9a3f21x", false}, // trailing garbage
	}
	for _, c := range cases {
		if got := IsValidLessonID(c.id); got != c.valid {
			t.Errorf("IsValidLessonID(%q) = %v, want %v", c.id, got, c.valid)
		}
	}
}

func TestDefaultDirName(t *testing.T) {
	if got, want := DefaultDirName("build-a-redis-core-in-go", "02-speak-resp"), "grid-build-a-redis-core-in-go-02-speak-resp"; got != want {
		t.Errorf("DefaultDirName = %q, want %q", got, want)
	}
}

func TestResolveDir(t *testing.T) {
	base := t.TempDir()

	dir, err := ResolveDir(base, "my-lesson", false)
	if err != nil {
		t.Fatalf("ResolveDir: %v", err)
	}
	if want := filepath.Join(base, "my-lesson"); dir != want {
		t.Errorf("ResolveDir (not here) = %q, want %q", dir, want)
	}

	dir, err = ResolveDir(base, "my-lesson", true)
	if err != nil {
		t.Fatalf("ResolveDir --here: %v", err)
	}
	if dir != base {
		t.Errorf("ResolveDir --here = %q, want %q", dir, base)
	}
}

// TestResolveDirRejectsEscapingDirName proves the fix for the path-traversal
// defect found in verification: a server-controlled dirName (GRID_SPEC
// §5.2 workspace.dirName) containing ".." components, or an absolute path,
// must never resolve outside baseDir.
func TestResolveDirRejectsEscapingDirName(t *testing.T) {
	// A nested base so an escape would land somewhere clearly outside it.
	root := t.TempDir()
	base := filepath.Join(root, "depth1", "depth2", "depth3")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir base: %v", err)
	}

	cases := []string{
		"../../../escape",
		"..",
		"/etc/escape",
	}
	for _, dirName := range cases {
		dir, err := ResolveDir(base, dirName, false)
		if err == nil {
			t.Errorf("ResolveDir(base, %q, false) = %q, <nil>; want an error", dirName, dir)
			continue
		}
		if !strings.Contains(err.Error(), "escape") && !strings.Contains(err.Error(), "absolute") {
			t.Errorf("ResolveDir(base, %q, false) error should explain the rejection, got: %v", dirName, err)
		}
	}
}

func TestEnsureWritable(t *testing.T) {
	t.Run("missing directory is writable", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "does-not-exist-yet")
		if err := EnsureWritable(dir, false); err != nil {
			t.Errorf("EnsureWritable on a missing dir: %v", err)
		}
	})

	t.Run("empty directory is writable", func(t *testing.T) {
		dir := t.TempDir()
		if err := EnsureWritable(dir, false); err != nil {
			t.Errorf("EnsureWritable on an empty dir: %v", err)
		}
	})

	t.Run("non-empty directory is refused without --force and left untouched", func(t *testing.T) {
		dir := t.TempDir()
		existing := filepath.Join(dir, "keep-me.txt")
		if err := os.WriteFile(existing, []byte("original\n"), 0o644); err != nil {
			t.Fatalf("seed file: %v", err)
		}

		err := EnsureWritable(dir, false)
		if err == nil {
			t.Fatal("EnsureWritable on a non-empty dir without --force: want error, got nil")
		}
		if !strings.Contains(err.Error(), "--force") {
			t.Errorf("error should mention --force: %v", err)
		}

		// Nothing in the directory moved.
		got, err := os.ReadFile(existing)
		if err != nil {
			t.Fatalf("re-reading existing file: %v", err)
		}
		if string(got) != "original\n" {
			t.Errorf("existing file content changed: %q", got)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		if len(entries) != 1 {
			t.Errorf("directory entry count changed: got %d entries, want 1", len(entries))
		}
	})

	t.Run("non-empty directory is allowed with --force", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "stale.txt"), []byte("old\n"), 0o644); err != nil {
			t.Fatalf("seed file: %v", err)
		}
		if err := EnsureWritable(dir, true); err != nil {
			t.Errorf("EnsureWritable with --force: %v", err)
		}
	})
}

func TestWriteFiles(t *testing.T) {
	dir := t.TempDir()
	files := []api.WorkspaceFile{
		{Path: "README.md", Contents: "line one\r\nline two\rline three\n"},
		{Path: "nested/dir/go.mod", Contents: "module x\n"},
	}
	if err := WriteFiles(dir, files); err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("reading README.md: %v", err)
	}
	if want := "line one\nline two\nline three\n"; string(got) != want {
		t.Errorf("README.md content = %q, want %q (CRLF/CR must fold to LF)", got, want)
	}
	if strings.Contains(string(got), "\r") {
		t.Errorf("README.md still contains a carriage return: %q", got)
	}

	got, err = os.ReadFile(filepath.Join(dir, "nested", "dir", "go.mod"))
	if err != nil {
		t.Fatalf("reading nested/dir/go.mod: %v", err)
	}
	if string(got) != "module x\n" {
		t.Errorf("nested file content = %q", got)
	}
}

func TestWriteFilesRejectsEscapingPaths(t *testing.T) {
	dir := t.TempDir()

	cases := []api.WorkspaceFile{
		{Path: "../escape.txt", Contents: "x"},
		{Path: "/etc/escape.txt", Contents: "x"},
	}
	for _, f := range cases {
		if err := WriteFiles(dir, []api.WorkspaceFile{f}); err == nil {
			t.Errorf("WriteFiles(%q): want error, got nil", f.Path)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("escaping paths left entries behind in the workspace: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.txt")); err == nil {
		t.Error("WriteFiles wrote outside the workspace directory")
	}
}

// TestAttemptFileRoundTripHasNoSessionKey proves the .grid/attempt.json
// format carries exactly the plan's fields and never a session key — the
// AttemptFile struct has no such field, but this is the tripwire that fails
// loudly if one is ever added carelessly.
func TestAttemptFileRoundTripHasNoSessionKey(t *testing.T) {
	dir := t.TempDir()
	att := api.AttemptResponse{
		Attempt:   api.Attempt{ID: "attempt-1", Nonce: "nonce-1", ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)},
		Lesson:    api.Lesson{ID: "lesson-1", Title: "Speak RESP", CourseSlug: "redis", LessonSlug: "speak-resp", Difficulty: "intermediate", EstimatedMinutes: 30},
		CheckSpec: json.RawMessage(`{"version":1,"checks":[]}`),
		URLs:      api.LessonURLs{LessonURL: "http://localhost:3000/x"},
	}
	af := NewAttemptFile(att, ModeFolder, dir, "dev-test", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err := WriteAttemptFile(dir, af); err != nil {
		t.Fatalf("WriteAttemptFile: %v", err)
	}

	raw, err := os.ReadFile(AttemptFilePath(dir))
	if err != nil {
		t.Fatalf("reading attempt.json: %v", err)
	}
	const canarySessionKey = "super-secret-session-key-should-never-appear"
	if strings.Contains(string(raw), "sessionKey") || strings.Contains(string(raw), canarySessionKey) {
		t.Errorf(".grid/attempt.json leaks a session key: %s", raw)
	}

	for _, field := range []string{`"attemptId"`, `"nonce"`, `"expiresAt"`, `"lesson"`, `"spec"`, `"dirPath"`, `"createdAt"`, `"cliVersion"`} {
		if !strings.Contains(string(raw), field) {
			t.Errorf("attempt.json missing field %s:\n%s", field, raw)
		}
	}

	loaded, err := LoadAttemptFile(dir)
	if err != nil {
		t.Fatalf("LoadAttemptFile: %v", err)
	}
	if loaded.AttemptID != "attempt-1" || loaded.Nonce != "nonce-1" || loaded.CLIVersion != "dev-test" {
		t.Errorf("LoadAttemptFile round trip mismatch: %+v", loaded)
	}
	if loaded.Lesson.Title != "Speak RESP" {
		t.Errorf("LoadAttemptFile lesson mismatch: %+v", loaded.Lesson)
	}
	if loaded.Mode != string(ModeFolder) {
		t.Errorf("LoadAttemptFile mode = %q, want %q", loaded.Mode, ModeFolder)
	}
}

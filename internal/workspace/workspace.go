// Package workspace scaffolds a lesson's local workspace (GRID_SPEC §5.2):
// resolving the target directory, guarding against clobbering an existing
// one, writing the server's starter files with normalised line endings,
// and persisting `.grid/attempt.json` — the CLI-local record of the open
// attempt that `grid check`/`grid submit`/`grid status` read back (pass 7+).
//
// This file owns directory resolution, the non-empty-dir guard, starter
// file writes, and the attempt.json format. Starter-repo cloning lives in
// gitclone.go; the post-scaffold file fingerprint lives in fingerprint.go.
package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/anshul-kh/grid-cli/internal/api"
)

// uuidPattern matches a canonical 8-4-4-4-12 hex UUID of any version/variant
// (case-insensitive) — GRID_SPEC never pins lesson ids to a specific UUID
// version, so this only rejects obviously malformed ids, which is all
// `grid run` needs before it is safe to spend a network round trip.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsValidLessonID reports whether id is a syntactically valid UUID. `grid
// run` checks this before any network call — an obviously bad id is a
// usage error (exit 5), not a 404 round trip.
func IsValidLessonID(id string) bool {
	return uuidPattern.MatchString(id)
}

// DefaultDirName returns the workspace directory name to use when the
// server's workspace.dirName is empty (GRID_SPEC §5.2: "default
// grid-<courseSlug>-<lessonSlug>").
func DefaultDirName(courseSlug, lessonSlug string) string {
	return "grid-" + courseSlug + "-" + lessonSlug
}

// ResolveDir computes the absolute workspace directory: baseDir itself when
// here is true, otherwise dirName joined under baseDir. baseDir is the
// resolved "current directory" (the --dir global flag, or the process cwd).
// dirName is server-controlled (GRID_SPEC §5.2 workspace.dirName) — the same
// trust boundary WriteFiles already guards for workspace.files[].path, so a
// "../escape" or absolute dirName is rejected here too, before it is ever
// joined onto baseDir.
func ResolveDir(baseDir, dirName string, here bool) (string, error) {
	target := dirName
	if here {
		target = ""
	} else if err := rejectEscapingPath(dirName); err != nil {
		return "", fmt.Errorf("workspace directory name %w", err)
	}
	dir, err := filepath.Abs(filepath.Join(baseDir, target))
	if err != nil {
		return "", fmt.Errorf("resolving workspace directory: %w", err)
	}
	// Belt-and-suspenders containment check: dir must still be baseDir
	// itself or something under it. rejectEscapingPath already catches the
	// paths that matter (absolute dirName, "../..." components), but this
	// re-derives the same guarantee from the actual joined result, so a
	// platform quirk in path handling can never silently defeat the check
	// above.
	base, err := filepath.Abs(baseDir)
	if err != nil {
		return "", fmt.Errorf("resolving working directory: %w", err)
	}
	if rel, err := filepath.Rel(base, dir); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("workspace directory name %q escapes %s", dirName, base)
	}
	return dir, nil
}

// rejectEscapingPath rejects a server-controlled relative path that would
// escape the directory it is meant to be joined under: an absolute path, or
// a path containing a ".." component. Shared by ResolveDir
// (workspace.dirName) and WriteFiles (workspace.files[].path) — GRID_SPEC
// §5.2 gives the server control of both fields, so both get the same
// treatment. An empty path is fine (ResolveDir's --here case passes "").
func rejectEscapingPath(p string) error {
	if p == "" {
		return nil
	}
	// Spec paths always use forward slashes, so a leading "/" is absolute on
	// every host. filepath.IsAbs alone misses it on Windows, where
	// "/etc/passwd" would otherwise be joined *under* the workspace and
	// written with different semantics than the server intended.
	if isAbsoluteSpecPath(p) {
		return fmt.Errorf("%q is an absolute path, which is not allowed here", p)
	}
	rel := filepath.FromSlash(p)
	if isAbsoluteSpecPath(rel) {
		return fmt.Errorf("%q is an absolute path, which is not allowed here", p)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%q escapes the directory it must stay inside", p)
	}
	return nil
}

// isAbsoluteSpecPath reports whether a spec-supplied path is absolute on any
// host. See rejectEscapingPath: a POSIX-absolute name is not absolute to
// filepath.IsAbs on Windows, and a Windows drive/UNC name is not absolute on
// unix, so both spellings are rejected everywhere for identical semantics.
func isAbsoluteSpecPath(p string) bool {
	return strings.HasPrefix(p, "/") ||
		strings.HasPrefix(p, `\`) ||
		filepath.IsAbs(p) ||
		filepath.VolumeName(p) != ""
}

// EnsureWritable guards against clobbering an existing workspace: a
// directory that does not exist yet, or exists and is empty, is always
// safe. A non-empty directory is refused unless force is set. It never
// creates, removes, or modifies anything — callers only proceed to write
// once this returns nil.
func EnsureWritable(dir string, force bool) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking workspace directory %s: %w", dir, err)
	}
	if len(entries) == 0 || force {
		return nil
	}
	return fmt.Errorf(
		"directory %s is not empty — pass --force to overwrite it, or remove it and run `grid run` again",
		dir,
	)
}

// WriteFiles writes the server's starter files (GRID_SPEC §5.2
// workspace.files[]) under dir. Spec paths use forward slashes and are
// converted with filepath.FromSlash (never hand-concatenated); contents are
// written with \n line endings only, whatever the server sent (TASK.md §5).
// A path that escapes dir (absolute, or containing a ".." component) is
// rejected — the server is trusted for lesson content, not for writing
// outside the workspace it was asked to scaffold.
func WriteFiles(dir string, files []api.WorkspaceFile) error {
	for _, f := range files {
		if err := rejectEscapingPath(f.Path); err != nil {
			return fmt.Errorf("starter file %q: %w", f.Path, err)
		}
		dest := filepath.Join(dir, filepath.Clean(filepath.FromSlash(f.Path)))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("creating directory for %s: %w", f.Path, err)
		}
		if err := os.WriteFile(dest, []byte(toLF(f.Contents)), 0o644); err != nil {
			return fmt.Errorf("writing starter file %s: %w", f.Path, err)
		}
	}
	return nil
}

// toLF folds CRLF and bare CR to LF. This is deliberately narrower than
// runner.NormalizeOutput (which also strips trailing whitespace and trims
// trailing newlines for measurement comparison) — starter file contents
// must be written faithfully, only their line endings normalised.
func toLF(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}

// AttemptFile is the on-disk shape of .grid/attempt.json: the single-use
// attempt plus enough of the lesson and its public check-spec for
// `grid check`/`grid submit`/`grid status` to act without a prior
// `grid run`. It never carries the session key — that lives only in the
// credential store (internal/auth) and is never written into a workspace.
type AttemptFile struct {
	AttemptID  string          `json:"attemptId"`
	Nonce      string          `json:"nonce"`
	ExpiresAt  time.Time       `json:"expiresAt"`
	Lesson     api.Lesson      `json:"lesson"`
	Spec       json.RawMessage `json:"spec"`
	DirPath    string          `json:"dirPath"`
	CreatedAt  time.Time       `json:"createdAt"`
	CLIVersion string          `json:"cliVersion"`
	// LessonURL is the human-facing lesson page (api.LessonURLs.LessonURL),
	// persisted so `grid open`/`grid status` can use it without a network
	// round trip (pass 8: both read attempt.json only).
	LessonURL string `json:"lessonUrl,omitempty"`
	// Mode is the resolved workspace.mode this attempt was scaffolded under
	// (GRID_SPEC §4) — the canonical ParseMode result, not the server's raw
	// string. FindAttempt's callers use it to tell a mode: none attempt
	// (DirPath is a pseudo-directory under the CLI's own state dir, never a
	// real project the checks should treat as their cwd) apart from every
	// other mode (DirPath is the real scaffolded workspace). Empty on an
	// attempt.json written before this field existed, which ParseMode reads
	// back as ModeFolder — correct, since every pre-P18 workspace was one.
	Mode string `json:"mode,omitempty"`
}

// NewAttemptFile builds the attempt.json payload from a fetched attempt
// response, the resolved workspace mode and directory, and the running
// CLI's version.
func NewAttemptFile(att api.AttemptResponse, mode Mode, dirPath, cliVersion string, createdAt time.Time) AttemptFile {
	return AttemptFile{
		AttemptID:  att.Attempt.ID,
		Nonce:      att.Attempt.Nonce,
		ExpiresAt:  att.Attempt.ExpiresAt,
		Lesson:     att.Lesson,
		Spec:       att.CheckSpec,
		DirPath:    dirPath,
		CreatedAt:  createdAt,
		CLIVersion: cliVersion,
		LessonURL:  att.URLs.LessonURL,
		Mode:       string(mode),
	}
}

// gridDir is the metadata directory's name inside a workspace.
const gridDir = ".grid"

// AttemptFilePath returns the .grid/attempt.json path under a workspace
// directory.
func AttemptFilePath(dir string) string {
	return filepath.Join(dir, gridDir, "attempt.json")
}

// WriteAttemptFile writes .grid/attempt.json under dir, creating the .grid
// directory if needed.
func WriteAttemptFile(dir string, af AttemptFile) error {
	return writeJSONFile(AttemptFilePath(dir), af)
}

// UpdateNonce rewrites .grid/attempt.json's nonce field after a submission
// response rotates it (GRID_SPEC §5.4, GRID_SYNC_CLAUDE.md 2026-11-12
// 01:05): the attempt survives a non-passing submission, but its nonce is
// single-use, so `grid submit`'s next retry must use the value the server
// returned as nextNonce. Skipping this is the single most common way to
// make a learner's second `grid submit` fail with 409 nonce_already_used,
// which looks like a server bug.
func UpdateNonce(dir, nonce string) error {
	af, err := LoadAttemptFile(dir)
	if err != nil {
		return err
	}
	af.Nonce = nonce
	return WriteAttemptFile(dir, af)
}

// LoadAttemptFile reads back .grid/attempt.json from a workspace directory.
func LoadAttemptFile(dir string) (AttemptFile, error) {
	var af AttemptFile
	err := readJSONFile(AttemptFilePath(dir), &af)
	return af, err
}

// Package workspace — this file (findattempt.go) owns the lookup every
// attempt-reading command (`grid check`, `grid status`, `grid submit`,
// `grid open`) shares: find the open attempt without requiring a lesson id
// on the command line. A mode: none lesson (GRID_SPEC §4) has no workspace
// directory to hold a .grid folder, so `grid run` persists its
// attempt.json under the CLI's own state directory instead
// (internal/config.AttemptsDir) — StateDir computes exactly where, reusing
// workspace.go's existing AttemptFilePath/WriteAttemptFile/LoadAttemptFile
// unchanged by just pointing them at that directory instead of a real
// workspace.
package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/anshul-kh/grid-cli/internal/config"
)

// StateDir returns the per-lesson pseudo-workspace directory inside the
// CLI's own state directory, keyed by lesson id. Used only for
// workspace.mode: none lessons — the only mode that creates no directory
// in the invocation directory. Its shape mirrors a real workspace (a
// .grid/ folder holding attempt.json), so every existing helper that takes
// a workspace dir (WriteAttemptFile, LoadAttemptFile, UpdateNonce,
// WriteLastRun, WriteLastVerdict) works against it unchanged.
func StateDir(lessonID string) (string, error) {
	root, err := config.AttemptsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, lessonID), nil
}

// FindAttempt resolves the attempt record `grid check`/`grid status`/
// `grid submit`/`grid open` should act on, without a lesson id argument: a
// local workspace's .grid/attempt.json under dir when one exists (every
// mode except none scaffolds this), otherwise the most recently written
// attempt in the CLI's state directory — where a mode: none `grid run`
// persisted it instead (StateDir), since it never created a workspace
// directory. Reports os.ErrNotExist (via errors.Is), same as
// LoadAttemptFile, when neither has anything, so every caller keeps one
// not-found branch.
func FindAttempt(dir string) (AttemptFile, error) {
	af, err := LoadAttemptFile(dir)
	if err == nil {
		return af, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return AttemptFile{}, err
	}
	return loadLatestStateAttempt()
}

// loadLatestStateAttempt is FindAttempt's state-directory fallback. "Most
// recent" is deliberate: grid check/grid status/grid submit/grid open take
// no lesson id, so when more than one mode: none lesson has an open
// attempt, the one `grid run` most recently created (or `grid submit` most
// recently rotated the nonce on) is the one the user almost certainly
// means.
func loadLatestStateAttempt() (AttemptFile, error) {
	root, err := config.AttemptsDir()
	if err != nil {
		return AttemptFile{}, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return AttemptFile{}, os.ErrNotExist
		}
		return AttemptFile{}, err
	}

	var latestDir string
	var latestModTime time.Time
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(root, e.Name())
		info, err := os.Stat(AttemptFilePath(candidate))
		if err != nil {
			continue // no attempt.json in this lesson's state dir — skip it.
		}
		if latestDir == "" || info.ModTime().After(latestModTime) {
			latestDir, latestModTime = candidate, info.ModTime()
		}
	}
	if latestDir == "" {
		return AttemptFile{}, os.ErrNotExist
	}
	return LoadAttemptFile(latestDir)
}

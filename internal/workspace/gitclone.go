package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrGitUnavailable is returned by CloneStarter when the system `git`
// binary is not on PATH. The plan deliberately has no go-git dependency
// (TASK.md §2 prefers shelling out to the system binary); callers degrade
// gracefully — warn and continue with workspace.files[] alone — rather
// than failing the whole `grid run` over a missing optional tool.
var ErrGitUnavailable = errors.New("git is not installed")

// ErrSubdirEscapesRepo is returned by CloneStarter when
// workspace.starterSubdir — server-controlled, GRID_SPEC §5.2 — would
// resolve outside the cloned repo (an absolute path, or one containing a
// ".." component). The same trust boundary WriteFiles and ResolveDir
// already enforce for workspace.files[].path and workspace.dirName applies
// here too: a malicious or buggy starterSubdir must never let CloneStarter
// copy an arbitrary local file into the new workspace.
var ErrSubdirEscapesRepo = errors.New("starter repo subdir escapes the clone")

// ErrStarterRepoContainsSymlink is returned by CloneStarter when the cloned
// repository's own committed content contains a symlink, whether as the
// starterSubdir path itself or anywhere inside the tree that would be
// copied. GRID_SPEC §4 cross-platform rule 5 already says "no check may
// depend on ... symlinks" — refusing them in a scaffolded workspace is not
// a new restriction on legitimate lesson content, it closes the gap: a
// symlink committed to a server-controlled starterRepoUrl can point
// anywhere on the local filesystem, and following it would copy (or, via
// Fingerprint's os.Open, hash) an arbitrary local file's content into the
// new workspace.
var ErrStarterRepoContainsSymlink = errors.New("starter repo contains a symlink")

// CloneStarter shallow-clones repoURL into dir (GRID_SPEC §5.2
// workspace.starterRepoUrl). The repo is always cloned into a scratch
// staging directory first, never straight into dir: that staging copy is
// validated — the resolved subdir (if any) must stay inside the clone, and
// nothing in the copied tree may be a symlink — before a single byte is
// written into dir. Only once validation passes does the selected tree
// (the whole repo, or just starterSubdir) get copied into dir. This
// ordering is what makes rejection atomic: a rejected clone never leaves
// partial or unsafe content sitting in the caller's workspace directory.
//
// The whole-repo copy preserves `.git` (lessons may check git history with
// git.has_commit); the subdir copy does not carry `.git` across, since a
// subdirectory never legitimately contains the repo's own `.git`.
func CloneStarter(ctx context.Context, dir, repoURL, subdir string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("%w: install git, or re-run and let `grid run` scaffold the starter files on their own", ErrGitUnavailable)
	}

	staging, err := os.MkdirTemp(filepath.Dir(dir), ".grid-clone-*")
	if err != nil {
		return fmt.Errorf("creating staging directory for starter repo clone: %w", err)
	}
	defer os.RemoveAll(staging)

	if err := gitClone(ctx, repoURL, staging); err != nil {
		return err
	}

	src, skipGit := staging, false
	if subdir != "" {
		if err := rejectEscapingPath(subdir); err != nil {
			return fmt.Errorf("%w: %s", ErrSubdirEscapesRepo, err)
		}
		candidate := filepath.Join(staging, filepath.FromSlash(subdir))
		// Belt-and-suspenders containment check, the same reasoning as
		// ResolveDir's: re-derive containment from the actual joined path
		// rather than trusting the pre-check alone.
		if rel, err := filepath.Rel(staging, candidate); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%w: %q", ErrSubdirEscapesRepo, subdir)
		}
		// Lstat, not Stat: the subdir path itself must not be a symlink —
		// following it before the directory check would be exactly the
		// bug this fix closes, just one level earlier.
		info, err := os.Lstat(candidate)
		if err != nil {
			return fmt.Errorf("starter repo subdir %q not found after clone: %w", subdir, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: starter repo subdir %q is itself a symlink", ErrStarterRepoContainsSymlink, subdir)
		}
		if !info.IsDir() {
			return fmt.Errorf("starter repo subdir %q is not a directory", subdir)
		}
		src, skipGit = candidate, true
	}

	if err := rejectSymlinks(src); err != nil {
		return err
	}
	return copyTree(src, dir, skipGit)
}

// gitClone runs `git clone --depth 1 <url> <dest>`, bounded by ctx so a
// command-level cancellation (Ctrl-C, the request timeout) stops the clone
// too.
func gitClone(ctx context.Context, url, dest string) error {
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", url, dest)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone %s: %w: %s", url, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// rejectSymlinks walks root and fails on the first symlink found anywhere
// in the tree (root itself excluded — the caller already Lstat-checked it
// where that matters). It copies nothing; CloneStarter only proceeds to the
// real copy once this reports the tree clean, so a rejection never leaves
// partial content behind.
func rejectSymlinks(root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			return fmt.Errorf("%w: %s", ErrStarterRepoContainsSymlink, rel)
		}
		return nil
	})
}

// copyTree copies every file under src into dst, preserving relative
// paths. When skipGit is set, a `.git` entry is skipped (the subdir-copy
// path: a subdirectory never legitimately carries the repo's own `.git`
// out with it); the whole-repo path keeps `.git` so git.has_commit checks
// still work. rejectSymlinks has already proven src contains no symlink by
// the time this runs — this function does not need to recheck.
func copyTree(src, dst string, skipGit bool) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if info.IsDir() {
			if skipGit && info.Name() == ".git" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		return copyFile(path, filepath.Join(dst, rel), info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

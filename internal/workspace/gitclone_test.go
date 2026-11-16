package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// localRepo creates a tiny local git repo (a valid clone source — `git
// clone` works over a filesystem path) with one commit and, optionally, a
// subdirectory. Returns the repo path. Skips the test if git is not on the
// machine running the suite.
func localRepo(t *testing.T, withSubdir bool) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed, skipping starter-repo clone test")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=grid-cli-test", "GIT_AUTHOR_EMAIL=test@grid.dev",
			"GIT_COMMITTER_NAME=grid-cli-test", "GIT_COMMITTER_EMAIL=test@grid.dev",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "root.txt"), []byte("root\n"), 0o644); err != nil {
		t.Fatalf("seed root.txt: %v", err)
	}
	if withSubdir {
		sub := filepath.Join(dir, "starter")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatalf("mkdir starter: %v", err)
		}
		if err := os.WriteFile(filepath.Join(sub, "go.mod"), []byte("module starter\n"), 0o644); err != nil {
			t.Fatalf("seed starter/go.mod: %v", err)
		}
	}
	run("add", "-A")
	run("commit", "-q", "-m", "initial")
	return dir
}

func TestCloneStarterWholeRepo(t *testing.T) {
	repo := localRepo(t, false)
	dest := filepath.Join(t.TempDir(), "workspace")

	if err := CloneStarter(context.Background(), dest, repo, ""); err != nil {
		t.Fatalf("CloneStarter: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "root.txt")); err != nil {
		t.Errorf("cloned repo missing root.txt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".git")); err != nil {
		t.Errorf("whole-repo clone should keep .git (git.has_commit checks may need it): %v", err)
	}
}

func TestCloneStarterSubdir(t *testing.T) {
	repo := localRepo(t, true)
	dest := filepath.Join(t.TempDir(), "workspace")

	if err := CloneStarter(context.Background(), dest, repo, "starter"); err != nil {
		t.Fatalf("CloneStarter with subdir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "go.mod")); err != nil {
		t.Errorf("subdir clone missing go.mod: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "root.txt")); err == nil {
		t.Error("subdir clone leaked a file from outside the subdir")
	}
	if _, err := os.Stat(filepath.Join(dest, ".git")); err == nil {
		t.Error("subdir clone should not carry the repo's .git directory")
	}
}

// TestCloneStarterRejectsEscapingSubdir proves the fix for the path-
// traversal defect found in verification: a server-controlled subdir
// (GRID_SPEC §5.2 workspace.starterSubdir) that would resolve outside the
// clone's staging directory must never let CloneStarter copy anything from
// outside the clone into the new workspace — in particular, it must never
// reach an arbitrary file elsewhere on the local filesystem.
func TestCloneStarterRejectsEscapingSubdir(t *testing.T) {
	repo := localRepo(t, false)

	cases := []string{
		"../escape",
		"..",
		"/etc",
		"../../../../../../../../etc",
	}
	for _, subdir := range cases {
		dest := filepath.Join(t.TempDir(), "workspace")
		err := CloneStarter(context.Background(), dest, repo, subdir)
		if !errors.Is(err, ErrSubdirEscapesRepo) {
			t.Errorf("CloneStarter with subdir %q: err = %v, want ErrSubdirEscapesRepo", subdir, err)
		}
		entries, readErr := os.ReadDir(dest)
		if readErr == nil && len(entries) > 0 {
			t.Errorf("CloneStarter with subdir %q wrote into dest despite rejecting the subdir: %v", subdir, entries)
		}
	}
}

// localRepoWithSymlink creates a tiny local git repo with one committed
// regular file and one committed symlink named linkName pointing at
// target — which may be, and in these tests is, outside the repo entirely.
// That is the point: a cloned repo's own committed content can point
// anywhere the filesystem allows. When subdir is non-empty, the regular
// file and the symlink are placed inside that subdirectory instead of the
// repo root. Skips the test if git, or this host's ability to create a
// symlink, isn't available (e.g. Windows without Developer Mode/elevation)
// — the defence this proves is identical on every platform; what differs
// is only whether this host can construct the fixture.
func localRepoWithSymlink(t *testing.T, linkName, target, subdir string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed, skipping starter-repo symlink test")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=grid-cli-test", "GIT_AUTHOR_EMAIL=test@grid.dev",
			"GIT_COMMITTER_NAME=grid-cli-test", "GIT_COMMITTER_EMAIL=test@grid.dev",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "core.symlinks", "true")

	content := dir
	if subdir != "" {
		content = filepath.Join(dir, subdir)
		if err := os.MkdirAll(content, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", subdir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(content, "go.mod"), []byte("module starter\n"), 0o644); err != nil {
		t.Fatalf("seed go.mod: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(content, linkName)); err != nil {
		t.Skipf("cannot create symlinks on this host: %v", err)
	}

	run("add", "-A")
	run("commit", "-q", "-m", "initial")
	return dir
}

// assertNoContent fails the test if dest has any entries — used to prove a
// rejected clone leaves nothing behind, not even a partial copy.
func assertNoContent(t *testing.T, dest string) {
	t.Helper()
	entries, err := os.ReadDir(dest)
	if err == nil && len(entries) > 0 {
		t.Errorf("CloneStarter left content in %s despite rejecting the clone: %v", dest, entries)
	}
}

// TestCloneStarterRejectsSymlinkInWholeRepo proves fix point 1/2 from the
// attempt-2 verdict: a committed symlink anywhere in a whole-repo clone
// (no starterSubdir) is refused, atomically — nothing from the clone lands
// in dest, and in particular the symlink itself (which would otherwise let
// Fingerprint's os.Open hash an arbitrary local file) never does either.
func TestCloneStarterRejectsSymlinkInWholeRepo(t *testing.T) {
	secretDir := t.TempDir()
	secretPath := filepath.Join(secretDir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("TOP-SECRET-CANARY\n"), 0o644); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	repo := localRepoWithSymlink(t, "server", secretPath, "")
	dest := filepath.Join(t.TempDir(), "workspace")

	err := CloneStarter(context.Background(), dest, repo, "")
	if !errors.Is(err, ErrStarterRepoContainsSymlink) {
		t.Fatalf("CloneStarter on a repo with a committed symlink: err = %v, want ErrStarterRepoContainsSymlink", err)
	}
	assertNoContent(t, dest)
	if _, statErr := os.Lstat(filepath.Join(dest, "server")); statErr == nil {
		t.Error("CloneStarter copied the symlink into the workspace")
	}
}

// TestCloneStarterRejectsSymlinkInSubdir proves the same defence for the
// starterSubdir path: a symlink nested inside an otherwise-legitimate
// subdirectory (one that passes every escaping-path check this pass added)
// must still be refused before copyTree ever dereferences it into a
// regular file carrying the target's bytes.
func TestCloneStarterRejectsSymlinkInSubdir(t *testing.T) {
	secretDir := t.TempDir()
	secretPath := filepath.Join(secretDir, "id_rsa_secret")
	if err := os.WriteFile(secretPath, []byte("-----BEGIN PRIVATE KEY-----CANARY-----END-----\n"), 0o644); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	repo := localRepoWithSymlink(t, "id_rsa", secretPath, "starter")
	dest := filepath.Join(t.TempDir(), "workspace")

	err := CloneStarter(context.Background(), dest, repo, "starter")
	if !errors.Is(err, ErrStarterRepoContainsSymlink) {
		t.Fatalf("CloneStarter on a subdir with a committed symlink: err = %v, want ErrStarterRepoContainsSymlink", err)
	}
	assertNoContent(t, dest)
	if got, statErr := os.ReadFile(filepath.Join(dest, "id_rsa")); statErr == nil {
		t.Errorf("CloneStarter copied the secret's content into the workspace: %q", got)
	}
}

// TestCloneStarterRejectsSymlinkAsSubdirItself proves the subdir-path's own
// root is Lstat-checked, not Stat-checked: a committed symlink whose name
// matches starterSubdir itself (so the "is it a directory" check would
// otherwise follow it) is refused before any directory check runs.
func TestCloneStarterRejectsSymlinkAsSubdirItself(t *testing.T) {
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("CANARY\n"), 0o644); err != nil {
		t.Fatalf("seed outside secret: %v", err)
	}
	// A top-level symlink literally named "starter" pointing at a directory
	// outside the repo — subdir="starter" must still be refused.
	repo := localRepoWithSymlink(t, "starter", outsideDir, "")
	dest := filepath.Join(t.TempDir(), "workspace")

	err := CloneStarter(context.Background(), dest, repo, "starter")
	if !errors.Is(err, ErrStarterRepoContainsSymlink) {
		t.Fatalf("CloneStarter with subdir==a symlink: err = %v, want ErrStarterRepoContainsSymlink", err)
	}
	assertNoContent(t, dest)
}

func TestCloneStarterMissingGitIsActionable(t *testing.T) {
	// Simulate "no git on PATH" by pointing PATH at an empty directory.
	t.Setenv("PATH", t.TempDir())

	err := CloneStarter(context.Background(), t.TempDir(), "https://example.invalid/repo.git", "")
	if !errors.Is(err, ErrGitUnavailable) {
		t.Fatalf("CloneStarter with no git on PATH: err = %v, want ErrGitUnavailable", err)
	}
}

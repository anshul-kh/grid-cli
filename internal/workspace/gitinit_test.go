package workspace

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestInitRepoCreatesAGitDir proves InitRepo leaves a working git repo
// behind — skipped when git isn't on PATH (the attack-case test below
// covers that path with a stubbed exec instead).
func TestInitRepoCreatesAGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed, skipping git init test")
	}
	dir := t.TempDir()
	if err := InitRepo(dir); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("InitRepo did not create .git: %v", err)
	}
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git rev-parse --git-dir in %s: %v\n%s", dir, err, out)
	}
}

// TestInitRepoMissingGitIsActionable proves the degrade-gracefully policy's
// sentinel, the same way gitclone_test.go's TestCloneStarterMissingGitIsActionable
// does: point PATH at an empty directory so git truly cannot be found,
// without shelling out to anything real.
func TestInitRepoMissingGitIsActionable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	err := InitRepo(t.TempDir())
	if !errors.Is(err, ErrGitUnavailable) {
		t.Fatalf("InitRepo with no git on PATH: err = %v, want ErrGitUnavailable", err)
	}
}

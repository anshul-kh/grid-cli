package workspace

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// InitRepo runs `git init` inside dir (GRID_SPEC §4 workspace.mode repo/
// bundle: "directory + git init"). It shares gitclone.go's ErrGitUnavailable
// sentinel and degrade-gracefully policy: a missing git binary is not a
// `grid run` failure — the caller warns and continues with the plain
// directory, and the lesson's git.* checks will then legitimately (and
// honestly) report as not measurable.
func InitRepo(dir string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("%w: install git, or this lesson's git.* checks will fail", ErrGitUnavailable)
	}
	cmd := exec.Command("git", "init", "-q", dir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git init %s: %w: %s", dir, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

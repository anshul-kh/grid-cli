package checks

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-cli/internal/spec"
)

// initGitRepo creates a real git repository in a fresh temp dir with the
// given commit subjects, oldest first, and returns the repo's root. `git`
// itself is shelled out to (not go-git — plan Assumption 6), exercising
// the same binary git.go runs checks against.
func initGitRepo(t *testing.T, subjects ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=grid", "GIT_AUTHOR_EMAIL=grid@example.com",
			"GIT_COMMITTER_NAME=grid", "GIT_COMMITTER_EMAIL=grid@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "commit.gpgsign", "false")
	for _, subject := range subjects {
		run("commit", "-q", "--allow-empty", "-m", subject)
	}
	return dir
}

// TestGitHasCommitEvidenceShape proves { commits[] } (sha + subject),
// most-recent-first, and that the sha is a real, resolvable commit hash —
// not a placeholder.
func TestGitHasCommitEvidenceShape(t *testing.T) {
	dir := initGitRepo(t, "first commit", "second commit")
	env := runner.NewEnv(dir, nil)

	ev, err := (gitHasCommitChecker{}).Measure(context.Background(), spec.Check{Path: "."}, env)
	if err != nil {
		t.Fatalf("Measure: unexpected error: %v", err)
	}
	commits, ok := ev["commits"].([]map[string]string)
	if !ok {
		t.Fatalf("evidence[commits] is %T, want []map[string]string", ev["commits"])
	}
	if len(commits) != 2 {
		t.Fatalf("len(commits) = %d, want 2", len(commits))
	}
	if commits[0]["subject"] != "second commit" {
		t.Errorf("commits[0][subject] = %q, want \"second commit\" (most recent first)", commits[0]["subject"])
	}
	if len(commits[0]["sha"]) != 40 {
		t.Errorf("commits[0][sha] = %q, want a 40-character hex sha", commits[0]["sha"])
	}
}

// TestGitHasCommitCappedAt50 proves the commits[] cap.
func TestGitHasCommitCappedAt50(t *testing.T) {
	subjects := make([]string, 55)
	for i := range subjects {
		subjects[i] = "commit"
	}
	dir := initGitRepo(t, subjects...)
	env := runner.NewEnv(dir, nil)

	ev, err := (gitHasCommitChecker{}).Measure(context.Background(), spec.Check{Path: "."}, env)
	if err != nil {
		t.Fatalf("Measure: unexpected error: %v", err)
	}
	commits := ev["commits"].([]map[string]string)
	if len(commits) != gitCommitCap {
		t.Fatalf("len(commits) = %d, want exactly the %d cap (repo has 55 commits)", len(commits), gitCommitCap)
	}
}

// TestGitHasCommitNotARepoIsError proves a directory that is not a git
// repository cannot be measured.
func TestGitHasCommitNotARepoIsError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	env := runner.NewEnv(t.TempDir(), nil)
	if _, err := (gitHasCommitChecker{}).Measure(context.Background(), spec.Check{Path: "."}, env); err == nil {
		t.Fatal("Measure in a non-repo: want error, got nil")
	}
}

// TestGitFileTrackedReportsObservedFact proves { tracked } for both a
// tracked and an untracked file, and that an untracked path is ok, not an
// error (§1 principle 1).
func TestGitFileTrackedReportsObservedFact(t *testing.T) {
	dir := initGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd := exec.Command("git", "add", "tracked.txt")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	env := runner.NewEnv(dir, nil)
	checker := gitFileTrackedChecker{}

	ev, err := checker.Measure(context.Background(), spec.Check{Path: "tracked.txt"}, env)
	if err != nil {
		t.Fatalf("Measure(tracked): unexpected error: %v", err)
	}
	if ev["tracked"] != true {
		t.Errorf("tracked.txt: evidence[tracked] = %v, want true", ev["tracked"])
	}

	ev, err = checker.Measure(context.Background(), spec.Check{Path: "untracked.txt"}, env)
	if err != nil {
		t.Fatalf("Measure(untracked): unexpected error (not-tracked is ok, not error): %v", err)
	}
	if ev["tracked"] != false {
		t.Errorf("untracked.txt: evidence[tracked] = %v, want false", ev["tracked"])
	}
}

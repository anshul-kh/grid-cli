package checks

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-cli/internal/spec"
)

// gitLogFieldSep is a byte that never appears in a commit subject, used to
// split `git log --format` output into sha/subject pairs without guessing
// at a human-readable delimiter.
const gitLogFieldSep = "\x1f"

// gitCommitCap is §4's git.has_commit evidence cap: commits[] is capped 50.
const gitCommitCap = 50

// gitHasCommitChecker implements git.has_commit (§4): evidence is
// { commits[] } (sha + subject, capped 50). `message_matches`/`min_count`
// are judge-side and absent from the public spec — the checker shells out
// to `git log` in c.Path (default workdir) and uploads what it finds; the
// server decides whether any commit satisfies the lesson's expectation.
type gitHasCommitChecker struct{}

func (gitHasCommitChecker) Measure(ctx context.Context, c spec.Check, env *runner.Env) (runner.Evidence, error) {
	dir := env.Dir(c.Path)
	res, err := env.Run(ctx, runner.CmdSpec{
		Run: []string{"git", "log", "--format=%H" + gitLogFieldSep + "%s", "-n", fmt.Sprint(gitCommitCap)},
		Dir: dir,
	})
	if err != nil {
		return nil, fmt.Errorf("git.has_commit: %w", err)
	}

	normalized := string(runner.NormalizeOutput(res.Stdout))
	commits := []map[string]string{}
	if normalized != "" {
		for _, line := range strings.Split(normalized, "\n") {
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, gitLogFieldSep, 2)
			commit := map[string]string{"sha": parts[0]}
			if len(parts) > 1 {
				commit["subject"] = parts[1]
			}
			commits = append(commits, commit)
		}
	}
	if len(commits) > gitCommitCap {
		commits = commits[:gitCommitCap]
	}

	return runner.BuildEvidence(map[string]any{"commits": commits}), nil
}

// gitFileTrackedChecker implements git.file_tracked (§4): evidence is
// { tracked }, via `git ls-files --error-unmatch` in the workspace root.
// Exit code 1 ("pathspec did not match any tracked files") is a clean
// `tracked: false` observation, not a measurement failure; any other
// failure (not a git repository, git missing) is.
type gitFileTrackedChecker struct{}

func (gitFileTrackedChecker) Measure(ctx context.Context, c spec.Check, env *runner.Env) (runner.Evidence, error) {
	_, err := env.Run(ctx, runner.CmdSpec{
		Run: []string{"git", "ls-files", "--error-unmatch", "--", c.Path},
		Dir: env.Workdir,
	})
	if err == nil {
		return runner.BuildEvidence(map[string]any{"tracked": true}), nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return runner.BuildEvidence(map[string]any{"tracked": false}), nil
	}
	return nil, fmt.Errorf("git.file_tracked: %w", err)
}

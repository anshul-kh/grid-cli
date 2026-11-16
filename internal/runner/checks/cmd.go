package checks

import (
	"context"
	"errors"
	"os/exec"

	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-cli/internal/spec"
)

// cmdChecker implements every cmd.* type (§4: cmd.exit_code,
// cmd.stdout_matches, cmd.stdout_equals, cmd.stderr_matches). All four take
// the identical measurement — run the argv via runner/exec.go, capture and
// normalise stdout/stderr, report the exit code and duration — and differ
// only in how the *server* judges the evidence (equals/pattern/value, all
// judge-side fields content.publicview.ts strips from the public spec).
// Registering one Checker under all four type strings keeps that shared
// measurement in one place instead of four near-identical copies.
type cmdChecker struct{}

func (cmdChecker) Measure(ctx context.Context, c spec.Check, env *runner.Env) (runner.Evidence, error) {
	res, runErr := env.Run(ctx, runner.CmdSpec{
		Run:   c.Run,
		Shell: c.Shell,
		Dir:   env.Dir(c.Cwd),
		Stdin: []byte(c.Stdin),
		Env:   c.Env,
		// The check's own timeout is already the context deadline
		// MeasureCheck wraps around this call (runner.go); setting it again
		// here too costs nothing (context.WithTimeout always keeps the
		// earlier deadline) and makes the checker correct when Measure is
		// exercised directly, without going through MeasureCheck, as the
		// tests do.
		Timeout: c.Timeout.D(),
	})

	if res == nil {
		// The command could not even be described (empty argv) — a content
		// bug, not a runtime one, but still nothing was measured.
		return nil, runErr
	}

	evidence := runner.BuildEvidence(map[string]any{
		"exitCode":   res.ExitCode,
		"stdout":     string(runner.NormalizeOutput(res.Stdout)),
		"stderr":     string(runner.NormalizeOutput(res.Stderr)),
		"durationMs": res.DurationMS,
	})
	if res.StdoutTruncated || res.StderrTruncated {
		evidence["truncated"] = true
	}

	if runErr == nil {
		return evidence, nil
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		// A nonzero exit is exactly the measurement cmd.exit_code (and its
		// siblings) exist to report — not a failure to take it (§1
		// principle 1: "wrong answer" is not an error).
		return evidence, nil
	}

	// Timeout, cancellation, or the command never started at all (not
	// found, permission denied) — the measurement itself failed.
	return evidence, runErr
}

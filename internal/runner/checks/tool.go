package checks

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-cli/internal/spec"
)

// defaultVersionArgs is §4's default when a tool.installed check does not
// set `version_args` (plan Assumption 9).
var defaultVersionArgs = []string{"--version"}

// toolInstalledChecker implements tool.installed (§4): evidence is
// { found, path, versionOutput }. Resolution uses exec.LookPath — never a
// shelled-out `which`/`where`, so the check works identically on every OS
// and never depends on a login shell (§4 cross-platform rule 5).
// `version_matches` is judge-side and absent from the public spec; the
// checker just captures whatever the tool prints.
type toolInstalledChecker struct{}

func (toolInstalledChecker) Measure(ctx context.Context, c spec.Check, env *runner.Env) (runner.Evidence, error) {
	if c.Name == "" {
		return nil, fmt.Errorf("tool.installed: check %q has no name", c.ID)
	}

	path, err := exec.LookPath(c.Name)
	if err != nil {
		// Not found is a clean observation — most tool.installed checks
		// exist precisely to measure that the learner hasn't installed
		// something yet.
		return runner.BuildEvidence(map[string]any{"found": false}), nil
	}

	versionArgs := c.VersionArgs
	if len(versionArgs) == 0 {
		versionArgs = defaultVersionArgs
	}

	res, runErr := env.Run(ctx, runner.CmdSpec{Run: append([]string{path}, versionArgs...)})
	if res == nil {
		return nil, fmt.Errorf("tool.installed: running %q %v: %w", path, versionArgs, runErr)
	}

	// Many tools print their version to stderr instead of stdout (or vice
	// versa); evidence has one versionOutput field, so both streams are
	// combined — whichever is non-empty, or both, in that order.
	var combined []byte
	combined = append(combined, res.Stdout...)
	if len(res.Stdout) > 0 && len(res.Stderr) > 0 {
		combined = append(combined, '\n')
	}
	combined = append(combined, res.Stderr...)

	evidence := runner.BuildEvidence(map[string]any{
		"found":         true,
		"path":          path,
		"versionOutput": string(runner.NormalizeOutput(combined)),
	})

	if runErr == nil {
		return evidence, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		// A tool that exits nonzero on its own version flag is still
		// evidence, not a failed measurement.
		return evidence, nil
	}
	return evidence, runErr
}

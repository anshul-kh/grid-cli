package checks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-cli/internal/spec"
)

// dirTreeEntryCap is §4's dir.tree evidence cap: entries[] is capped 500.
// BuildEvidence only byte-caps string/[]byte fields (evidence.go), so the
// composite entries slice is capped here, matching git.go's 50-commit cap.
const dirTreeEntryCap = 500

// dirExistsChecker implements dir.exists (§4): evidence is { exists }.
// `negate` is judge-side (plan Assumption 5) — the checker always reports
// what it observed.
type dirExistsChecker struct{}

func (dirExistsChecker) Measure(_ context.Context, c spec.Check, env *runner.Env) (runner.Evidence, error) {
	full := env.Dir(c.Path)
	info, err := os.Stat(full)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return runner.BuildEvidence(map[string]any{"exists": false}), nil
	case err != nil:
		return nil, fmt.Errorf("dir.exists: stat %q: %w", c.Path, err)
	}
	return runner.BuildEvidence(map[string]any{"exists": info.IsDir()}), nil
}

// dirTreeChecker implements dir.tree (§4): evidence is { entries[] } —
// paths relative to c.Path, forward-slashed, sorted, capped at 500.
// `must_include`/`must_not_include` are judge-side and absent from the
// public spec; `max_depth` is kept (content.publicview.ts), so the CLI
// bounds the walk itself rather than uploading an unbounded tree.
type dirTreeChecker struct{}

func (dirTreeChecker) Measure(_ context.Context, c spec.Check, env *runner.Env) (runner.Evidence, error) {
	full := env.Dir(c.Path)
	rootInfo, err := os.Stat(full)
	if err != nil {
		return nil, fmt.Errorf("dir.tree: stat %q: %w", c.Path, err)
	}
	if !rootInfo.IsDir() {
		return nil, fmt.Errorf("dir.tree: %q is not a directory", c.Path)
	}

	entries := []string{}
	walkErr := filepath.WalkDir(full, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == full {
			return nil
		}
		rel, err := filepath.Rel(full, p)
		if err != nil {
			return err
		}
		depth := strings.Count(rel, string(filepath.Separator)) + 1
		if c.MaxDepth > 0 && depth > c.MaxDepth {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		entries = append(entries, filepath.ToSlash(rel))
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("dir.tree: walking %q: %w", c.Path, walkErr)
	}

	sort.Strings(entries)
	truncated := len(entries) > dirTreeEntryCap
	if truncated {
		entries = entries[:dirTreeEntryCap]
	}

	ev := runner.BuildEvidence(map[string]any{"entries": entries})
	if truncated {
		ev["truncated"] = true
	}
	return ev, nil
}

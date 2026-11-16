package checks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-cli/internal/spec"
)

// readFileWithHash reads a file whole and hashes it — the single read used
// by every file.* checker (§4: file.exists, file.contains and file.hash all
// report a sha256 of the file's actual bytes, never a normalised copy, so
// the hash means the same thing a learner's own `sha256sum` would show).
func readFileWithHash(path string) (content []byte, hexSum string, size int64, err error) {
	content, err = os.ReadFile(path)
	if err != nil {
		return nil, "", 0, err
	}
	sum := sha256.Sum256(content)
	return content, hex.EncodeToString(sum[:]), int64(len(content)), nil
}

// fileExistsChecker implements file.exists (§4): evidence is
// { exists, size, sha256 }. `negate` is judge-side (plan Assumption 5 /
// GRID_SPEC §1 principle 1) — the checker always reports what it observed;
// it never flips `exists` to pre-answer the server's question.
type fileExistsChecker struct{}

func (fileExistsChecker) Measure(_ context.Context, c spec.Check, env *runner.Env) (runner.Evidence, error) {
	full := env.Dir(c.Path)
	info, err := os.Stat(full)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return runner.BuildEvidence(map[string]any{"exists": false}), nil
	case err != nil:
		return nil, fmt.Errorf("file.exists: stat %q: %w", c.Path, err)
	}
	if info.IsDir() {
		// A directory sits there, not a file — "exists" (as a file) is false;
		// this is still a clean observation, not an inability to measure.
		return runner.BuildEvidence(map[string]any{"exists": false}), nil
	}
	_, hexSum, size, err := readFileWithHash(full)
	if err != nil {
		return nil, fmt.Errorf("file.exists: reading %q: %w", c.Path, err)
	}
	return runner.BuildEvidence(map[string]any{"exists": true, "size": size, "sha256": hexSum}), nil
}

// fileContainsChecker implements file.contains (§4): evidence is
// { sha256, size, excerpt } — the excerpt is the first 32 KB, or ±5 lines
// around the first match when the check carries `pattern`/`text` (judge-
// only fields the public spec strips for this type per
// content.publicview.ts — present only when a judge-complete spec is
// decoded locally). Locating a match is not judging: the checker never
// turns it into ok/error, only into which slice of the file is worth
// uploading.
type fileContainsChecker struct{}

func (fileContainsChecker) Measure(_ context.Context, c spec.Check, env *runner.Env) (runner.Evidence, error) {
	full := env.Dir(c.Path)
	content, hexSum, size, err := readFileWithHash(full)
	if err != nil {
		return nil, fmt.Errorf("file.contains: reading %q: %w", c.Path, err)
	}

	normalized := runner.NormalizeOutput(content)
	excerpt, truncated := runner.FileExcerpt(normalized, locateMatch(normalized, c))

	fields := map[string]any{"sha256": hexSum, "size": size, "excerpt": string(excerpt)}
	ev := runner.BuildEvidence(fields)
	if truncated {
		ev["truncated"] = true
	}
	return ev, nil
}

// locateMatch finds the byte offset of the first match of c.Pattern (a
// RE2-compatible regex, compiled per normalize.go's multiline-always-on
// contract) or, failing that, c.Text (a literal substring) within the
// already-normalised content. It returns -1 — "no match, use the plain head
// excerpt" — whenever neither field is set, the pattern fails to compile
// (a content bug, not a CLI measurement failure), or nothing matches.
func locateMatch(normalized []byte, c spec.Check) int {
	if c.Pattern != "" {
		re, err := compileFilePattern(c.Pattern, c.IgnoreCase)
		if err != nil {
			return -1
		}
		loc := re.FindIndex(normalized)
		if loc == nil {
			return -1
		}
		return loc[0]
	}
	if c.Text != "" {
		if c.IgnoreCase {
			return strings.Index(strings.ToLower(string(normalized)), strings.ToLower(c.Text))
		}
		return bytes.Index(normalized, []byte(c.Text))
	}
	return -1
}

// compileFilePattern compiles pattern with the multiline flag always on
// (normalize.go's documented contract) plus case-insensitivity when
// ignoreCase is set — mirrors grid-backend's normalize.ts::compilePattern.
func compileFilePattern(pattern string, ignoreCase bool) (*regexp.Regexp, error) {
	flags := "m"
	if ignoreCase {
		flags += "i"
	}
	return regexp.Compile(fmt.Sprintf("(?%s)%s", flags, pattern))
}

// fileHashChecker implements file.hash (§4): evidence is { sha256 } only —
// it never compares to the spec's expected hash (the public spec omits it
// anyway; the server owns that comparison).
type fileHashChecker struct{}

func (fileHashChecker) Measure(_ context.Context, c spec.Check, env *runner.Env) (runner.Evidence, error) {
	full := env.Dir(c.Path)
	_, hexSum, _, err := readFileWithHash(full)
	if err != nil {
		return nil, fmt.Errorf("file.hash: reading %q: %w", c.Path, err)
	}
	return runner.BuildEvidence(map[string]any{"sha256": hexSum}), nil
}

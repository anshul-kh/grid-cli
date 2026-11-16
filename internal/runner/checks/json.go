package checks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-cli/internal/spec"
)

// jsonFileChecker implements json.file (§4): evidence is { sha256, value } —
// it parses the file, evaluates jsonpath, and uploads whatever value it
// found. `equals`/`contains` are judge-side and the server decides whether
// the value satisfies them; the checker never does.
type jsonFileChecker struct{}

func (jsonFileChecker) Measure(_ context.Context, c spec.Check, env *runner.Env) (runner.Evidence, error) {
	full := env.Dir(c.Path)
	content, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("json.file: reading %q: %w", c.Path, err)
	}
	sum := sha256.Sum256(content)

	var doc any
	if err := json.Unmarshal(content, &doc); err != nil {
		return nil, fmt.Errorf("json.file: %q is not valid JSON: %w", c.Path, err)
	}

	// A jsonpath that does not resolve — a missing key, an out-of-range
	// index, a type mismatch, or a malformed path expression — is not a
	// measurement failure: it is the learner not having gotten there yet
	// (§1 principle 1). It reports as value: null, same as a genuine JSON
	// null at that path; only the file itself being unreadable or not JSON
	// stops the measurement.
	value := evalJSONPath(c.JSONPath, doc)

	return runner.BuildEvidence(map[string]any{
		"sha256": hex.EncodeToString(sum[:]),
		"value":  value,
	}), nil
}

// jsonPathSegKind is one evalJSONPath step.
type jsonPathSegKind int

const (
	jsonPathKey jsonPathSegKind = iota
	jsonPathIndex
	jsonPathWildcard
)

// jsonPathSeg is one parsed path step: a `.key`/`['key']` object access, a
// `[N]` array index, or a `[*]` wildcard over every array element.
type jsonPathSeg struct {
	kind  jsonPathSegKind
	key   string
	index int
}

// evalJSONPath evaluates the small JSONPath subset §4's `jsonpath` field
// uses in every lesson shipped so far ($.key, $.a.b, $.arr[0], $.$schema,
// $.arr[*]): root "$", dot keys, bracket keys ('...'/"..."), numeric
// indices, and "*". It never returns an error — a path that cannot be
// followed is exactly as reportable as the value it would have found
// (nil), see Measure's comment.
func evalJSONPath(path string, doc any) any {
	segs, err := parseJSONPath(path)
	if err != nil {
		return nil
	}
	v, ok := applyJSONPath(segs, doc)
	if !ok {
		return nil
	}
	return v
}

func parseJSONPath(path string) ([]jsonPathSeg, error) {
	if !strings.HasPrefix(path, "$") {
		return nil, fmt.Errorf("jsonpath %q must start with \"$\"", path)
	}
	rest := path[1:]
	var segs []jsonPathSeg
	i := 0
	for i < len(rest) {
		switch rest[i] {
		case '.':
			i++
			start := i
			for i < len(rest) && rest[i] != '.' && rest[i] != '[' {
				i++
			}
			key := rest[start:i]
			if key == "" {
				return nil, fmt.Errorf("jsonpath %q has an empty segment", path)
			}
			segs = append(segs, jsonPathSeg{kind: jsonPathKey, key: key})
		case '[':
			end := strings.IndexByte(rest[i:], ']')
			if end < 0 {
				return nil, fmt.Errorf("jsonpath %q has an unterminated \"[\"", path)
			}
			inner := rest[i+1 : i+end]
			i += end + 1
			switch {
			case inner == "*":
				segs = append(segs, jsonPathSeg{kind: jsonPathWildcard})
			case len(inner) >= 2 && (inner[0] == '\'' || inner[0] == '"') && inner[len(inner)-1] == inner[0]:
				segs = append(segs, jsonPathSeg{kind: jsonPathKey, key: inner[1 : len(inner)-1]})
			default:
				n, err := strconv.Atoi(inner)
				if err != nil {
					return nil, fmt.Errorf("jsonpath %q has an invalid index %q", path, inner)
				}
				segs = append(segs, jsonPathSeg{kind: jsonPathIndex, index: n})
			}
		default:
			return nil, fmt.Errorf("jsonpath %q is malformed at %q", path, rest[i:])
		}
	}
	return segs, nil
}

func applyJSONPath(segs []jsonPathSeg, v any) (any, bool) {
	if len(segs) == 0 {
		return v, true
	}
	seg, rest := segs[0], segs[1:]
	switch seg.kind {
	case jsonPathKey:
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		child, ok := m[seg.key]
		if !ok {
			return nil, false
		}
		return applyJSONPath(rest, child)
	case jsonPathIndex:
		arr, ok := v.([]any)
		if !ok || seg.index < 0 || seg.index >= len(arr) {
			return nil, false
		}
		return applyJSONPath(rest, arr[seg.index])
	case jsonPathWildcard:
		arr, ok := v.([]any)
		if !ok {
			return nil, false
		}
		out := make([]any, 0, len(arr))
		for _, el := range arr {
			if child, ok := applyJSONPath(rest, el); ok {
				out = append(out, child)
			}
		}
		return out, true
	default:
		return nil, false
	}
}

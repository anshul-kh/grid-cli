package checks

import (
	"context"
	"regexp"
	"strings"

	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-cli/internal/spec"
)

// envVarSetChecker implements env.var_set (§4): evidence is
// { set, matches } and it NEVER uploads the variable's value — the one
// documented exception where `pattern` survives content.publicview.ts,
// specifically so this checker can compute `matches` itself without
// sending the secret anywhere. `matches` is only evaluated — and only
// present in the evidence at all — when a pattern was given; its absence
// means "report `set` only" (GRID_SPEC §4 / plan pass 5 item 7).
type envVarSetChecker struct{}

func (envVarSetChecker) Measure(_ context.Context, c spec.Check, env *runner.Env) (runner.Evidence, error) {
	value, set := lookupMergedEnv(env, c.Name)

	fields := map[string]any{"set": set}
	if c.Pattern != "" {
		matches := false
		if set {
			if re, err := regexp.Compile(c.Pattern); err == nil {
				matches = re.MatchString(value)
			}
			// A pattern that fails to compile is a content bug the server
			// can see from its own copy of the spec; the checker does not
			// fail the measurement over it — `set` alone is still true
			// evidence.
		}
		fields["matches"] = matches
	}
	// value never enters fields, an error message, or a log line from here
	// on — this is the whole point of the check.
	return runner.BuildEvidence(fields), nil
}

// lookupMergedEnv finds name in the environment the runner actually applies
// to child processes — spec env layered over the parent environment
// (runner.Env.Environ's documented precedence) — and reports whether it is
// set and, if so, its value. The value is returned to the caller only to
// evaluate `matches` locally; envVarSetChecker never lets it leave that
// scope.
func lookupMergedEnv(env *runner.Env, name string) (value string, set bool) {
	prefix := name + "="
	for _, kv := range env.Environ(nil) {
		if rest, ok := strings.CutPrefix(kv, prefix); ok {
			value, set = rest, true
		}
	}
	return value, set
}

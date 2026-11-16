// Package checks holds the per-check-type measurement implementations
// (TASK.md §4: one Checker per GRID_SPEC §4 type, landing in pass 5) and
// the registry mapping a type string to its Checker.
package checks

import (
	"sort"
	"strings"
	"sync"

	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-cli/internal/spec"
)

// Checker performs a single measurement (TASK.md §4). It returns evidence
// and an error only if the measurement itself could not be taken — a
// "wrong answer" is not an error; the server judges the evidence. Declared
// as an alias of runner.Checker so the runner's orchestration skeleton can
// consume registries without an import cycle (checks imports runner for
// Env/Evidence; runner must not import checks).
type Checker = runner.Checker

// Registry maps §4 check-type strings to Checkers. Unknown types are a
// hard error naming the CLI version and suggesting `grid upgrade`
// (TASK.md §4 forward compatibility): the server may ship a new check type
// before the user updates, and that must surface as a clear, actionable
// error — never silent behaviour.
//
// Pass 4 registers nothing: every lookup returns the upgrade-hint error.
// Pass 5 registers all 15 §4 types.
type Registry struct {
	mu       sync.RWMutex
	checkers map[string]Checker
	// prefixes are the namespaced type families resolved by factory (the
	// ext.<name>.<action> namespace, §4/A1) — a family, not fixed strings,
	// because each check's checker is built per check from its own type.
	prefixes []prefixEntry
}

// prefixEntry is one registered namespace prefix and its checker factory.
type prefixEntry struct {
	prefix  string
	factory PrefixFactory
}

// PrefixFactory resolves the Checker for one check whose type lives under
// a registered namespace prefix. A factory (not a shared checker) because
// an ext check's name/version/action are per type, and the resulting
// checker carries its own result instance — never shared across parallel
// measurements.
type PrefixFactory func(typeName string) (Checker, error)

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{checkers: make(map[string]Checker)}
}

// Register adds the Checker for a §4 check type.
func (r *Registry) Register(typeName string, c Checker) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkers[typeName] = c
}

// RegisterPrefix adds a namespace family: any type with this prefix
// resolves through the factory. Prefixes are consulted only after the
// exact-type map misses, so a builtin type always wins, and they never
// appear in Registered() — the §4 type table stays exactly the 15 builtins.
func (r *Registry) RegisterPrefix(prefix string, f PrefixFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prefixes = append(r.prefixes, prefixEntry{prefix: prefix, factory: f})
}

// Lookup resolves a check type to its Checker. An unregistered type —
// known-but-unimplemented or not in §4 at all — returns the versioned
// upgrade-hint error from spec.UnknownCheckTypeError.
func (r *Registry) Lookup(typeName string) (Checker, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if c, ok := r.checkers[typeName]; ok {
		return c, nil
	}
	for _, p := range r.prefixes {
		if strings.HasPrefix(typeName, p.prefix) {
			return p.factory(typeName)
		}
	}
	return nil, spec.UnknownCheckTypeError(typeName)
}

// Registered lists the registered type strings, sorted.
func (r *Registry) Registered() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.checkers))
	for t := range r.checkers {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// NewDefaultRegistry returns a Registry with all 15 §4 check types
// registered against their Checker implementations (file.go, dir.go,
// cmd.go, http.go, json.go, git.go, env.go, tool.go). This is what `grid
// run`/`grid submit` (pass 6/7) wire into Runner.RunChecks; any type not in
// this list — including a genuinely new §4 addition this CLI predates —
// still returns the upgrade-hint error.
func NewDefaultRegistry() *Registry {
	r := NewRegistry()

	r.Register("file.exists", fileExistsChecker{})
	r.Register("file.contains", fileContainsChecker{})
	r.Register("file.hash", fileHashChecker{})

	r.Register("dir.exists", dirExistsChecker{})
	r.Register("dir.tree", dirTreeChecker{})

	cmd := cmdChecker{}
	r.Register("cmd.exit_code", cmd)
	r.Register("cmd.stdout_matches", cmd)
	r.Register("cmd.stdout_equals", cmd)
	r.Register("cmd.stderr_matches", cmd)

	r.Register("http.request", httpRequestChecker{})
	r.Register("json.file", jsonFileChecker{})

	r.Register("git.has_commit", gitHasCommitChecker{})
	r.Register("git.file_tracked", gitFileTrackedChecker{})

	r.Register("env.var_set", envVarSetChecker{})
	r.Register("tool.installed", toolInstalledChecker{})

	return r
}

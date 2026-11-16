// Package spec implements the GRID_SPEC §4 check-spec grammar: the YAML
// document shapes (CheckSpec, Check, Service, SetupStep), strict decoding that
// tolerates the server's public view (§5.2, judge-only expected values
// stripped), and structural validation (validate.go).
package spec

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that decodes from the spec's time-style
// strings ("180s", "30s"). A bare integer is tolerated and read as seconds,
// though §4 always writes the string form.
type Duration time.Duration

// UnmarshalYAML decodes "180s"-style strings (and bare integers as seconds).
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Tag == "!!null" {
		*d = 0
		return nil
	}
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("timeout must be a duration string like \"180s\", got %s", node.Tag)
	}
	if node.Tag == "!!int" {
		var secs int64
		if err := node.Decode(&secs); err != nil {
			return err
		}
		*d = Duration(secs * int64(time.Second))
		return nil
	}
	parsed, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("timeout %q is not a valid duration (use the time style, e.g. \"30s\"): %w", node.Value, err)
	}
	*d = Duration(parsed)
	return nil
}

// D returns the underlying time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// CheckSpec mirrors the full §4 grammar. Both the authored judge-complete
// YAML and the public view the server serves to `grid run` (§5.2) decode into
// it: every judge-only field is an ordinary optional field here, so its
// absence is fine; a genuinely unknown key is a hard error (strict decoding —
// a forward-incompatible spec must surface clearly, never silently).
type CheckSpec struct {
	Version  int               `yaml:"version"`
	Workdir  string            `yaml:"workdir"`
	Timeout  Duration          `yaml:"timeout"`
	Env      map[string]string `yaml:"env"`
	Setup    []SetupStep       `yaml:"setup"`
	Services []Service         `yaml:"services"`
	Checks   []Check           `yaml:"checks"`
	// Workspace mirrors the check-spec's own workspace: block (GRID_SPEC
	// §4 "Workspace modes", confirmed 2026-11-13 against a live seeded
	// backend: every attempt's checkSpec now carries one, e.g.
	// {"mode":"folder"}). Decoded as `any`, not a nested struct, because
	// grid run never reads it from here — it reads the resolved mode from
	// the attempt response's own top-level workspace object
	// (internal/api.Workspace.Mode) — so this field exists purely to keep
	// KnownFields(true) from hard-erroring on a key the public view
	// legitimately sends; its sub-shape (today just `mode`; `bundle.key`
	// is reserved per GRID_SYNC_CLAUDE.md 2026-11-13) is the server's
	// concern, not something worth tracking field-by-field here.
	Workspace any `yaml:"workspace"`
	// Extensions are the §4/A1 extension pins: the signed binaries this
	// spec's ext.* checks need, cached under ~/.grid/extensions before
	// anything measures (internal/extensions).
	Extensions []ExtensionPin `yaml:"extensions"`
}

// ExtensionPin is one §4/A1 pin: the extension's name (its cache/mirror
// directory) and the exact version to ensure.
type ExtensionPin struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
}

// ExtTypePrefix prefixes every extension check type (§4/A1).
const ExtTypePrefix = "ext."

// ParseExtType splits an ext.<name>.<action> check type into its parts
// (§4/A1). ok is false for anything that is not a well-formed ext type —
// builtin types included.
func ParseExtType(typeName string) (name, action string, ok bool) {
	rest, found := strings.CutPrefix(typeName, ExtTypePrefix)
	if !found {
		return "", "", false
	}
	name, action, ok = strings.Cut(rest, ".")
	return name, action, ok && name != "" && action != ""
}

// SetupStep is one §4 setup command. expect_exit is judge-side and is stripped
// from the public view (the CLI still runs the step; the server compares the
// reported exitCode).
type SetupStep struct {
	Run        []string `yaml:"run"`
	ExpectExit *int     `yaml:"expect_exit"`
}

// Service is one §4 long-running process, started before checks and stopped
// after. Services carry no expected values and pass through the public view
// unchanged.
type Service struct {
	ID    string       `yaml:"id"`
	Run   []string     `yaml:"run"`
	Ready ServiceReady `yaml:"ready"`
}

// ServiceReady probes readiness: a tcp address to dial or an http URL that
// must return 2xx, within the given timeout.
type ServiceReady struct {
	TCP     string   `yaml:"tcp"`
	HTTP    string   `yaml:"http"`
	Timeout Duration `yaml:"timeout"`
}

// HTTPExpect is the judge-side `expect` block of an http.request check. It is
// never present in the public view (§5.2); decoding tolerates its absence.
type HTTPExpect struct {
	Status      *int   `yaml:"status"`
	BodyMatches string `yaml:"body_matches"`
	JSONPath    string `yaml:"jsonpath"`
	Equals      any    `yaml:"equals"`
	Contains    string `yaml:"contains"`
}

// Check mirrors every field §4 gives any check type: the common optional
// fields plus all per-type fields. Fields a type does not use are simply
// empty. Judge-only expected values (pattern — except env.var_set's
// documented exception — equals, sha256, value, must_include,
// must_not_include, expect, expect_exit) are declared so the CLI can decode a
// judge-complete spec too, but validation never requires them.
type Check struct {
	// Common optional fields (§4). parallel_safe comes from TASK.md §4
	// (concurrency); it is not in GRID_SPEC §4's common list and the public
	// view omits it — absence means sequential.
	ID           string   `yaml:"id"`
	Title        string   `yaml:"title"`
	Hint         string   `yaml:"hint"`
	Weight       int      `yaml:"weight"`
	Optional     bool     `yaml:"optional"`
	Timeout      Duration `yaml:"timeout"`
	DependsOn    []string `yaml:"depends_on"`
	ParallelSafe bool     `yaml:"parallel_safe"`

	// Per-type fields (§4 check-type table).
	Type           string            `yaml:"type"`
	Path           string            `yaml:"path"`
	Negate         bool              `yaml:"negate"` // judge-side interpretation; public view strips it
	Pattern        string            `yaml:"pattern"`
	Text           string            `yaml:"text"`
	IgnoreCase     bool              `yaml:"ignore_case"`
	Trim           bool              `yaml:"trim"`
	Run            []string          `yaml:"run"`
	Equals         any               `yaml:"equals"`
	Cwd            string            `yaml:"cwd"`
	Stdin          string            `yaml:"stdin"`
	Env            map[string]string `yaml:"env"`
	Shell          bool              `yaml:"shell"`
	MustInclude    []string          `yaml:"must_include"`
	MustNotInclude []string          `yaml:"must_not_include"`
	MaxDepth       int               `yaml:"max_depth"`
	Method         string            `yaml:"method"`
	URL            string            `yaml:"url"`
	Headers        map[string]string `yaml:"headers"`
	Body           string            `yaml:"body"`
	JSONPath       string            `yaml:"jsonpath"`
	Name           string            `yaml:"name"`
	VersionArgs    []string          `yaml:"version_args"`
	VersionMatches string            `yaml:"version_matches"`
	MinCount       int               `yaml:"min_count"`
	MessageMatches string            `yaml:"message_matches"`
	SHA256         string            `yaml:"sha256"`
	Value          string            `yaml:"value"`
	Expect         *HTTPExpect       `yaml:"expect"`
	// Ext checks (§4/A1): params are the named extension's own free-form
	// contract, passed through verbatim to its IPC request; expected is
	// judge-only (stripped from the public view exactly like the builtin
	// expected fields) and the CLI never reads it.
	Params   map[string]any `yaml:"params"`
	Expected any            `yaml:"expected"`
}

// Parse decodes a check-spec document. It accepts both the authored YAML and
// the JSON public view (JSON is a subset of YAML 1.2, which yaml.v3 parses).
// Decoding is strict: any key not in the §4 grammar is a hard error naming
// the offending field, so a spec from a newer grammar surfaces clearly
// instead of changing behaviour silently.
func Parse(data []byte) (*CheckSpec, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var cs CheckSpec
	if err := dec.Decode(&cs); err != nil {
		return nil, fmt.Errorf("check-spec: %w", err)
	}
	return &cs, nil
}

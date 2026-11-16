package checks

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-cli/internal/spec"
)

// TestEnvVarSetSecretLeak is the test plan pass 5 item 7 requires: set a
// canary env var to a unique random value, run env.var_set against it, and
// prove the canary string appears NOWHERE in the marshalled evidence JSON
// nor in anything the checker could plausibly have logged — the whole
// reason `env.var_set` is the one type whose pattern the public spec keeps
// (content.publicview.ts) is so the CLI can answer "matches" without ever
// uploading the value itself.
func TestEnvVarSetSecretLeak(t *testing.T) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	canary := "CANARY-" + hex.EncodeToString(buf[:])

	env := runner.NewEnv(t.TempDir(), map[string]string{"GRID_SECRET": canary})

	// captured stands in for "every log line the run could have produced":
	// a Warnf channel wired exactly like the CLI wires --verbose output,
	// plus the error return and the evidence itself.
	var captured bytes.Buffer
	env.Warnf = func(format string, args ...any) { fmt.Fprintf(&captured, format+"\n", args...) }

	checker := envVarSetChecker{}
	ev, err := checker.Measure(context.Background(), spec.Check{
		ID: "c", Name: "GRID_SECRET", Pattern: "^CANARY-",
	}, env)
	if err != nil {
		t.Fatalf("Measure: unexpected error: %v", err)
	}
	if ev["set"] != true {
		t.Fatalf("evidence[set] = %v, want true", ev["set"])
	}
	if ev["matches"] != true {
		t.Fatalf("evidence[matches] = %v, want true (pattern ^CANARY- should match)", ev["matches"])
	}

	marshalled, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal evidence: %v", err)
	}
	captured.WriteString(string(marshalled))
	if err != nil {
		captured.WriteString(err.Error())
	}

	if bytes.Contains(marshalled, []byte(canary)) {
		t.Fatalf("SECRET LEAK: evidence JSON contains the canary value: %s", marshalled)
	}
	if bytes.Contains(captured.Bytes(), []byte(canary)) {
		t.Fatalf("SECRET LEAK: captured output contains the canary value: %s", captured.Bytes())
	}
}

// TestEnvVarSetWithoutPattern proves "pattern absent -> report set only"
// (§4 / plan item 7): no `matches` key at all, not even false.
func TestEnvVarSetWithoutPattern(t *testing.T) {
	env := runner.NewEnv(t.TempDir(), map[string]string{"GRID_FLAG": "anything"})
	ev, err := (envVarSetChecker{}).Measure(context.Background(), spec.Check{ID: "c", Name: "GRID_FLAG"}, env)
	if err != nil {
		t.Fatalf("Measure: unexpected error: %v", err)
	}
	if ev["set"] != true {
		t.Errorf("evidence[set] = %v, want true", ev["set"])
	}
	if _, ok := ev["matches"]; ok {
		t.Errorf("evidence has a matches field with no pattern given: %v", ev)
	}
}

// TestEnvVarSetUnsetVariable proves a variable the runner never applies is
// ok, set:false — not an error.
func TestEnvVarSetUnsetVariable(t *testing.T) {
	env := runner.NewEnv(t.TempDir(), nil)
	ev, err := (envVarSetChecker{}).Measure(context.Background(), spec.Check{
		ID: "c", Name: "GRID_TOTALLY_UNSET_VAR", Pattern: ".*",
	}, env)
	if err != nil {
		t.Fatalf("Measure: unexpected error: %v", err)
	}
	if ev["set"] != false {
		t.Errorf("evidence[set] = %v, want false", ev["set"])
	}
	if ev["matches"] != false {
		t.Errorf("evidence[matches] = %v, want false when unset (nothing to match against)", ev["matches"])
	}
}

// TestLookupMergedEnvSpecOverridesParent proves the runner's documented
// precedence (spec env over parent env) is what env.var_set actually
// checks, not just the parent process's own environment.
func TestLookupMergedEnvSpecOverridesParent(t *testing.T) {
	t.Setenv("GRID_OVERRIDE_TEST", "from-parent")
	env := runner.NewEnv(t.TempDir(), map[string]string{"GRID_OVERRIDE_TEST": "from-spec"})
	value, set := lookupMergedEnv(env, "GRID_OVERRIDE_TEST")
	if !set || value != "from-spec" {
		t.Errorf("lookupMergedEnv = (%q, %v), want (\"from-spec\", true)", value, set)
	}
}

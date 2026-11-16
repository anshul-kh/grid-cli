package checks

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-cli/internal/spec"
)

func jsonFixtureEnv(t *testing.T) *runner.Env {
	t.Helper()
	fixture := filepath.Join("..", "..", "..", "testdata", "checks", "sample.json")
	content, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sample.json"), content, 0o644); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	return runner.NewEnv(dir, nil)
}

// TestJSONFileExtractsValue proves json.file's { sha256, value } shape
// against every jsonpath style actually used in shipped lesson content
// (plain key, nested key, array index, and the "$schema" key whose own
// name starts with "$").
func TestJSONFileExtractsValue(t *testing.T) {
	env := jsonFixtureEnv(t)
	checker := jsonFileChecker{}

	cases := []struct {
		name     string
		jsonpath string
		want     any
	}{
		{"plain key", "$.version", "1.0.0"},
		{"bool", "$.ready", true},
		{"nested key", "$.permission.webfetch", "ask"},
		{"array index then key", "$.hooks.PostToolUse[0].matcher", "Write"},
		{"dollar-prefixed key", "$.$schema", "https://example.com/schema.json"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev, err := checker.Measure(context.Background(), spec.Check{Path: "sample.json", JSONPath: c.jsonpath}, env)
			if err != nil {
				t.Fatalf("Measure(%q): unexpected error: %v", c.jsonpath, err)
			}
			if ev["value"] != c.want {
				t.Errorf("Measure(%q)[value] = %v (%T), want %v (%T)", c.jsonpath, ev["value"], ev["value"], c.want, c.want)
			}
			if _, ok := ev["sha256"].(string); !ok {
				t.Errorf("evidence is missing sha256: %v", ev)
			}
		})
	}
}

// TestJSONFileUnresolvedPathIsValueNilNotError proves a jsonpath that does
// not resolve — the learner hasn't added that key yet — is ok, value: nil,
// never a Measure error (§1 principle 1 applies to json.file too).
func TestJSONFileUnresolvedPathIsValueNilNotError(t *testing.T) {
	env := jsonFixtureEnv(t)
	ev, err := (jsonFileChecker{}).Measure(context.Background(), spec.Check{
		Path: "sample.json", JSONPath: "$.not.there.at.all",
	}, env)
	if err != nil {
		t.Fatalf("Measure: unexpected error for an unresolved path: %v", err)
	}
	if ev["value"] != nil {
		t.Errorf("evidence[value] = %v, want nil", ev["value"])
	}
}

// TestJSONFileMalformedJSONIsError proves a file that is not valid JSON is
// a genuine measurement failure (unlike an unresolved path).
func TestJSONFileMalformedJSONIsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	env := runner.NewEnv(dir, nil)
	if _, err := (jsonFileChecker{}).Measure(context.Background(), spec.Check{Path: "bad.json", JSONPath: "$.x"}, env); err == nil {
		t.Fatal("Measure on malformed JSON: want error, got nil")
	}
}

// TestEvalJSONPathWildcard proves the "[*]" wildcard maps the remainder of
// the path over every array element.
func TestEvalJSONPathWildcard(t *testing.T) {
	doc := map[string]any{
		"items": []any{
			map[string]any{"name": "a"},
			map[string]any{"name": "b"},
		},
	}
	got := evalJSONPath("$.items[*].name", doc)
	arr, ok := got.([]any)
	if !ok || len(arr) != 2 || arr[0] != "a" || arr[1] != "b" {
		t.Errorf("evalJSONPath(wildcard) = %#v, want [a b]", got)
	}
}

package spec

import (
	"strings"
	"testing"
)

// extSampleYAML is the A1 sample shape: a top-level extensions array and an
// ext.loadtest.http_benchmark check with free-form params.
const extSampleYAML = `version: 1
workdir: "."
timeout: 180s
extensions:
  - name: loadtest
    version: 0.1.0
checks:
  - id: load_1k_rps
    title: "1k rps for 60s, p99 < 120ms"
    type: ext.loadtest.http_benchmark
    params:
      targetUrl: "http://localhost:8080/api/v1/items"
      targetRps: 1000
      durationSeconds: 60
`

func TestParseAcceptsExtSample(t *testing.T) {
	cs, err := Parse([]byte(extSampleYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cs.Extensions) != 1 || cs.Extensions[0].Name != "loadtest" || cs.Extensions[0].Version != "0.1.0" {
		t.Errorf("Extensions = %+v, want [{loadtest 0.1.0}]", cs.Extensions)
	}
	if len(cs.Checks) != 1 {
		t.Fatalf("Checks = %+v, want 1", cs.Checks)
	}
	c := cs.Checks[0]
	name, action, ok := ParseExtType(c.Type)
	if !ok || name != "loadtest" || action != "http_benchmark" {
		t.Errorf("ParseExtType(%q) = %q/%q/%v, want loadtest/http_benchmark/true", c.Type, name, action, ok)
	}
	if c.Params["targetUrl"] != "http://localhost:8080/api/v1/items" {
		t.Errorf("Params[targetUrl] = %v, want the free-form param passed through", c.Params["targetUrl"])
	}
	if err := Validate(cs); err != nil {
		t.Errorf("Validate on the A1 sample: %v", err)
	}
}

func TestParseExtType(t *testing.T) {
	cases := []struct {
		in           string
		name, action string
		ok           bool
	}{
		{"ext.loadtest.http_benchmark", "loadtest", "http_benchmark", true},
		{"file.exists", "", "", false},
		{"ext.nodot", "nodot", "", false},
		{"ext..action", "", "action", false},
	}
	for _, tc := range cases {
		name, action, ok := ParseExtType(tc.in)
		if ok != tc.ok || name != tc.name || action != tc.action {
			t.Errorf("ParseExtType(%q) = %q/%q/%v, want %q/%q/%v", tc.in, name, action, ok, tc.name, tc.action, tc.ok)
		}
	}
}

func TestValidateRejectsExtPinningMismatch(t *testing.T) {
	// The check names loadtest; the spec pins nothing.
	unpinned := `version: 1
checks:
  - id: load
    type: ext.loadtest.http_benchmark
`
	cs, err := Parse([]byte(unpinned))
	if err != nil {
		t.Fatal(err)
	}
	err = Validate(cs)
	if err == nil || !strings.Contains(err.Error(), "does not pin") {
		t.Errorf("Validate(unpinned ext check) = %v, want the pinning error", err)
	}

	// A pin without a version is also refused — cache paths are built
	// from these values.
	versionless := `version: 1
extensions:
  - name: loadtest
checks:
  - id: load
    type: ext.loadtest.http_benchmark
`
	cs, err = Parse([]byte(versionless))
	if err != nil {
		t.Fatal(err)
	}
	err = Validate(cs)
	if err == nil || !strings.Contains(err.Error(), "name and version must both be set") {
		t.Errorf("Validate(versionless pin) = %v, want the version error", err)
	}

	// An ext check naming a *different* pinned extension is refused too.
	mismatched := `version: 1
extensions:
  - name: other
    version: 1.0.0
checks:
  - id: load
    type: ext.loadtest.http_benchmark
`
	cs, err = Parse([]byte(mismatched))
	if err != nil {
		t.Fatal(err)
	}
	err = Validate(cs)
	if err == nil || !strings.Contains(err.Error(), "does not pin") {
		t.Errorf("Validate(mismatched pin) = %v, want the pinning error", err)
	}
}

// TestParseToleratesJudgeOnlyExpected: an ext check's expected object is
// judge-only (A1) — the CLI must decode a judge-complete spec without
// hard-erroring, and never read it.
func TestParseToleratesJudgeOnlyExpected(t *testing.T) {
	judge := `version: 1
extensions:
  - name: loadtest
    version: 0.1.0
checks:
  - id: load
    type: ext.loadtest.http_benchmark
    params:
      targetRps: 1000
    expected:
      minActualRps: 980
      maxP99Ms: 120
`
	cs, err := Parse([]byte(judge))
	if err != nil {
		t.Fatalf("Parse of a judge-complete ext spec: %v", err)
	}
	if cs.Checks[0].Expected == nil {
		t.Error("Expected = nil, want the judge-only object tolerated")
	}
	if err := Validate(cs); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

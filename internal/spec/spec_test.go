package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anshul-kh/grid-cli/internal/buildinfo"
)

// loadFixture reads a file under the package testdata directory.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

// TestParseFullJudgeSpec proves the judge-complete YAML (every §4 field,
// including every judge-only expected value) decodes with the exact values.
// The fixture mirrors grid-backend's FIXTURE_SPEC byte-for-byte in shape.
func TestParseFullJudgeSpec(t *testing.T) {
	cs, err := Parse(loadFixture(t, "full-spec.yaml"))
	if err != nil {
		t.Fatalf("Parse(full-spec.yaml): %v", err)
	}
	if cs.Version != 1 {
		t.Errorf("Version = %d, want 1", cs.Version)
	}
	if cs.Workdir != "." {
		t.Errorf("Workdir = %q, want \".\"", cs.Workdir)
	}
	if cs.Timeout.D() != 180*time.Second {
		t.Errorf("Timeout = %s, want 180s", cs.Timeout.D())
	}
	if cs.Env["CGO_ENABLED"] != "0" {
		t.Errorf("Env[CGO_ENABLED] = %q, want \"0\"", cs.Env["CGO_ENABLED"])
	}
	if len(cs.Setup) != 1 || len(cs.Setup[0].Run) != 2 {
		t.Fatalf("Setup = %+v, want one step `go version`", cs.Setup)
	}
	if cs.Setup[0].Run[0] != "go" || cs.Setup[0].Run[1] != "version" {
		t.Errorf("Setup[0].Run = %v, want [go version]", cs.Setup[0].Run)
	}
	if cs.Setup[0].ExpectExit == nil || *cs.Setup[0].ExpectExit != 0 {
		t.Errorf("Setup[0].ExpectExit = %v, want 0", cs.Setup[0].ExpectExit)
	}
	if len(cs.Services) != 1 {
		t.Fatalf("Services = %+v, want one service", cs.Services)
	}
	svc := cs.Services[0]
	if svc.ID != "server" || svc.Run[0] != "go" || svc.Run[1] != "run" {
		t.Errorf("Service = %+v, want id server running `go run .`", svc)
	}
	if svc.Ready.TCP != "127.0.0.1:6380" {
		t.Errorf("Service.Ready.TCP = %q, want 127.0.0.1:6380", svc.Ready.TCP)
	}
	if svc.Ready.Timeout.D() != 20*time.Second {
		t.Errorf("Service.Ready.Timeout = %s, want 20s", svc.Ready.Timeout.D())
	}

	if len(cs.Checks) != len(KnownTypes) {
		t.Fatalf("got %d checks, want %d (one per §4 type)", len(cs.Checks), len(KnownTypes))
	}
	seen := map[string]bool{}
	for _, c := range cs.Checks {
		if seen[c.Type] {
			t.Errorf("duplicate check type %q in fixture", c.Type)
		}
		seen[c.Type] = true
	}
	for _, typ := range KnownTypes {
		if !seen[typ] {
			t.Errorf("fixture is missing a %q check", typ)
		}
	}

	byID := map[string]Check{}
	for _, c := range cs.Checks {
		byID[c.ID] = c
	}

	// Judge-only fields decode on the complete shape.
	if got := byID["c-file-hash"].SHA256; !strings.HasPrefix(got, "aaaa") || len(got) != 64 {
		t.Errorf("c-file-hash sha256 = %q, want 64 a's", got)
	}
	if eq, ok := byID["c-cmd-exit"].Equals.(int); !ok || eq != 0 {
		t.Errorf("c-cmd-exit equals = %#v, want int 0", byID["c-cmd-exit"].Equals)
	}
	if got := byID["c-cmd-stdout-equals"].Value; got != "hi" {
		t.Errorf("c-cmd-stdout-equals value = %q, want \"hi\"", got)
	}
	if got := byID["c-file-contains"].Pattern; got != "secret-answer" {
		t.Errorf("c-file-contains pattern = %q, want \"secret-answer\"", got)
	}
	if got := byID["c-file-exists"].Negate; got != true {
		t.Errorf("c-file-exists negate = %v, want true", got)
	}
	if got := byID["c-dir-tree"].MustInclude; len(got) != 1 || got[0] != "keep.txt" {
		t.Errorf("c-dir-tree must_include = %v", got)
	}
	exp := byID["c-http"].Expect
	if exp == nil || exp.Status == nil || *exp.Status != 200 || exp.BodyMatches != "ok" {
		t.Errorf("c-http expect = %+v, want status 200 body_matches ok", exp)
	}
	if eq, ok := exp.Equals.(string); !ok || eq != "ok" {
		t.Errorf("c-http expect.equals = %#v, want \"ok\"", exp.Equals)
	}
	if got := byID["c-git-commit"].MinCount; got != 2 {
		t.Errorf("c-git-commit min_count = %d, want 2", got)
	}
	if got := byID["c-tool"].VersionMatches; got != `go1\.2[0-9]` {
		t.Errorf("c-tool version_matches = %q", got)
	}

	// Per-check timeout decodes as a time-style string.
	if got := byID["c-cmd-exit"].Timeout.D(); got != 60*time.Second {
		t.Errorf("c-cmd-exit timeout = %s, want 60s", got)
	}
	// Common fields.
	if byID["c-tool"].Weight != 3 || !byID["c-tool"].Optional {
		t.Errorf("c-tool weight/optional = %d/%v, want 3/true", byID["c-tool"].Weight, byID["c-tool"].Optional)
	}
	if deps := byID["c-cmd-stderr-matches"].DependsOn; len(deps) != 1 || deps[0] != "c-cmd-exit" {
		t.Errorf("c-cmd-stderr-matches depends_on = %v", deps)
	}
}

// TestParsePublicSpec proves the public view (§5.2 — judge-only fields
// stripped, setup reduced to {run}, durations as JSON strings) decodes and
// then validates: the CLI must be able to run the spec it is actually sent.
func TestParsePublicSpec(t *testing.T) {
	cs, err := Parse(loadFixture(t, "public-spec.json"))
	if err != nil {
		t.Fatalf("Parse(public-spec.json): %v", err)
	}
	if err := Validate(cs); err != nil {
		t.Fatalf("Validate(public spec): %v", err)
	}
	if len(cs.Checks) != len(KnownTypes) {
		t.Fatalf("got %d checks, want %d", len(cs.Checks), len(KnownTypes))
	}
	// The public view keeps env.var_set's pattern (the one documented
	// exception) and strips every other expected value.
	byID := map[string]Check{}
	for _, c := range cs.Checks {
		byID[c.ID] = c
	}
	if got := byID["c-env-var"].Pattern; got != "^sk-" {
		t.Errorf("public env.var_set pattern = %q, want \"^sk-\" (documented exception)", got)
	}
	if got := byID["c-file-contains"].Pattern; got != "" {
		t.Errorf("public file.contains pattern = %q, want stripped", got)
	}
	if got := byID["c-cmd-exit"].Equals; got != nil {
		t.Errorf("public cmd.exit_code equals = %#v, want stripped", got)
	}
	if got := byID["c-file-hash"].SHA256; got != "" {
		t.Errorf("public file.hash sha256 = %q, want stripped", got)
	}
	if byID["c-file-exists"].Negate {
		t.Error("public file.exists negate = true, want stripped (never in the public view)")
	}
	if byID["c-cmd-exit"].Timeout.D() != 60*time.Second {
		t.Errorf("public cmd timeout = %s, want 60s", byID["c-cmd-exit"].Timeout.D())
	}
	for _, step := range cs.Setup {
		if step.ExpectExit != nil {
			t.Errorf("public setup step still carries expect_exit: %+v", step)
		}
	}
}

// TestParseToleratesWorkspaceBlock proves strict decoding accepts the
// check-spec's own workspace: block (GRID_SPEC §4 "Workspace modes"),
// confirmed 2026-11-13 against a live seeded backend: every attempt's
// checkSpec now carries one (e.g. {"mode":"folder"}), and KnownFields(true)
// must not hard-error on it the way it does a genuinely unknown key.
func TestParseToleratesWorkspaceBlock(t *testing.T) {
	cs, err := Parse([]byte(`{"version":1,"workdir":".","timeout":"60s","workspace":{"mode":"folder"},"checks":[]}`))
	if err != nil {
		t.Fatalf("Parse with a workspace: block: %v", err)
	}
	if err := Validate(cs); err != nil {
		t.Fatalf("Validate with a workspace: block: %v", err)
	}
}

// TestParseStrictRejectsUnknownFields proves strict decoding: a spec key the
// grammar does not define is a hard error naming the key, whether at the top
// level or inside a check.
func TestParseStrictRejectsUnknownFields(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "unknown top-level key",
			yaml: "version: 1\nworkdir: \".\"\nunknown_top: 1\nchecks: []\n",
			want: "unknown_top",
		},
		{
			name: "unknown check key",
			yaml: "version: 1\nchecks:\n  - id: a\n    type: file.exists\n    path: a.txt\n    quantum: true\n",
			want: "quantum",
		},
		{
			name: "unknown service key",
			yaml: "version: 1\nservices:\n  - id: s\n    run: [\"sleep\"]\n    port: 1\n",
			want: "port",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("Parse accepted unknown key %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name the unknown key %q", err, tc.want)
			}
		})
	}
}

// TestParseDurations pins the duration grammar: time-style strings, bare
// integers as seconds, and clear errors on junk.
func TestParseDurations(t *testing.T) {
	cs, err := Parse([]byte("version: 1\ntimeout: 180s\nchecks:\n  - id: a\n    type: file.exists\n    path: a.txt\n    timeout: 30s\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cs.Timeout.D() != 180*time.Second {
		t.Errorf("spec timeout = %s, want 180s", cs.Timeout.D())
	}
	if cs.Checks[0].Timeout.D() != 30*time.Second {
		t.Errorf("check timeout = %s, want 30s", cs.Checks[0].Timeout.D())
	}

	cs, err = Parse([]byte("version: 1\ntimeout: 45\nchecks: []\n"))
	if err != nil {
		t.Fatalf("Parse integer duration: %v", err)
	}
	if cs.Timeout.D() != 45*time.Second {
		t.Errorf("integer timeout = %s, want 45s", cs.Timeout.D())
	}

	if _, err := Parse([]byte("version: 1\ntimeout: soonish\nchecks: []\n")); err == nil {
		t.Error("Parse accepted an invalid duration")
	}
}

// TestParseEmptyDocument proves an empty spec surfaces as a clear error.
func TestParseEmptyDocument(t *testing.T) {
	if _, err := Parse(nil); err == nil || !strings.Contains(err.Error(), "check-spec") {
		t.Errorf("Parse(nil) err = %v, want a check-spec error", err)
	}
}

// TestUnknownCheckTypeErrorMessage pins the forward-compatibility contract:
// the error names the CLI version and suggests `grid upgrade`.
func TestUnknownCheckTypeErrorMessage(t *testing.T) {
	err := UnknownCheckTypeError("quantum.entangled")
	msg := err.Error()
	if !strings.Contains(msg, buildinfo.Version) {
		t.Errorf("error %q does not name the CLI version %q", msg, buildinfo.Version)
	}
	if !strings.Contains(msg, "grid upgrade") {
		t.Errorf("error %q does not suggest `grid upgrade`", msg)
	}
}

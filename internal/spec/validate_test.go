package spec

import (
	"strings"
	"testing"
)

// validSpec returns a minimal, structurally valid spec for mutation tests.
func validSpec() *CheckSpec {
	return &CheckSpec{
		Version: 1,
		Workdir: ".",
		Checks: []Check{
			{ID: "a", Type: "file.exists", Path: "a.txt"},
			{ID: "b", Type: "cmd.exit_code", Run: []string{"go", "version"}, DependsOn: []string{"a"}},
		},
	}
}

func TestValidateAcceptsValidSpec(t *testing.T) {
	if err := Validate(validSpec()); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateErrors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*CheckSpec)
		want   string
	}{
		{
			name:   "missing check id",
			mutate: func(cs *CheckSpec) { cs.Checks[0].ID = "" },
			want:   "id must not be empty",
		},
		{
			name: "duplicate check ids",
			mutate: func(cs *CheckSpec) {
				cs.Checks[1].ID = "a"
				cs.Checks[1].DependsOn = nil
			},
			want: "duplicate check id",
		},
		{
			name:   "unknown check type",
			mutate: func(cs *CheckSpec) { cs.Checks[0].Type = "ai.vibes" },
			want:   "unknown check type",
		},
		{
			name: "unknown check type carries upgrade hint",
			mutate: func(cs *CheckSpec) {
				cs.Checks[0].Type = "ai.vibes"
			},
			want: "grid upgrade",
		},
		{
			name: "depends_on references unknown check",
			mutate: func(cs *CheckSpec) {
				cs.Checks[1].DependsOn = []string{"ghost"}
			},
			want: "depends_on references unknown check",
		},
		{
			name:   "cmd type without run",
			mutate: func(cs *CheckSpec) { cs.Checks[1].Run = nil },
			want:   "run must be a non-empty argv array",
		},
		{
			name:   "file type without path",
			mutate: func(cs *CheckSpec) { cs.Checks[0].Path = "" },
			want:   "path must be set",
		},
		{
			name:   "http.request without url",
			mutate: func(cs *CheckSpec) { cs.Checks[0].Type, cs.Checks[0].Path = "http.request", "" },
			want:   "url must be set",
		},
		{
			name:   "env.var_set without name",
			mutate: func(cs *CheckSpec) { cs.Checks[0].Type, cs.Checks[0].Path = "env.var_set", "" },
			want:   "name must be set",
		},
		{
			name:   "tool.installed without name",
			mutate: func(cs *CheckSpec) { cs.Checks[0].Type, cs.Checks[0].Path = "tool.installed", "" },
			want:   "name must be set",
		},
		{
			name: "backslash in path",
			mutate: func(cs *CheckSpec) {
				cs.Checks[0].Path = `internal\resp\parser.go`
			},
			want: "path must use forward slashes",
		},
		{
			name: "backslash in cwd",
			mutate: func(cs *CheckSpec) {
				cs.Checks[1].Cwd = `cmd\server`
			},
			want: "cwd must use forward slashes",
		},
		{
			name:   "backslash in workdir",
			mutate: func(cs *CheckSpec) { cs.Workdir = `c:\ws` },
			want:   "workdir must use forward slashes",
		},
		{
			name: "unsupported grammar version",
			mutate: func(cs *CheckSpec) {
				cs.Version = 2
			},
			want: "check-spec version 2 is not supported",
		},
		{
			name: "setup step without run",
			mutate: func(cs *CheckSpec) {
				cs.Setup = []SetupStep{{Run: nil}}
			},
			want: "setup step 0: run must be a non-empty argv array",
		},
		{
			name: "service without run",
			mutate: func(cs *CheckSpec) {
				cs.Services = []Service{{ID: "srv", Run: nil}}
			},
			want: "service 0: run must be a non-empty argv array",
		},
		{
			name: "duplicate service ids",
			mutate: func(cs *CheckSpec) {
				cs.Services = []Service{
					{ID: "srv", Run: []string{"a"}},
					{ID: "srv", Run: []string{"b"}},
				}
			},
			want: "duplicate service id",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := validSpec()
			tc.mutate(cs)
			err := Validate(cs)
			if err == nil {
				t.Fatalf("Validate accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Validate error %q does not contain %q", err, tc.want)
			}
		})
	}
}

// TestValidateDoesNotRequireJudgeOnlyFields proves the public spec (no
// pattern, equals, sha256, value, must_include, expect, expect_exit anywhere)
// validates — validation must never demand an expected value.
func TestValidateDoesNotRequireJudgeOnlyFields(t *testing.T) {
	cs := validSpec()
	cs.Checks = []Check{
		{ID: "fe", Type: "file.exists", Path: "a.txt"},
		{ID: "fc", Type: "file.contains", Path: "a.txt"},
		{ID: "fh", Type: "file.hash", Path: "a.txt"},
		{ID: "de", Type: "dir.exists", Path: "d"},
		{ID: "dt", Type: "dir.tree", Path: "d"},
		{ID: "ce", Type: "cmd.exit_code", Run: []string{"true"}},
		{ID: "sm", Type: "cmd.stdout_matches", Run: []string{"true"}},
		{ID: "se", Type: "cmd.stdout_equals", Run: []string{"true"}},
		{ID: "em", Type: "cmd.stderr_matches", Run: []string{"true"}},
		{ID: "hr", Type: "http.request", Method: "GET", URL: "http://x/y"},
		{ID: "jf", Type: "json.file", Path: "p.json", JSONPath: "$.a"},
		{ID: "gc", Type: "git.has_commit"},
		{ID: "gt", Type: "git.file_tracked", Path: "a.txt"},
		{ID: "ev", Type: "env.var_set", Name: "HOME"},
		{ID: "ti", Type: "tool.installed", Name: "go"},
	}
	cs.Setup = []SetupStep{{Run: []string{"go", "version"}}} // public form: no expect_exit
	if err := Validate(cs); err != nil {
		t.Fatalf("Validate public-shaped spec: %v", err)
	}
}

// TestValidateAllKnownTypes proves every one of §4's 15 type strings is
// accepted as known.
func TestValidateAllKnownTypes(t *testing.T) {
	cs := validSpec()
	cs.Checks = nil
	for i, typ := range KnownTypes {
		c := Check{ID: typ, Type: typ}
		switch {
		case cmdTypes[typ]:
			c.Run = []string{"true"}
		case pathTypes[typ]:
			c.Path = "a.txt"
		case typ == "http.request":
			c.Method, c.URL = "GET", "http://x/y"
		case typ == "env.var_set" || typ == "tool.installed":
			c.Name = "x"
		}
		_ = i
		cs.Checks = append(cs.Checks, c)
	}
	if err := Validate(cs); err != nil {
		t.Fatalf("Validate all-15-types spec: %v", err)
	}
}

// TestValidateAggregatesProblems proves multiple problems surface together.
func TestValidateAggregatesProblems(t *testing.T) {
	cs := validSpec()
	cs.Checks[0].ID = ""
	cs.Checks[1].Run = nil
	err := Validate(cs)
	if err == nil {
		t.Fatal("Validate accepted a broken spec")
	}
	for _, want := range []string{"id must not be empty", "run must be a non-empty argv array"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

package spec

import (
	"errors"
	"fmt"
	"strings"

	"github.com/anshul-kh/grid-cli/internal/buildinfo"
)

// KnownTypes lists every check type in GRID_SPEC §4's check-type table, in
// table order.
var KnownTypes = []string{
	"file.exists",
	"file.contains",
	"file.hash",
	"dir.exists",
	"dir.tree",
	"cmd.exit_code",
	"cmd.stdout_matches",
	"cmd.stdout_equals",
	"cmd.stderr_matches",
	"http.request",
	"json.file",
	"git.has_commit",
	"git.file_tracked",
	"env.var_set",
	"tool.installed",
}

var knownTypes = func() map[string]bool {
	m := make(map[string]bool, len(KnownTypes))
	for _, t := range KnownTypes {
		m[t] = true
	}
	return m
}()

// IsKnownType reports whether typeName is one of §4's check types.
func IsKnownType(typeName string) bool { return knownTypes[typeName] }

// UnknownCheckTypeError is the forward-compatibility error for a check type
// this CLI cannot measure: the message names the CLI version and suggests
// `grid upgrade` (TASK.md §4), because the server may ship a new check type
// before the user updates. Both the spec validator and the checker registry
// return it.
func UnknownCheckTypeError(typeName string) error {
	return fmt.Errorf(
		"unknown check type %q: this grid-cli (version %s) does not know how to measure it — run `grid upgrade` to get a newer CLI, then try again",
		typeName, buildinfo.Version)
}

// unsupportedVersionError guards the §4 grammar version the same way: a spec
// written for a newer grammar must not run half-understood.
func unsupportedVersionError(version int) error {
	return fmt.Errorf(
		"check-spec version %d is not supported by this grid-cli (version %s) — run `grid upgrade` to get a newer CLI, then try again",
		version, buildinfo.Version)
}

// cmdTypes are the check families whose measurement is a command: `run` must
// be a non-empty argv slice.
var cmdTypes = map[string]bool{
	"cmd.exit_code":      true,
	"cmd.stdout_matches": true,
	"cmd.stdout_equals":  true,
	"cmd.stderr_matches": true,
}

// pathTypes require a `path` to measure (git.has_commit's path is optional —
// it defaults to the workdir).
var pathTypes = map[string]bool{
	"file.exists":      true,
	"file.contains":    true,
	"file.hash":        true,
	"dir.exists":       true,
	"dir.tree":         true,
	"json.file":        true,
	"git.file_tracked": true,
}

// Validate performs structural validation of a decoded CheckSpec (the public
// or the judge-complete shape — judge-only expected values are never
// required). It returns an error joining every problem found:
//   - version must be 1 (§4 "grammar v1")
//   - check ids are non-empty and unique
//   - every check type is one of §4's (unknown → versioned `grid upgrade` error)
//   - depends_on references declared ids
//   - cmd-family checks have a non-empty run; path types have a path;
//     http.request has method+url; env.var_set/tool.installed have a name
//   - spec paths (workdir, path, cwd) use forward slashes only (§4 rule 2)
//   - setup steps and services have non-empty run; service ids are unique
func Validate(cs *CheckSpec) error {
	var errs []error

	if cs.Version != 1 {
		errs = append(errs, unsupportedVersionError(cs.Version))
	}
	if strings.ContainsRune(cs.Workdir, '\\') {
		errs = append(errs, errors.New(`workdir must use forward slashes (spec paths are converted per platform): `+fmt.Sprintf("%q", cs.Workdir)))
	}

	for i, step := range cs.Setup {
		if len(step.Run) == 0 {
			errs = append(errs, fmt.Errorf("setup step %d: run must be a non-empty argv array", i))
		}
	}

	serviceIDs := make(map[string]bool, len(cs.Services))
	for i, svc := range cs.Services {
		if svc.ID == "" {
			errs = append(errs, fmt.Errorf("service %d: id must not be empty", i))
		} else if serviceIDs[svc.ID] {
			errs = append(errs, fmt.Errorf("service %q: duplicate service id", svc.ID))
		} else {
			serviceIDs[svc.ID] = true
		}
		if len(svc.Run) == 0 {
			errs = append(errs, fmt.Errorf("service %d: run must be a non-empty argv array", i))
		}
	}

	ids := make(map[string]bool, len(cs.Checks))
	for _, c := range cs.Checks {
		ids[c.ID] = true
	}

	// Extension pins (§4/A1): every pin must be fully shaped and unique —
	// the cache and mirror paths are built from these values, so a sloppy
	// name/version must never reach the filesystem layer.
	pinned := make(map[string]string, len(cs.Extensions))
	pinsSeen := make(map[string]bool, len(cs.Extensions))
	for _, p := range cs.Extensions {
		if p.Name == "" || p.Version == "" {
			errs = append(errs, fmt.Errorf("extension pin %q: name and version must both be set", p.Name))
			continue
		}
		if pinsSeen[p.Name] {
			errs = append(errs, fmt.Errorf("extension %q: duplicate pin", p.Name))
			continue
		}
		pinsSeen[p.Name] = true
		pinned[p.Name] = p.Version
	}

	for i, c := range cs.Checks {
		label := fmt.Sprintf("check %d", i)
		if c.ID != "" {
			label = fmt.Sprintf("check %q", c.ID)
		}
		if c.ID == "" {
			errs = append(errs, fmt.Errorf("check %d: id must not be empty", i))
		} else if ids[c.ID] && countID(cs.Checks, c.ID) > 1 {
			errs = append(errs, fmt.Errorf("check %q: duplicate check id", c.ID))
		}
		if name, _, ok := ParseExtType(c.Type); ok {
			// ext.<name>.<action> (§4/A1): the extension must be pinned,
			// and a pinned name only counts when it is fully shaped (the
			// pin loop above records those).
			if _, ok := pinned[name]; !ok {
				if pinsSeen[name] {
					errs = append(errs, fmt.Errorf("%s: extension %q is pinned without a version — set both name and version in the spec's extensions block", label, name))
				} else {
					errs = append(errs, fmt.Errorf("%s: type %q names extension %q, which the spec does not pin in its extensions block", label, c.Type, name))
				}
			}
		} else if !IsKnownType(c.Type) {
			errs = append(errs, fmt.Errorf("%s: %w", label, UnknownCheckTypeError(c.Type)))
		}
		for _, dep := range c.DependsOn {
			if !ids[dep] {
				errs = append(errs, fmt.Errorf("%s: depends_on references unknown check %q", label, dep))
			}
		}
		if cmdTypes[c.Type] && len(c.Run) == 0 {
			errs = append(errs, fmt.Errorf("%s: run must be a non-empty argv array", label))
		}
		if pathTypes[c.Type] && c.Path == "" {
			errs = append(errs, fmt.Errorf("%s: path must be set", label))
		}
		if c.Type == "http.request" {
			if c.Method == "" {
				errs = append(errs, fmt.Errorf("%s: method must be set", label))
			}
			if c.URL == "" {
				errs = append(errs, fmt.Errorf("%s: url must be set", label))
			}
		}
		if (c.Type == "env.var_set" || c.Type == "tool.installed") && c.Name == "" {
			errs = append(errs, fmt.Errorf("%s: name must be set", label))
		}
		if strings.ContainsRune(c.Path, '\\') {
			errs = append(errs, fmt.Errorf("%s: path must use forward slashes (spec paths are converted per platform): %q", label, c.Path))
		}
		if strings.ContainsRune(c.Cwd, '\\') {
			errs = append(errs, fmt.Errorf("%s: cwd must use forward slashes (spec paths are converted per platform): %q", label, c.Cwd))
		}
	}

	return errors.Join(errs...)
}

func countID(checks []Check, id string) int {
	n := 0
	for _, c := range checks {
		if c.ID == id {
			n++
		}
	}
	return n
}

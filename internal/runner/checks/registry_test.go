package checks

import (
	"context"
	"strings"
	"testing"

	"github.com/anshul-kh/grid-cli/internal/buildinfo"
	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-cli/internal/spec"
)

// TestLookupUnknownTypeCarriesUpgradeHint proves that until pass 5 registers
// real checkers, *every* lookup — including §4's own types — returns the
// forward-compatibility error naming the CLI version and suggesting
// `grid upgrade` (plan pass 4 item 9): the registry must never silently
// return a nil Checker or a bare "not found".
func TestLookupUnknownTypeCarriesUpgradeHint(t *testing.T) {
	reg := NewRegistry()

	for _, typeName := range []string{"file.exists", "cmd.exit_code", "totally.unknown"} {
		c, err := reg.Lookup(typeName)
		if c != nil {
			t.Errorf("Lookup(%q) returned a Checker before pass 5 registers any", typeName)
		}
		if err == nil {
			t.Fatalf("Lookup(%q): want error, got nil", typeName)
		}
		if !strings.Contains(err.Error(), buildinfo.Version) {
			t.Errorf("Lookup(%q) error %q does not name the CLI version %q", typeName, err.Error(), buildinfo.Version)
		}
		if !strings.Contains(err.Error(), "grid upgrade") {
			t.Errorf("Lookup(%q) error %q does not suggest `grid upgrade`", typeName, err.Error())
		}
		if !strings.Contains(err.Error(), typeName) {
			t.Errorf("Lookup(%q) error %q does not name the offending type", typeName, err.Error())
		}
	}
}

// TestRegisterThenLookup proves a registered type resolves to its Checker
// instead of the upgrade-hint error, and Registered() lists it sorted.
func TestRegisterThenLookup(t *testing.T) {
	reg := NewRegistry()
	var stub Checker = checkerFunc(func(context.Context, spec.Check, *runner.Env) (runner.Evidence, error) {
		return runner.Evidence{"exists": true}, nil
	})
	reg.Register("file.exists", stub)

	got, err := reg.Lookup("file.exists")
	if err != nil {
		t.Fatalf("Lookup(%q): unexpected error: %v", "file.exists", err)
	}
	if got == nil {
		t.Fatal("Lookup(\"file.exists\") = nil Checker, want the registered stub")
	}

	if _, err := reg.Lookup("dir.exists"); err == nil {
		t.Fatal("Lookup(\"dir.exists\"): want upgrade-hint error for an unregistered type, got nil")
	}

	if reg := reg.Registered(); len(reg) != 1 || reg[0] != "file.exists" {
		t.Errorf("Registered() = %v, want [file.exists]", reg)
	}
}

// checkerFunc adapts a func to the Checker interface for tests.
type checkerFunc func(ctx context.Context, c spec.Check, env *runner.Env) (runner.Evidence, error)

func (f checkerFunc) Measure(ctx context.Context, c spec.Check, env *runner.Env) (runner.Evidence, error) {
	return f(ctx, c, env)
}

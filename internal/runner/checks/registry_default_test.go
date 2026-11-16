package checks

import (
	"testing"
)

// all15CheckTypes is GRID_SPEC §4's complete check-type table, used to
// prove NewDefaultRegistry registers exactly these (plan pass 5 item 9).
var all15CheckTypes = []string{
	"file.exists", "file.contains", "file.hash",
	"dir.exists", "dir.tree",
	"cmd.exit_code", "cmd.stdout_matches", "cmd.stdout_equals", "cmd.stderr_matches",
	"http.request",
	"json.file",
	"git.has_commit", "git.file_tracked",
	"env.var_set",
	"tool.installed",
}

// TestNewDefaultRegistryRegistersAll15Types proves every §4 type resolves
// to a real Checker (not the pass-4 upgrade-hint error), and that nothing
// extra sneaks in.
func TestNewDefaultRegistryRegistersAll15Types(t *testing.T) {
	reg := NewDefaultRegistry()

	if got := reg.Registered(); len(got) != len(all15CheckTypes) {
		t.Fatalf("Registered() has %d types, want exactly the 15 in GRID_SPEC §4: %v", len(got), got)
	}
	for _, typeName := range all15CheckTypes {
		c, err := reg.Lookup(typeName)
		if err != nil {
			t.Errorf("Lookup(%q): unexpected error: %v", typeName, err)
		}
		if c == nil {
			t.Errorf("Lookup(%q) = nil Checker", typeName)
		}
	}
}

// TestNewDefaultRegistryStillRejectsUnknownTypes proves pass 4's
// forward-compatibility error survives pass 5 registering everything it
// knows about — a type outside §4 entirely (or a future §4 addition this
// binary predates) must still name the CLI version and suggest
// `grid upgrade`.
func TestNewDefaultRegistryStillRejectsUnknownTypes(t *testing.T) {
	reg := NewDefaultRegistry()
	if _, err := reg.Lookup("future.check_type"); err == nil {
		t.Fatal("Lookup(\"future.check_type\"): want the upgrade-hint error, got nil")
	}
}

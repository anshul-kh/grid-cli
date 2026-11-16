package upgrade

import (
	"runtime"
	"testing"
)

// TestDetectPackageManagerHomebrewPaths exercises the Homebrew path
// heuristic with fake paths that need not exist on disk — EvalSymlinks
// fails silently on a nonexistent path and detectPackageManager falls back
// to the given string, which is all the (path-based, no stat) Homebrew
// check needs.
func TestDetectPackageManagerHomebrewPaths(t *testing.T) {
	neverCalled := func(string) bool {
		t.Helper()
		t.Fatal("the owner check must not run for a Homebrew path")
		return false
	}
	cases := []string{
		"/opt/homebrew/bin/grid",
		"/usr/local/Cellar/grid-cli/1.2.0/bin/grid",
	}
	for _, p := range cases {
		manager, managed := detectPackageManager(p, neverCalled)
		if !managed || manager != "Homebrew" {
			t.Errorf("detectPackageManager(%q) = (%q, %v), want (Homebrew, true)", p, manager, managed)
		}
	}
}

func TestDetectPackageManagerRootOwnedUsrLocalBin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the /usr/local/bin root-owned convention is unix-only")
	}
	manager, managed := detectPackageManager("/usr/local/bin/grid", func(string) bool { return true })
	if !managed || manager == "" {
		t.Errorf("detectPackageManager with a root-owned /usr/local/bin = (%q, %v), want managed", manager, managed)
	}
}

func TestDetectPackageManagerUsrLocalBinNotRootOwnedIsUnmanaged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the /usr/local/bin root-owned convention is unix-only")
	}
	manager, managed := detectPackageManager("/usr/local/bin/grid", func(string) bool { return false })
	if managed {
		t.Errorf("detectPackageManager with a non-root-owned /usr/local/bin = (%q, %v), want not managed", manager, managed)
	}
}

func TestDetectPackageManagerOrdinaryInstallIsUnmanaged(t *testing.T) {
	manager, managed := detectPackageManager("/home/user/.local/bin/grid", func(string) bool { return false })
	if managed {
		t.Errorf("detectPackageManager(ordinary path) = (%q, %v), want not managed", manager, managed)
	}
}

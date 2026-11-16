package upgrade

import (
	"path/filepath"
	"runtime"
	"strings"
)

// DetectPackageManager reports whether execPath looks like a system
// package-manager install `grid upgrade` must not fight with: Homebrew
// (under /opt/homebrew or /usr/local/Cellar — path-based, no stat needed)
// or a root-owned binary under /usr/local/bin (the classic Linux
// package-manager convention). It returns the manager's display name and
// true when managed, "" and false otherwise.
func DetectPackageManager(execPath string) (manager string, managed bool) {
	return detectPackageManager(execPath, isRootOwned)
}

// detectPackageManager is the pure core DetectPackageManager wraps: the
// owner check is injected so tests can fake "/usr/local/bin/grid is
// root-owned" without creating a real root-owned file.
func detectPackageManager(execPath string, rootOwned func(string) bool) (string, bool) {
	resolved := execPath
	if real, err := filepath.EvalSymlinks(execPath); err == nil {
		resolved = real
	}
	resolved = filepath.ToSlash(resolved)

	if strings.Contains(resolved, "/opt/homebrew/") || strings.Contains(resolved, "/usr/local/Cellar/") {
		return "Homebrew", true
	}
	if runtime.GOOS != "windows" && strings.HasPrefix(resolved, "/usr/local/bin/") && rootOwned(resolved) {
		return "a system package manager", true
	}
	return "", false
}

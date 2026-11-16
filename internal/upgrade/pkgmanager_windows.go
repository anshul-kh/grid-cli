//go:build windows

package upgrade

// isRootOwned is meaningless on Windows (ACLs govern ownership, not a
// POSIX uid 0); the /usr/local/bin convention it backs is unix-only too,
// so DetectPackageManager never reaches this branch on Windows in
// practice. Defined for completeness (TASK.md §5's Windows ACL note).
func isRootOwned(path string) bool { return false }

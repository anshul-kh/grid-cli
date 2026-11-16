//go:build !windows

package upgrade

import (
	"os"
	"syscall"
)

// isRootOwned reports whether path's file owner is uid 0 — the signal a
// root-owned /usr/local/bin binary gives that it was placed there by a
// system package manager, not grid upgrade itself.
func isRootOwned(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return st.Uid == 0
}

// Package buildinfo holds the version metadata injected at build time via
// -ldflags "-X github.com/anshul-kh/grid-cli/internal/buildinfo.<Var>=...".
//
// The module path appears here and in go.mod only (TASK.md §2), so a future
// org rename is a one-line change in each place.
package buildinfo

import (
	"fmt"
	"runtime"
)

// Build metadata. Overridden at release time by goreleaser ldflags
// (TASK.md §10); the defaults describe a development build.
var (
	// Version is the semantic version of the CLI ("dev" for local builds).
	Version = "dev"
	// Commit is the git commit hash the binary was built from ("none" for
	// local builds).
	Commit = "none"
	// Date is the build date ("unknown" for local builds).
	Date = "unknown"
	// RepoURL is the public repository the binary is released from; grid
	// upgrade consults it for the latest release.
	RepoURL = "https://github.com/anshul-kh/grid-cli"
)

// UserAgent returns the HTTP User-Agent string every request must carry
// (GRID_SPEC §5): grid-cli/<version> (<os>; <arch>).
func UserAgent() string {
	return fmt.Sprintf("grid-cli/%s (%s; %s)", Version, runtime.GOOS, runtime.GOARCH)
}

// Package workspace — this file (mode.go) owns workspace.mode (GRID_SPEC
// §4 "Workspace modes"): the server's declaration of what environment a
// lesson's checks need before setup[]/services[]/checks[] run. Like every
// other field in the public check-spec, it is environment, never
// assertion — the CLI builds what it says, it never uses it to infer a
// verdict.
package workspace

// Mode is the workspace environment a lesson declares (GRID_SPEC §4).
type Mode string

const (
	// ModeNone creates nothing at all — checks run in a throwaway temp cwd.
	// Use for lessons whose checks reference no path/cwd (e.g. "is Docker
	// installed").
	ModeNone Mode = "none"
	// ModeFolder creates a plain directory — today's v1 default.
	ModeFolder Mode = "folder"
	// ModeRepo creates a directory and runs `git init` inside it — required
	// by any check-spec containing a git.* check.
	ModeRepo Mode = "repo"
	// ModeBundle creates a directory, a git repo, and unpacks a
	// seed-generated starter archive into it (P20). Until that lands, grid
	// run treats it the same as ModeRepo.
	ModeBundle Mode = "bundle"
)

// ParseMode maps the server's workspace.mode string to a Mode. An unknown
// or empty value defaults to ModeFolder — GRID_SPEC §4's own default when
// the workspace: block is absent entirely, and the safe choice for a value
// this CLI doesn't recognise: ModeFolder scaffolds strictly less than
// ModeRepo/ModeBundle (no git, no bundle fetch), so a mode this CLI has
// never heard of — a future addition from a newer server — never escalates
// to behaviour this build wasn't written to perform safely.
func ParseMode(s string) Mode {
	switch Mode(s) {
	case ModeNone, ModeFolder, ModeRepo, ModeBundle:
		return Mode(s)
	default:
		return ModeFolder
	}
}

// CreatesDir reports whether this mode scaffolds a directory at all. False
// only for ModeNone — the entire point of that mode is that `grid run`
// creates nothing in the invocation directory.
func (m Mode) CreatesDir() bool {
	return m != ModeNone
}

// NeedsGit reports whether this mode initialises (ModeRepo) or ships
// (ModeBundle) a git repository.
func (m Mode) NeedsGit() bool {
	return m == ModeRepo || m == ModeBundle
}

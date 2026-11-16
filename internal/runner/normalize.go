package runner

import "strings"

// NormalizeOutput is the byte-identical twin of grid-backend's
// src/modules/submissions/normalize.ts::normalizeOutput (GRID_SPEC §4
// cross-platform rule 3 + §5.3 addendum). It is applied to captured command
// stdout/stderr, file excerpts, and http response bodies before they are
// uploaded or compared:
//
//  1. CRLF → LF, and a bare CR (no following LF) also folds to LF —
//     carriage-return-driven output (progress bars) must normalise the same
//     way CRLF does, or the same logical output diverges byte-for-byte
//     depending on which OS captured it.
//  2. Trailing spaces and tabs stripped per line — deliberately only
//     [ \t], never broader Unicode whitespace.
//  3. Any run of trailing newlines trimmed entirely (trailing blank lines
//     collapse to nothing; internal blank lines are preserved).
//
// The cross-repo proof is the shared fixture suite: the 10 pairs in
// grid-backend/tests/fixtures/normalize/cases.json are reproduced verbatim
// as testdata/normalize/*.in.txt / *.expected.txt pairs here, and the test
// suite walks them byte-for-byte. If either side's normalisation ever
// changes, the fixtures change first and both sides are re-verified — see
// the sync-file CONTRACT entry pinning the pairs.
//
// Note for pattern matching (pass 5): every text-matching check
// (file.contains, cmd.stdout_matches, cmd.stderr_matches,
// http.request body_matches, tool.installed version_matches,
// git.has_commit message_matches) must compile its regex with the multiline
// flag ALWAYS on — ^/$ anchor individual lines of a possibly multi-line
// value, not the whole string — plus case-insensitivity when the check sets
// ignore_case. Mirrors normalize.ts::compilePattern(pattern, ignoreCase).
func NormalizeOutput(input []byte) []byte {
	s := string(input)
	// 1. CRLF → LF, then any surviving bare CR → LF.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")

	// 2. Strip trailing [ \t] per line.
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	s = strings.Join(lines, "\n")

	// 3. Trim the run of trailing newlines entirely.
	s = strings.TrimRight(s, "\n")
	return []byte(s)
}

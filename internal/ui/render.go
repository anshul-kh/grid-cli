// Package ui renders all human-facing output: colour theming, tables,
// spinners, verdict rendering, and the --json machine output helpers.
//
// This file (render.go) owns `grid check`'s measurement table and `grid
// submit`'s verdict rendering (TASK.md §7 UX quality bar). The two are
// deliberately different shapes: grid check can only report what it
// measured (ok/error/skipped — GRID_SPEC §1, never a verdict), while grid
// submit renders the *server's* judgement, where ✔/✖ icons are allowed
// (TASK.md pass 7 item 4: "these icons are the server's verdict, which is
// allowed"). Grepping this file for "passed"/"failed" should find them only
// inside doc comments and the server-response rendering path — never a
// locally-derived verdict.
package ui

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/anshul-kh/grid-cli/internal/api"
	"github.com/anshul-kh/grid-extensions/pkg/attest"
)

// MeasurementRow is one row of `grid check`'s measurement table: what was
// measured, its local status (ok | error | skipped — never pass/fail), and
// how long it took.
type MeasurementRow struct {
	Title      string
	Status     api.CheckStatus
	DurationMS int64
	// Observed is a short summary of what the checker actually measured
	// ("exists=false", "exit=1"). Empty when there is nothing useful to show.
	Observed string
}

// measurementLabel renders a MeasurementRow's status as plain, unambiguous
// text — never ✔/✖, which TASK.md pass 7 item 4 reserves for the server's
// verdict — so `grid check`'s table cannot be mistaken for a pass/fail
// result.
func measurementLabel(theme *Theme, status api.CheckStatus) string {
	switch status {
	case api.CheckStatusOK:
		// Deliberately NOT green, and deliberately not the word "ok".
		// "ok" in green beside a check's title reads as "this check passed",
		// and it does not mean that — it means the measurement was taken.
		// A successful measurement of "the file is missing" was being shown
		// as a green ok next to "file exists", which is the opposite of the
		// truth. "measured" cannot be misread that way.
		return theme.Dim("measured")
	case api.CheckStatusError:
		return theme.Red("error")
	case api.CheckStatusSkipped:
		return theme.Dim("skipped")
	default:
		return string(status)
	}
}

// RenderMeasurements writes `grid check`'s per-row measurement table, each
// row indented by the shared Gutter (layout.go) rather than a hardcoded
// "  ".
func RenderMeasurements(w io.Writer, theme *Theme, rows []MeasurementRow) {
	for _, row := range rows {
		observed := ""
		if row.Observed != "" {
			observed = " " + theme.Dim("→ "+row.Observed)
		}
		line := fmt.Sprintf("%-14s %-44s%s %s",
			measurementLabel(theme, row.Status),
			Truncate(row.Title, 44),
			observed,
			theme.Dim(fmt.Sprintf("%dms", row.DurationMS)))
		fmt.Fprintln(w, Indent(line))
	}
}

// MeasurementFooter writes `grid check`'s explicit "it cannot judge" line
// (TASK.md §1): ok of total measured, and the exact next command. The
// blank line that used to separate this from the table above it is now
// the caller's Flow's job (layout.go) — this writes only its own two
// lines, so a caller that doesn't want that blank (grid status, reusing
// the same footer right under its own table) doesn't have to undo one.
func MeasurementFooter(w io.Writer, theme *Theme, ok, total int) {
	fmt.Fprintf(w, "%s\n", theme.Dim(fmt.Sprintf("measured %d/%d — these are observations, not a verdict", ok, total)))
	fmt.Fprintf(w, "%s\n", theme.Dim("run `grid submit` to have the server judge them"))
}

// RenderVerdictBanner writes the server's top-level verdict word (PASSED,
// FAILED, or INVALID) — this and the check-table icons are the only places
// `grid submit` is allowed to show a verdict, because both render the
// *server's* judgement (api.SubmissionResponse.Verdict), never one derived
// locally (TASK.md pass 7 item 4).
func RenderVerdictBanner(w io.Writer, theme *Theme, verdict api.Verdict) {
	switch verdict {
	case api.VerdictPassed:
		fmt.Fprintln(w, theme.Green(string(verdict)))
	case api.VerdictFailed:
		fmt.Fprintln(w, theme.Red(string(verdict)))
	default: // api.VerdictInvalid, or anything this CLI doesn't recognise yet
		fmt.Fprintln(w, theme.Yellow(string(verdict)))
	}
}

// VerdictRow is one row of `grid submit`'s post-verdict check table: the
// server's judgement (Passed, Message, Hint) paired with the CLI's own
// local measurement status for that check, so a check the CLI never even
// measured (depends_on, a setup failure) renders as "not evaluated" rather
// than a bare ✖ the server's Passed=false alone wouldn't distinguish.
type VerdictRow struct {
	Title       string
	Passed      bool
	Message     string
	Hint        string
	LocalStatus api.CheckStatus
}

// Icon renders the row's status symbol. These are the server's verdict
// (✔/✖) — the one place the CLI is allowed to render pass/fail — or "–" for
// a check the CLI never measured at all, independent of whatever the server
// says about evidence it was never given.
func (r VerdictRow) Icon() string {
	if r.LocalStatus == api.CheckStatusSkipped {
		return "–"
	}
	if r.Passed {
		return "✔"
	}
	return "✖"
}

// BuildVerdictRows pairs the server's per-check judgement with the CLI's own
// submitted measurement status (attest.Result — the A4 envelope's results
// entry), by checkId.
func BuildVerdictRows(checks []api.SubmissionCheck, results []attest.Result) []VerdictRow {
	local := make(map[string]api.CheckStatus, len(results))
	for _, res := range results {
		local[res.CheckID] = api.CheckStatus(res.Status)
	}
	rows := make([]VerdictRow, len(checks))
	for i, c := range checks {
		rows[i] = VerdictRow{
			Title:       c.Title,
			Passed:      c.Passed,
			Message:     c.Message,
			Hint:        c.Hint,
			LocalStatus: local[c.CheckID],
		}
	}
	return rows
}

// coloredIcon applies the theme's colour to a verdict icon: green ✔, red
// ✖, dim –.
func coloredIcon(theme *Theme, icon string) string {
	switch icon {
	case "✔":
		return theme.Green(icon)
	case "✖":
		return theme.Red(icon)
	default:
		return theme.Dim(icon)
	}
}

// RenderVerdictTable writes `grid submit`'s check table: one ✔/✖/– row per
// server-judged check (TASK.md §7), each indented by the shared Gutter
// (layout.go) rather than a hardcoded "  ".
func RenderVerdictTable(w io.Writer, theme *Theme, rows []VerdictRow) {
	for _, row := range rows {
		fmt.Fprintln(w, Indent(fmt.Sprintf("%s  %s", coloredIcon(theme, row.Icon()), row.Title)))
	}
}

// RenderFailureDetails expands every row that isn't a ✔ with the server's
// message and hint (TASK.md §7: "failures expanded with the server's
// message and hint"), word-wrapped to the live render width (TASK.md §7
// pass 9: "wrap to min(terminalWidth, 100)") — a server-authored message or
// hint is free text with no length contract, unlike a check title. A blank
// line separates one failing check's block from the next, but never
// precedes the first — callers render this inside a "Failures" Section
// (layout.go), whose own header already supplies that separation.
func RenderFailureDetails(w io.Writer, theme *Theme, rows []VerdictRow) {
	width := RenderWidth()
	first := true
	for _, row := range rows {
		if row.Icon() == "✔" {
			continue
		}
		if !first {
			fmt.Fprintln(w)
		}
		first = false
		fmt.Fprintf(w, "%s %s\n", coloredIcon(theme, row.Icon()), theme.Bold(row.Title))
		if row.Message != "" {
			writeIndentedWrapped(w, row.Message, "  ", width)
		}
		if row.Hint != "" {
			writeHintWrapped(w, theme, row.Hint, width)
		}
	}
}

// writeIndentedWrapped word-wraps s to width (minus indent's own width)
// and writes one output line per wrapped line, each prefixed with indent.
func writeIndentedWrapped(w io.Writer, s, indent string, width int) {
	for _, line := range Wrap(s, width-len(indent)) {
		fmt.Fprintf(w, "%s%s\n", indent, line)
	}
}

// writeHintWrapped writes hint word-wrapped to width, labelled "hint:" on
// the first line and aligned under that same label on every continuation
// line.
func writeHintWrapped(w io.Writer, theme *Theme, hint string, width int) {
	const prefix = "  hint: "
	for i, line := range Wrap(hint, width-len(prefix)) {
		if i == 0 {
			fmt.Fprintf(w, "  %s %s\n", theme.Dim("hint:"), line)
			continue
		}
		fmt.Fprintf(w, "%s%s\n", strings.Repeat(" ", len(prefix)), line)
	}
}

// RenderXPLine writes the XP summary line: "+94 XP  ·  1,284 total  ·
// OPERATOR" (TASK.md §7). A zero or negative delta prints without a
// leading "+". The blank line that used to separate this from whatever
// came before it is now the caller's Flow's job (layout.go).
func RenderXPLine(w io.Writer, theme *Theme, xp api.XP, rank string) {
	sign := ""
	if xp.Delta > 0 {
		sign = "+"
	}
	fmt.Fprintf(w, "%s%d XP  ·  %s total", sign, xp.Delta, formatThousands(xp.Total))
	if rank != "" {
		fmt.Fprintf(w, "  ·  %s", theme.Cyan(rank))
	}
	fmt.Fprintln(w)
	if xp.PenaltyCapped {
		fmt.Fprintln(w, theme.Dim("  penalty capped (GRID_SPEC §3: repeated failed submits reduce XP, up to a daily cap)"))
	}
}

// RenderStreak writes the current streak line.
func RenderStreak(w io.Writer, theme *Theme, currentStreak int) {
	noun := "day"
	if currentStreak != 1 {
		noun = "days"
	}
	fmt.Fprintf(w, "%d-%s streak\n", currentStreak, noun)
}

// RenderNewBadges writes newly-unlocked badges and newly-completed
// achievements, if any (api.Unlocked: "newly-awarded since this call
// started, not a running total" per GRID_SYNC_CLAUDE.md 2026-11-12 06:00).
// An achievement whose Completed is false merely progressed and is not
// announced as "new" here — only a badge's unconditional presence, or an
// achievement actually finishing, counts.
func RenderNewBadges(w io.Writer, theme *Theme, unlocked api.Unlocked) {
	var names []string
	for _, b := range unlocked.Badges {
		name := b.Name
		if name == "" {
			name = b.Slug
		}
		names = append(names, name)
	}
	for _, a := range unlocked.Achievements {
		if !a.Completed {
			continue
		}
		names = append(names, a.Slug)
	}
	if len(names) == 0 {
		return
	}
	fmt.Fprintf(w, "%s %s\n", theme.Magenta("New:"), strings.Join(names, ", "))
}

// RenderNextLesson writes the next-lesson command, when the server
// suggested one. next is nil once there is no further lesson in course
// order (GRID_SYNC_CLAUDE.md 2026-11-12 06:00) — this prints nothing then.
// The blank line that used to separate this from whatever came before it
// is now the caller's Flow's job (layout.go); the run command is indented
// by the shared Gutter rather than a hardcoded "  ".
func RenderNextLesson(w io.Writer, theme *Theme, next *api.NextLesson) {
	if next == nil {
		return
	}
	fmt.Fprintf(w, "%s %s\n", theme.Dim("Next:"), next.Title)
	fmt.Fprintln(w, Indent(fmt.Sprintf("grid run %s", next.LessonID)))
}

// formatThousands inserts "," thousands separators into a non-negative
// integer's decimal representation ("1284" -> "1,284").
func formatThousands(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

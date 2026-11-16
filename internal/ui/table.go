// Package ui — this file (table.go) owns `grid courses`' table (one row
// per enrolled course, with an ASCII progress bar and mono (fixed-width)
// digits so the percentage column stays aligned — TASK.md §7 UX quality
// bar, pass 8) and the width-aware primitives every renderer in this
// package shares: RenderWidth (TASK.md §7 pass 9: "wrap to
// min(terminalWidth, 100); degrade gracefully at 80 columns"), Truncate,
// and Wrap.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// progressBarWidth is the fixed character width of the "[====----]" bar.
const progressBarWidth = 20

// ProgressBar renders a fixed-width ASCII progress bar for a 0-100
// percentage, clamping out-of-range input rather than panicking or
// rendering something misleading.
func ProgressBar(pct int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := progressBarWidth * pct / 100
	return "[" + strings.Repeat("=", filled) + strings.Repeat("-", progressBarWidth-filled) + "]"
}

// CourseRow is one row of `grid courses`' table.
type CourseRow struct {
	Title            string
	Difficulty       string
	EnrollmentStatus string
	ProgressPct      int
	LessonCount      int
}

// courseRowOverhead is every character of a courses row that is not the
// title: the 2-space indent, the 14-wide difficulty column and its
// separator, the bar (progressBarWidth plus its brackets) and its
// separator, the "100%" percentage and its separators, a generous
// "(99 lessons)" budget, and a generous enrollment-status budget.
// courseTitleWidth sizes the one unbounded column — the title — from
// whatever of the render budget this leaves.
const courseRowOverhead = 2 + 14 + 1 + (progressBarWidth + 2) + 1 + 4 + 2 + 13 + 2 + 11

// minCourseTitleWidth is the floor courseTitleWidth never shrinks the
// title column below — comfortably wider than any title in this repo's
// fixtures, so an ordinary course title is never touched; it exists to cap
// a pathologically long title (bad content, not a narrow terminal) rather
// than let it run the row out indefinitely.
const minCourseTitleWidth = 48

// courseTitleWidth sizes RenderCourses' title column from the live render
// budget: whatever courseRowOverhead leaves, floored at
// minCourseTitleWidth.
func courseTitleWidth(renderWidth int) int {
	if w := renderWidth - courseRowOverhead; w > minCourseTitleWidth {
		return w
	}
	return minCourseTitleWidth
}

// RenderCourses writes `grid courses`' table: title, difficulty, progress
// bar, mono-digit percentage, and lesson count, one enrolled course per
// row. The title is truncated (never wrapped — it is one table cell, not a
// paragraph) to courseTitleWidth so a pathologically long one cannot break
// the row's alignment or run past the render budget. Each row is indented
// by the shared Gutter (layout.go) rather than a hardcoded "  ", so the
// courses table's left margin never drifts from every other renderer's.
func RenderCourses(w io.Writer, theme *Theme, rows []CourseRow) {
	titleWidth := courseTitleWidth(RenderWidth())
	for _, row := range rows {
		line := fmt.Sprintf("%-*s %-14s %s %3d%%  (%d lessons)  %s",
			titleWidth, Truncate(row.Title, titleWidth), theme.Cyan(row.Difficulty),
			ProgressBar(row.ProgressPct), row.ProgressPct,
			row.LessonCount, theme.Dim(row.EnrollmentStatus))
		fmt.Fprintln(w, Indent(line))
	}
}

// Truncate shortens s to at most width runes, replacing the last one with
// "…" when it had to cut — the opposite of Wrap: a table cell truncates
// instead of growing the row onto a second line.
func Truncate(s string, width int) string {
	r := []rune(s)
	if width <= 0 || len(r) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return string(r[:width-1]) + "…"
}

// Wrap greedily word-wraps s to at most width characters per line —
// splitting on existing newlines first so a caller's own paragraph breaks
// survive — and never splits a word unless the word alone exceeds width,
// in which case it is hard-cut at width. This is the one wrapping
// primitive every long-text renderer in this package shares (failure
// messages/hints in render.go, the empty-state panel in box.go): TASK.md §7
// pass 9, "wrap to min(terminalWidth, 100); degrade gracefully at 80
// columns".
func Wrap(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, paragraph := range strings.Split(s, "\n") {
		out = append(out, wrapWords(paragraph, width)...)
	}
	return out
}

// wrapWords greedy-wraps one paragraph (no interior newlines) to width.
func wrapWords(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}
	lines := make([]string, 0, 1)
	cur := words[0]
	for _, word := range words[1:] {
		if len(cur)+1+len(word) <= width {
			cur += " " + word
			continue
		}
		lines = append(lines, cur)
		cur = word
	}
	lines = append(lines, cur)

	out := make([]string, 0, len(lines))
	for _, line := range lines {
		for len(line) > width {
			out = append(out, line[:width])
			line = line[width:]
		}
		out = append(out, line)
	}
	return out
}

// maxRenderWidth / nonTTYRenderWidth are TASK.md §7 pass 9's width rules:
// wrap to min(terminalWidth, 100); assume 80 when stdout is not a terminal
// at all (piped, redirected, or its size could not be read).
const (
	maxRenderWidth    = 100
	nonTTYRenderWidth = 80
)

// renderWidth is the pure, testable core of RenderWidth: min(termWidth,
// 100), falling back to nonTTYRenderWidth when ok is false (stdout is not
// a terminal) or termWidth is nonsensical.
func renderWidth(termWidth int, ok bool) int {
	if !ok || termWidth <= 0 {
		termWidth = nonTTYRenderWidth
	}
	if termWidth > maxRenderWidth {
		termWidth = maxRenderWidth
	}
	return termWidth
}

// RenderWidth reports the live rendering budget every width-aware
// renderer in this package targets: min(terminalWidth, 100), or 80 when
// stdout is not a terminal.
func RenderWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	return renderWidth(w, err == nil)
}

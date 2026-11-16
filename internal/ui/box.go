// Package ui — this file (box.go) owns box-drawing output. GRID_SPEC §10
// (the design system this CLI's "terminal-grammar" tone matches, TASK.md
// §7) permits box-drawing characters (─ │ ┌ ┐ └ ┘ ├ ┤) in section headers
// and empty states, and never as table layout — RenderCourses/
// RenderMeasurements/RenderVerdictTable (table.go/render.go) stay plain
// text rows for exactly that reason; this file is the one place the
// box-drawing characters appear.
package ui

import (
	"fmt"
	"io"
	"strings"
)

// Rule returns a horizontal box-drawing rule ("─" repeated width times) —
// a section header, never layout.
func Rule(width int) string {
	if width < 0 {
		width = 0
	}
	return strings.Repeat("─", width)
}

// RenderHeader writes a titled section header: the title, a space, and a
// box-drawing rule filling the rest of width (GRID_SPEC §10: "permitted in
// section headers ... never as layout"). The rule's width is
// layout.go's headerRuleWidth — this signature stays exactly as it was so
// every existing caller (and Flow.Section, which is now the normal way a
// command reaches this) keeps compiling unchanged.
func RenderHeader(w io.Writer, theme *Theme, title string, width int) {
	fmt.Fprintf(w, "%s %s\n", theme.Bold(title), theme.Dim(Rule(headerRuleWidth(title, width))))
}

// RenderEmptyState writes a dim, box-drawn panel around message, wrapped to
// width minus its own border (GRID_SPEC §10: "permitted ... in empty
// states"). It is the one place `grid courses`' "not enrolled yet" message
// (and any other empty list in this CLI) should go through, rather than a
// bare dim line, so an empty result reads as a deliberate state instead of
// output that looks cut short.
func RenderEmptyState(w io.Writer, theme *Theme, message string, width int) {
	const borders = 4 // "│ " + " │" either side of the text is 4 characters
	inner := width - borders
	if inner < 1 {
		inner = 1
	}
	lines := Wrap(message, inner)
	longest := 0
	for _, line := range lines {
		if len(line) > longest {
			longest = len(line)
		}
	}
	rule := Rule(longest + 2) // + 2 for the single space padding each side
	fmt.Fprintln(w, theme.Dim("┌"+rule+"┐"))
	for _, line := range lines {
		fmt.Fprintf(w, "%s %-*s %s\n", theme.Dim("│"), longest, line, theme.Dim("│"))
	}
	fmt.Fprintln(w, theme.Dim("└"+rule+"┘"))
}

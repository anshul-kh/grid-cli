// Package ui — this file (layout.go) owns every command's margins, section
// spacing, and key/value alignment: the one place a blank line or an
// indent gets decided, so cmd/*.go never hand-rolls its own
// fmt.Println("") or fmt.Printf("  %s") again. Everything here is built on
// top of this package's existing primitives (Rule, RenderHeader, Truncate)
// rather than beside them — there is still exactly one rule helper, one
// width calculator, and one table renderer in this package.
package ui

import (
	"fmt"
	"io"
	"strings"
)

// Gutter is the left margin, in spaces, every indented body line in this
// CLI's human output shares — the one number Indent, Section, and
// RenderCourses/RenderMeasurements/RenderVerdictTable all defer to, so an
// indent never drifts between renderers.
const Gutter = 2

// Indent prepends Gutter spaces to s. s may itself be several lines (a
// pre-rendered table, a wrapped paragraph) — every non-blank line gets the
// same prefix, so a caller never has to split and rejoin a multi-line
// block by hand just to indent it; a blank line inside the block stays
// blank rather than becoming a padded one.
func Indent(s string) string {
	if s == "" {
		return s
	}
	prefix := strings.Repeat(" ", Gutter)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

// headerRuleWidth is RenderHeader's rule-width arithmetic (box.go delegates
// to it): width, minus the title, minus the one space between title and
// rule, floored at zero so a title longer than width never asks Rule for a
// negative count.
func headerRuleWidth(title string, width int) int {
	rule := width - len([]rune(title)) - 1
	if rule < 0 {
		return 0
	}
	return rule
}

// Section writes a titled section to w: a blank line (omitted when nothing
// has reached w yet through this call — see Flow.Section, the one caller
// that matters, for how that state persists across more than one
// section), RenderHeader's title-and-rule line, and body's own output
// indented by Gutter. Nothing follows the body; any further spacing is the
// next Section/Blank/Line's job.
func Section(w io.Writer, theme *Theme, title string, width int, body func(io.Writer)) {
	NewFlow(w).Section(theme, title, width, body)
}

// Blank writes exactly one blank line, unconditionally. It is the raw
// primitive Flow builds on; a direct caller gets no collapsing of its
// own — reach for Flow instead when more than one call might land back to
// back, which is every command's cmd/*.go call site.
func Blank(w io.Writer) {
	fmt.Fprintln(w)
}

// Flow is the stateful wrapper every command's human-output path writes
// through (NewFlow(os.Stdout) once per command). It guarantees two
// invariants no single call can enforce on its own: no two blank lines
// ever land back to back, and the stream never ends on a blank line —
// both by deferring a requested blank until something real is written
// after it, and simply dropping it if nothing ever is.
type Flow struct {
	w       io.Writer
	started bool // true once anything has reached w through this Flow
	blank   bool // a Blank()/Section() is pending, flushed before the next write
}

// NewFlow returns a Flow writing to w.
func NewFlow(w io.Writer) *Flow {
	return &Flow{w: w}
}

// Blank requests a blank line before whatever is written next. Calling it
// several times in a row collapses to one blank line (not several), and
// calling it before anything has been written at all is a no-op — the
// leading-blank case this type exists to rule out.
func (f *Flow) Blank() {
	if !f.started {
		return
	}
	f.blank = true
}

// flush emits a pending blank line, if any, immediately before the next
// real write, then clears it.
func (f *Flow) flush() {
	if f.blank {
		Blank(f.w)
		f.blank = false
	}
}

// Line writes s as one unit after flushing any pending blank: ordinarily a
// single line, but s may itself contain embedded newlines (a rendered
// table, a multi-line block another ui renderer already produced) — Line
// does not care, it is just the thing that comes after the blank.
func (f *Flow) Line(s string) {
	f.flush()
	fmt.Fprintln(f.w, s)
	f.started = true
}

// Section writes a titled section: a blank line (unless this Flow has
// written nothing yet), RenderHeader's title-and-rule line, and body's own
// output indented by Gutter. This is the only way a command should emit a
// section.
func (f *Flow) Section(theme *Theme, title string, width int, body func(io.Writer)) {
	f.Blank()
	f.flush()
	RenderHeader(f.w, theme, title, width)
	var buf strings.Builder
	body(&buf)
	if rendered := strings.TrimRight(buf.String(), "\n"); rendered != "" {
		fmt.Fprintln(f.w, Indent(rendered))
	}
	f.started = true
}

// KeyValue writes an aligned two-column key/value block: keys dim, values
// plain, every value starting at the same column — one past the longest
// key — capped at RenderWidth()/3 so one abnormally long key cannot push
// every value halfway across the terminal.
func KeyValue(w io.Writer, theme *Theme, pairs [][2]string) {
	if len(pairs) == 0 {
		return
	}
	width := 0
	for _, kv := range pairs {
		if n := len([]rune(kv[0])); n > width {
			width = n
		}
	}
	if capWidth := RenderWidth() / 3; width > capWidth {
		width = capWidth
	}
	for _, kv := range pairs {
		key := fmt.Sprintf("%-*s", width, Truncate(kv[0], width))
		fmt.Fprintf(w, "%s %s\n", theme.Dim(key), kv[1])
	}
}

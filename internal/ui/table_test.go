package ui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRenderWidthPureCore(t *testing.T) {
	tests := []struct {
		name      string
		termWidth int
		ok        bool
		want      int
	}{
		{name: "not a terminal defaults to 80", termWidth: 0, ok: false, want: 80},
		{name: "GetSize failed defaults to 80", termWidth: 237, ok: false, want: 80},
		{name: "narrow terminal passes through", termWidth: 60, ok: true, want: 60},
		{name: "exactly 100 passes through", termWidth: 100, ok: true, want: 100},
		{name: "wide terminal clamps to 100", termWidth: 237, ok: true, want: 100},
		{name: "nonsensical zero width defaults to 80", termWidth: 0, ok: true, want: 80},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, renderWidth(tt.termWidth, tt.ok))
		})
	}
}

func TestTruncate(t *testing.T) {
	require.Equal(t, "hello", Truncate("hello", 10), "shorter than width is unchanged")
	require.Equal(t, "hello", Truncate("hello", 5), "exactly width is unchanged")
	require.Equal(t, "hell…", Truncate("hello world", 5))
	require.Equal(t, "…", Truncate("hello", 1))
	require.Equal(t, "hello", Truncate("hello", 0), "width<=0 is treated as unbounded, never panics")
}

// TestWrapStaysWithinRenderWidth is the plan's pinned criterion: wrapped
// output stays within min(terminalWidth, 100) for a fixture of long
// strings — including a single "word" far longer than the width, which
// Wrap must hard-cut rather than leave unbounded.
func TestWrapStaysWithinRenderWidth(t *testing.T) {
	longMessage := strings.Repeat("the evidence did not match the expected pattern ", 12)
	longSingleWord := strings.Repeat("x", 500)
	for _, width := range []int{80, 100} {
		for _, fixture := range []string{longMessage, longSingleWord, "line one\nline two is also fairly long here"} {
			lines := Wrap(fixture, width)
			if len(lines) == 0 {
				t.Fatalf("Wrap(%q, %d) returned no lines", fixture, width)
			}
			for _, line := range lines {
				if len(line) > width {
					t.Errorf("Wrap(_, %d) produced a %d-char line, want <= %d: %q", width, len(line), width, line)
				}
			}
		}
	}
}

func TestWrapPreservesWords(t *testing.T) {
	lines := Wrap("the quick brown fox jumps over the lazy dog", 15)
	joined := strings.Join(lines, " ")
	require.Equal(t, "the quick brown fox jumps over the lazy dog", joined, "wrapping must not drop or reorder words")
	for _, line := range lines {
		require.LessOrEqual(t, len(line), 15)
	}
}

func TestWrapEmptyString(t *testing.T) {
	require.Equal(t, []string{""}, Wrap("", 80))
}

func TestCourseTitleWidthFloorsAtMinimum(t *testing.T) {
	// At the default non-TTY width (80) the fixed columns (difficulty, bar,
	// percentage, lesson count, status) already leave less than
	// minCourseTitleWidth — the floor must still win, so an ordinary
	// course title is never truncated.
	require.Equal(t, minCourseTitleWidth, courseTitleWidth(80))
	require.Equal(t, minCourseTitleWidth, courseTitleWidth(100))
}

func TestRenderCoursesTruncatesAPathologicallyLongTitle(t *testing.T) {
	var buf strings.Builder
	theme := NewTheme(false)
	longTitle := strings.Repeat("Enormous Course Title ", 10)
	RenderCourses(&buf, theme, []CourseRow{{
		Title:            longTitle,
		Difficulty:       "BEGINNER",
		EnrollmentStatus: "ACTIVE",
		ProgressPct:      50,
		LessonCount:      6,
	}})
	out := buf.String()
	if strings.Contains(out, longTitle) {
		t.Errorf("RenderCourses did not truncate a %d-char title:\n%s", len(longTitle), out)
	}
	if !strings.Contains(out, "…") {
		t.Errorf("RenderCourses' truncated title is missing the ellipsis marker:\n%s", out)
	}
}

func TestRenderCoursesLeavesShortTitlesUntouched(t *testing.T) {
	var buf strings.Builder
	theme := NewTheme(false)
	RenderCourses(&buf, theme, []CourseRow{{
		Title:            "Build a Redis core in Go",
		Difficulty:       "INTERMEDIATE",
		EnrollmentStatus: "ACTIVE",
		ProgressPct:      25,
		LessonCount:      8,
	}})
	out := buf.String()
	if !strings.Contains(out, "Build a Redis core in Go") {
		t.Errorf("RenderCourses truncated a short title that should fit:\n%s", out)
	}
}

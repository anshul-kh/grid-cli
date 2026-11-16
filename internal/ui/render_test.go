package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/anshul-kh/grid-cli/internal/api"
)

func TestFormatThousands(t *testing.T) {
	cases := map[int]string{
		0:       "0",
		5:       "5",
		94:      "94",
		999:     "999",
		1000:    "1,000",
		1284:    "1,284",
		12345:   "12,345",
		1000000: "1,000,000",
		-1284:   "-1,284",
	}
	for n, want := range cases {
		if got := formatThousands(n); got != want {
			t.Errorf("formatThousands(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestVerdictRowIconReflectsLocalSkipOverServerPassed proves a check the
// CLI never measured (depends_on, setup failure) renders "–" regardless of
// whatever the server's Passed field says — the dash means "not evaluated",
// a claim the CLI itself is entitled to make about its own submission.
func TestVerdictRowIconReflectsLocalSkipOverServerPassed(t *testing.T) {
	skipped := VerdictRow{Passed: true, LocalStatus: api.CheckStatusSkipped}
	if got := skipped.Icon(); got != "–" {
		t.Errorf("Icon() = %q, want – for a locally-skipped check even when Passed=true", got)
	}
	passed := VerdictRow{Passed: true, LocalStatus: api.CheckStatusOK}
	if got := passed.Icon(); got != "✔" {
		t.Errorf("Icon() = %q, want ✔", got)
	}
	failed := VerdictRow{Passed: false, LocalStatus: api.CheckStatusOK}
	if got := failed.Icon(); got != "✖" {
		t.Errorf("Icon() = %q, want ✖", got)
	}
}

// TestRenderMeasurementsNeverUsesVerdictWords proves grid check's rendering
// never prints pass/fail/passed/failed anywhere — only the measurement
// vocabulary (ok/error/skipped) GRID_SPEC §1 allows it.
func TestRenderMeasurementsNeverUsesVerdictWords(t *testing.T) {
	var buf bytes.Buffer
	theme := NewTheme(false)
	RenderMeasurements(&buf, theme, []MeasurementRow{
		{Title: "a", Status: api.CheckStatusOK, DurationMS: 5},
		{Title: "b", Status: api.CheckStatusError, DurationMS: 10},
		{Title: "c", Status: api.CheckStatusSkipped, DurationMS: 0},
	})
	MeasurementFooter(&buf, theme, 1, 3)
	out := strings.ToLower(buf.String())
	for _, forbidden := range []string{"passed", "failed", "pass", "fail"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("grid check output contains verdict word %q:\n%s", forbidden, buf.String())
		}
	}
	// Assert the intent, not an exact sentence: the footer must point at
	// `grid submit` and must say these rows are not a verdict. Pinning the
	// literal wording made this fail when the copy was corrected, which is
	// noise rather than signal.
	rendered := buf.String()
	if !strings.Contains(rendered, "grid submit") {
		t.Errorf("footer does not point the user at `grid submit`:\n%s", rendered)
	}
	if !strings.Contains(strings.ToLower(rendered), "not a verdict") {
		t.Errorf("footer does not state that these are not a verdict:\n%s", rendered)
	}
	// The per-row label must not read as success either — a green "ok" beside
	// "logs/ directory exists" was the original bug.
	if strings.Contains(rendered, " ok ") {
		t.Errorf("measurement rows still use the verdict-like label \"ok\":\n%s", rendered)
	}
}

// TestRenderXPLineFormatting pins the exact TASK.md §7 shape.
func TestRenderXPLineFormatting(t *testing.T) {
	var buf bytes.Buffer
	theme := NewTheme(false)
	RenderXPLine(&buf, theme, api.XP{Delta: 94, Total: 1284}, "OPERATOR")
	got := strings.TrimSpace(buf.String())
	want := "+94 XP  ·  1,284 total  ·  OPERATOR"
	if got != want {
		t.Errorf("RenderXPLine = %q, want %q", got, want)
	}
}

// TestRenderNextLessonNilIsANoOp proves a nil next (no further lesson)
// prints nothing, rather than a blank "Next:" line.
func TestRenderNextLessonNilIsANoOp(t *testing.T) {
	var buf bytes.Buffer
	RenderNextLesson(&buf, NewTheme(false), nil)
	if buf.Len() != 0 {
		t.Errorf("RenderNextLesson(nil) wrote %q, want nothing", buf.String())
	}
}

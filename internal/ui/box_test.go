package ui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuleClampsNegativeWidth(t *testing.T) {
	require.Equal(t, "", Rule(-5))
	require.Equal(t, "────", Rule(4))
}

func TestRenderHeaderFillsToWidth(t *testing.T) {
	var buf strings.Builder
	RenderHeader(&buf, NewTheme(false), "Tasks", 20)
	// "Tasks" (5) + " " (1) + rule(14) = 20 characters before the newline.
	require.Equal(t, "Tasks ──────────────\n", buf.String())
}

func TestRenderHeaderNeverPanicsWhenTitleExceedsWidth(t *testing.T) {
	var buf strings.Builder
	require.NotPanics(t, func() {
		RenderHeader(&buf, NewTheme(false), "A very long title indeed", 10)
	})
	require.Contains(t, buf.String(), "A very long title indeed")
}

func TestRenderEmptyStateWrapsAndBorders(t *testing.T) {
	var buf strings.Builder
	RenderEmptyState(&buf, NewTheme(false), "Not enrolled in any courses yet.", 40)
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("RenderEmptyState produced %d line(s), want a top border, >=1 body line, and a bottom border:\n%s", len(lines), out)
	}
	first, last := lines[0], lines[len(lines)-1]
	if !strings.HasPrefix(first, "┌") || !strings.HasSuffix(first, "┐") {
		t.Errorf("top border = %q, want ┌...┐", first)
	}
	if !strings.HasPrefix(last, "└") || !strings.HasSuffix(last, "┘") {
		t.Errorf("bottom border = %q, want └...┘", last)
	}
	if !strings.Contains(out, "Not enrolled in any courses yet.") {
		t.Errorf("empty-state panel missing the message:\n%s", out)
	}
	// Every line (border and body) renders at the same width, so the box
	// stays a box rather than a ragged edge.
	width := len([]rune(first))
	for _, line := range lines {
		if got := len([]rune(line)); got != width {
			t.Errorf("line %q is %d runes wide, want %d (every line must match the border width)", line, got, width)
		}
	}
}

func TestRenderEmptyStateWrapsLongMessages(t *testing.T) {
	var buf strings.Builder
	longMessage := strings.Repeat("word ", 40)
	RenderEmptyState(&buf, NewTheme(false), longMessage, 40)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) <= 3 {
		t.Fatalf("a long message should wrap onto more than one body line, got %d line(s) total:\n%s", len(lines), buf.String())
	}
	for _, line := range lines {
		if len([]rune(line)) > 40 {
			t.Errorf("line exceeds the requested width 40: %q", line)
		}
	}
}

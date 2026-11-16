package ui

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIndent(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty string is unchanged", "", ""},
		{"single line gets the gutter", "hello", "  hello"},
		{"multiple lines each get the gutter", "a\nb\nc", "  a\n  b\n  c"},
		{"a blank line inside a block stays blank, not padded", "a\n\nb", "  a\n\n  b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Indent(tt.in))
		})
	}
}

func TestFlowBlankCollapsesConsecutiveCalls(t *testing.T) {
	var buf strings.Builder
	f := NewFlow(&buf)
	f.Line("one")
	f.Blank()
	f.Blank()
	f.Blank()
	f.Line("two")
	require.Equal(t, "one\n\ntwo\n", buf.String(), "three Blank() calls in a row must collapse to one blank line")
}

func TestFlowSuppressesLeadingBlank(t *testing.T) {
	var buf strings.Builder
	f := NewFlow(&buf)
	f.Blank() // nothing has been written yet — must not produce a leading blank
	f.Line("first")
	require.Equal(t, "first\n", buf.String())
}

func TestFlowNeverEndsOnABlankLine(t *testing.T) {
	var buf strings.Builder
	f := NewFlow(&buf)
	f.Line("one")
	f.Blank() // requested, but nothing ever follows — must never be flushed
	require.Equal(t, "one\n", buf.String())
}

func TestFlowSectionOmitsBlankOnlyAtTop(t *testing.T) {
	var buf strings.Builder
	f := NewFlow(&buf)
	theme := NewTheme(false)
	f.Section(theme, "Tasks", 20, func(w io.Writer) {
		fmt.Fprintln(w, "1. do the thing")
	})
	f.Section(theme, "Next", 20, func(w io.Writer) {
		fmt.Fprintln(w, "2. do the next thing")
	})
	out := buf.String()
	if strings.HasPrefix(out, "\n") {
		t.Errorf("a Section at the top of output must not start with a blank line:\n%q", out)
	}
	if !strings.Contains(out, "\n\nNext ") {
		t.Errorf("a Section after the first one must be preceded by exactly one blank line:\n%q", out)
	}
	if strings.Contains(out, "\n\n\n") {
		t.Errorf("no two blank lines may land back to back:\n%q", out)
	}
}

func TestFlowLineAcceptsAPreRenderedMultiLineBlock(t *testing.T) {
	var buf strings.Builder
	f := NewFlow(&buf)
	f.Line("title")
	f.Blank()
	f.Line("row one\nrow two")
	require.Equal(t, "title\n\nrow one\nrow two\n", buf.String())
}

func TestSectionAtTheTopOmitsTheLeadingBlank(t *testing.T) {
	var buf strings.Builder
	Section(&buf, NewTheme(false), "Tasks", 20, func(w io.Writer) {
		fmt.Fprintln(w, "one")
		fmt.Fprintln(w, "two")
	})
	require.Equal(t, "Tasks ──────────────\n  one\n  two\n", buf.String())
}

func TestKeyValueAlignsValuesOnTheLongestKey(t *testing.T) {
	var buf strings.Builder
	KeyValue(&buf, NewTheme(false), [][2]string{
		{"commit", "abc123"},
		{"os/arch", "darwin/amd64"},
	})
	require.Equal(t, "commit  abc123\nos/arch darwin/amd64\n", buf.String())
}

func TestKeyValueEmptyIsANoOp(t *testing.T) {
	var buf strings.Builder
	KeyValue(&buf, NewTheme(false), nil)
	require.Equal(t, "", buf.String())
}

func TestKeyValueCapsColumnWidthAtRenderWidthOverThree(t *testing.T) {
	var buf strings.Builder
	longKey := strings.Repeat("x", 500)
	KeyValue(&buf, NewTheme(false), [][2]string{{longKey, "value"}})
	line := strings.TrimSuffix(buf.String(), "\n")
	gotKey, _, found := strings.Cut(line, " value")
	if !found {
		t.Fatalf("KeyValue output %q does not end with the expected value", line)
	}
	if max := RenderWidth() / 3; len([]rune(gotKey)) > max {
		t.Errorf("key column is %d runes wide, want <= RenderWidth()/3 (%d)", len([]rune(gotKey)), max)
	}
}

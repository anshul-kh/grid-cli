package ui

import "testing"

// The bug these guard against: `grid check` printed a green "ok" next to a
// check's title and nothing else, so a learner whose `logs/` directory did
// not exist saw "ok  logs/ directory exists" and concluded it did. The
// measurement had succeeded; the display threw away *what* it measured.
func TestSummariseEvidenceShowsTheDecidingFact(t *testing.T) {
	cases := []struct {
		name     string
		evidence map[string]any
		want     string
	}{
		{"absent directory is visible", map[string]any{"exists": false}, "exists=false"},
		{"present directory is visible", map[string]any{"exists": true}, "exists=true"},
		{"untracked file is visible", map[string]any{"tracked": false}, "tracked=false"},
		{"missing tool is visible", map[string]any{"found": false}, "found=false"},
		{"env var reports set without the value", map[string]any{"set": true, "matches": true}, "set=true"},
		{"exit code is labelled readably", map[string]any{"exitCode": float64(1)}, "exit=1"},
		{"http status", map[string]any{"status": float64(404)}, "status=404"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SummariseEvidence(tc.evidence); got != tc.want {
				t.Errorf("SummariseEvidence(%v) = %q, want %q", tc.evidence, got, tc.want)
			}
		})
	}
}

func TestSummariseEvidenceAppendsVersionToFound(t *testing.T) {
	got := SummariseEvidence(map[string]any{"found": true, "versionOutput": "git version 2.55.0"})
	if got != "found=true git version 2.55.0" {
		t.Errorf("got %q, want the version alongside found=true", got)
	}
}

// A command that printed hundreds of lines must not wreck the table.
func TestSummariseEvidenceCollapsesMultilineAndLongValues(t *testing.T) {
	got := SummariseEvidence(map[string]any{"found": true, "versionOutput": "first line\nsecond line\nthird"})
	if want := "found=true first line"; got != want {
		t.Errorf("got %q, want %q (only the first line)", got, want)
	}

	long := SummariseEvidence(map[string]any{"status": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	if len(long) > 40 {
		t.Errorf("summary not capped: %q (len %d)", long, len(long))
	}
}

// An exit code of 0 is a real observation and must not be mistaken for "no
// evidence" — a bare `if v != 0` style check would drop it.
func TestSummariseEvidenceKeepsZeroValues(t *testing.T) {
	if got := SummariseEvidence(map[string]any{"exitCode": float64(0)}); got != "exit=0" {
		t.Errorf("got %q, want exit=0", got)
	}
	if got := SummariseEvidence(map[string]any{"exists": false}); got != "exists=false" {
		t.Errorf("got %q, want exists=false", got)
	}
}

func TestSummariseEvidenceCountsComposites(t *testing.T) {
	if got := SummariseEvidence(map[string]any{"entries": []any{"a", "b", "c"}}); got != "entries=3" {
		t.Errorf("got %q, want entries=3", got)
	}
}

// No evidence, or evidence with nothing decisive, yields "" so the caller can
// omit the column rather than print an empty arrow.
func TestSummariseEvidenceEmptyWhenNothingUseful(t *testing.T) {
	for _, e := range []map[string]any{nil, {}, {"truncated": true}} {
		if got := SummariseEvidence(e); got != "" {
			t.Errorf("SummariseEvidence(%v) = %q, want empty", e, got)
		}
	}
}

// The CLI must never present a measurement as a verdict.
func TestMeasurementLabelIsNeverAVerdictWord(t *testing.T) {
	theme := &Theme{}
	label := measurementLabel(theme, "ok")
	for _, banned := range []string{"ok", "pass", "passed", "fail", "✔", "✖"} {
		if label == banned {
			t.Errorf("measurementLabel rendered %q, which reads as a verdict", label)
		}
	}
	if label != "measured" {
		t.Errorf("measurementLabel = %q, want \"measured\"", label)
	}
}

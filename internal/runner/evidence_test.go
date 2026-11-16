package runner

import (
	"fmt"
	"strings"
	"testing"
)

// TestEvidenceFieldCap proves a field larger than the 64 KB per-field cap is
// head-truncated, flagged "truncated": true, and the tail is absent.
func TestEvidenceFieldCap(t *testing.T) {
	big := strings.Repeat("h", MaxFieldBytes+50_000)
	tail := "THE_TAIL_MUST_NOT_SURVIVE"
	big = big[:len(big)-len(tail)] + tail // tail sits at the very end

	ev := BuildEvidence(map[string]any{"stdout": big})

	got, ok := ev["stdout"].(string)
	if !ok {
		t.Fatalf("stdout field = %#v, want string", ev["stdout"])
	}
	if len(got) > MaxFieldBytes {
		t.Errorf("field kept %d bytes, want ≤ %d", len(got), MaxFieldBytes)
	}
	if len(got) != MaxFieldBytes {
		t.Errorf("head-biased cap kept %d bytes, want exactly the first %d", len(got), MaxFieldBytes)
	}
	if !strings.HasPrefix(got, "hhhhh") {
		t.Errorf("cap did not keep the head: %q…", got[:20])
	}
	if strings.Contains(got, tail) {
		t.Error("cap is not head-biased: tail content survived truncation")
	}
	if truncated, ok := ev["truncated"].(bool); !ok || !truncated {
		t.Errorf("ev[\"truncated\"] = %#v, want true", ev["truncated"])
	}
}

// TestEvidenceTotalCap proves a payload over the 1 MB total cap is held
// under it by halving the per-field budget (still head-biased).
func TestEvidenceTotalCap(t *testing.T) {
	fields := map[string]any{}
	// 20 fields × 60 KB = 1.2 MB > 1 MB total. All under the per-field cap,
	// so only the total-cap loop can bring the payload down.
	for i := 0; i < 20; i++ {
		fields[fmt.Sprintf("f%02d", i)] = strings.Repeat("x", 60_000)
	}
	ev := BuildEvidence(fields)
	if size := Size(ev); size > MaxTotalBytes {
		t.Errorf("payload is %d bytes, want ≤ %d", size, MaxTotalBytes)
	}
	for k, v := range ev {
		if k == "truncated" {
			continue
		}
		if n := len(v.(string)); n > MaxFieldBytes/2 {
			t.Errorf("field %s kept %d bytes after total-cap pass, want ≤ %d", k, n, MaxFieldBytes/2)
		}
	}
	if truncated, ok := ev["truncated"].(bool); !ok || !truncated {
		t.Errorf("ev[\"truncated\"] = %#v, want true", ev["truncated"])
	}
}

// TestEvidenceByteSliceField proves []byte fields (http bodies, excerpts) are
// capped the same way.
func TestEvidenceByteSliceField(t *testing.T) {
	big := []byte(strings.Repeat("b", MaxFieldBytes+1000))
	ev := BuildEvidence(map[string]any{"body": big})
	got, ok := ev["body"].([]byte)
	if !ok {
		t.Fatalf("body field = %#v, want []byte", ev["body"])
	}
	if len(got) != MaxFieldBytes {
		t.Errorf("body kept %d bytes, want %d", len(got), MaxFieldBytes)
	}
	if ev["truncated"] != true {
		t.Errorf("truncated = %#v, want true", ev["truncated"])
	}
}

// TestEvidenceUTF8SafeTruncation proves head-truncation never splits a
// multi-byte rune.
func TestEvidenceUTF8SafeTruncation(t *testing.T) {
	// 40 000 × "é" (2 bytes each) = 80 KB > 64 KB cap; the cut lands mid-"é".
	big := strings.Repeat("é", 40_000)
	ev := BuildEvidence(map[string]any{"stdout": big})
	got := ev["stdout"].(string)
	if !strings.HasSuffix(got, "é") {
		t.Errorf("truncated field ends mid-rune: %q", got[len(got)-4:])
	}
}

// TestEvidenceSmallFieldsUntouched proves an under-cap payload is passed
// through unchanged, with no spurious truncated flag.
func TestEvidenceSmallFieldsUntouched(t *testing.T) {
	fields := map[string]any{
		"exitCode":   0,
		"stdout":     "hello\n",
		"stderr":     "",
		"durationMs": 42,
	}
	ev := BuildEvidence(fields)
	for k, v := range fields {
		if ev[k] != v {
			t.Errorf("field %s = %#v, want %#v", k, ev[k], v)
		}
	}
	if _, has := ev["truncated"]; has {
		t.Error("unexpected truncated flag on an under-cap payload")
	}
}

// TestRetruncateHalves proves the 413 path helper: Retruncate(ev, 0.5)
// re-truncates text fields at half the per-field cap, head-biased, flagging
// truncated.
func TestRetruncateHalves(t *testing.T) {
	full := strings.Repeat("r", MaxFieldBytes) + "SUFFIX_THAT_MUST_GO"
	ev := Evidence{"stdout": full, "exitCode": 0}
	half := Retruncate(ev, 0.5)

	got := half["stdout"].(string)
	if len(got) != MaxFieldBytes/2 {
		t.Errorf("re-truncated field = %d bytes, want exactly %d", len(got), MaxFieldBytes/2)
	}
	if strings.Contains(got, "SUFFIX_THAT_MUST_GO") {
		t.Error("re-truncation is not head-biased: tail survived")
	}
	if got, ok := half["exitCode"].(int); !ok || got != 0 {
		t.Errorf("exitCode = %#v, want 0 (non-text fields must survive re-truncation)", half["exitCode"])
	}
	if half["truncated"] != true {
		t.Errorf("truncated = %#v, want true", half["truncated"])
	}
	// The original evidence is untouched (callers may need it if the retry
	// is not needed).
	if len(ev["stdout"].(string)) != len(full) {
		t.Error("Retruncate mutated the input evidence")
	}
}

// TestRetruncateTotalCap proves re-truncation also holds the total at the
// same fraction of the total cap.
func TestRetruncateTotalCap(t *testing.T) {
	fields := map[string]any{}
	for i := 0; i < 40; i++ {
		fields[fmt.Sprintf("f%02d", i)] = strings.Repeat("y", 30_000)
	}
	ev := BuildEvidence(fields) // 40×30 KB = 1.2 MB → total-cap pass shrinks it
	half := Retruncate(ev, 0.5)
	if size := Size(half); size > MaxTotalBytes/2 {
		t.Errorf("re-truncated payload is %d bytes, want ≤ %d", size, MaxTotalBytes/2)
	}
}

// TestFileExcerptAroundFirstMatch pins the §4 file.contains excerpt: ±5
// lines around the FIRST match when one exists, the whole content when
// everything fits, and a 32 KB head cap otherwise.
func TestFileExcerptAroundFirstMatch(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, "line %02d\n", i)
	}
	content := []byte(strings.TrimSuffix(b.String(), "\n"))

	// Match on line 10 (index 10): window is lines 5..15 inclusive.
	firstMatch := strings.Index(string(content), "line 10")
	excerpt, truncated := FileExcerpt(content, firstMatch)
	if !truncated {
		t.Error("expected truncated=true: the window excludes lines outside 5..15")
	}
	lines := strings.Split(string(excerpt), "\n")
	if len(lines) != 11 {
		t.Fatalf("excerpt has %d lines, want 11 (±5 around the match)", len(lines))
	}
	if lines[0] != "line 05" || lines[10] != "line 15" {
		t.Errorf("window = [%s..%s], want [line 05..line 15]", lines[0], lines[10])
	}

	// No match: head cap.
	head, truncated := FileExcerpt(content, -1)
	if truncated {
		t.Error("small content with no match should not be truncated")
	}
	if string(head) != string(content) {
		t.Error("no-match excerpt of small content should be the whole content")
	}
}

func TestFileExcerptHeadCap(t *testing.T) {
	big := []byte(strings.Repeat("z", ExcerptLimit+5000))
	excerpt, truncated := FileExcerpt(big, -1)
	if len(excerpt) != ExcerptLimit {
		t.Errorf("no-match excerpt = %d bytes, want exactly the %d head cap", len(excerpt), ExcerptLimit)
	}
	if !truncated {
		t.Error("expected truncated=true for content over the head cap")
	}
}

func TestFileExcerptNearEdges(t *testing.T) {
	content := []byte("alpha\nbeta\ngamma\ndelta")
	// Match on the first line: window clamps to the start.
	excerpt, _ := FileExcerpt(content, strings.Index(string(content), "alpha"))
	if string(excerpt) != string(content) {
		t.Errorf("first-line match excerpt = %q, want the whole short content", excerpt)
	}
	// Match on the last line: window clamps to the end.
	excerpt, _ = FileExcerpt(content, strings.Index(string(content), "delta"))
	if string(excerpt) != string(content) {
		t.Errorf("last-line match excerpt = %q, want the whole short content", excerpt)
	}
}

// TestEvidenceConstants pins the §5.4 caps.
func TestEvidenceConstants(t *testing.T) {
	if MaxFieldBytes != 64*1024 {
		t.Errorf("MaxFieldBytes = %d, want 64 KB", MaxFieldBytes)
	}
	if MaxTotalBytes != 1024*1024 {
		t.Errorf("MaxTotalBytes = %d, want 1 MB", MaxTotalBytes)
	}
	if ExcerptLimit != 32*1024 {
		t.Errorf("ExcerptLimit = %d, want 32 KB", ExcerptLimit)
	}
}

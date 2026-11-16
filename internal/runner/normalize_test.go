package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNormalizeOutputFixtures walks the shared fixture suite
// (testdata/normalize/*.in.txt with a matching *.expected.txt) and asserts
// NormalizeOutput reproduces every expected output byte-for-byte. The first
// ten pairs are a verbatim copy of grid-backend's
// tests/fixtures/normalize/cases.json — the cross-repo byte-identical
// normalisation contract (GRID_SPEC §4 rule 3 + §5.3 addendum: bare \r
// folds to \n too).
func TestNormalizeOutputFixtures(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "normalize")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read fixture dir: %v", err)
	}
	n := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".in.txt") {
			continue
		}
		n++
		stem := strings.TrimSuffix(e.Name(), ".in.txt")
		t.Run(stem, func(t *testing.T) {
			in, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatalf("read input: %v", err)
			}
			want, err := os.ReadFile(filepath.Join(dir, stem+".expected.txt"))
			if err != nil {
				t.Fatalf("read expected: %v", err)
			}
			got := NormalizeOutput(in)
			if string(got) != string(want) {
				t.Errorf("NormalizeOutput(%q) = %q, want %q", in, got, want)
			}
		})
	}
	if n < 8 {
		t.Errorf("fixture suite has %d pairs, want at least 8", n)
	}
}

// TestNormalizeOutputBareCR pins the §5.3 addendum directly: a bare \r (no
// following \n) folds to \n exactly like CRLF does, so
// carriage-return-driven output (progress bars) normalises identically.
func TestNormalizeOutputBareCR(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a\r\nb\nc\rd", "a\nb\nc\nd"}, // mixed CRLF, LF, bare CR
		{"a\rb\rc", "a\nb\nc"},         // bare CR only
		{"downloading 50%\rdone 100%\n", "downloading 50%\ndone 100%"},
	}
	for _, tc := range cases {
		if got := string(NormalizeOutput([]byte(tc.in))); got != tc.want {
			t.Errorf("NormalizeOutput(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

package checks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-cli/internal/spec"
)

// TestFileExistsReportsObservedFact proves file.exists's three cases:
// missing path -> ok, exists:false (a "wrong answer" is not an error);
// present file -> ok, exists:true with size+sha256; unreadable path ->
// error (the measurement itself failed, not the content it would have
// found). Matches plan pass 5's explicit criterion in one test.
func TestFileExistsReportsObservedFact(t *testing.T) {
	dir := t.TempDir()
	env := runner.NewEnv(dir, nil)
	checker := fileExistsChecker{}

	t.Run("missing path is ok, not an error", func(t *testing.T) {
		ev, err := checker.Measure(context.Background(), spec.Check{Path: "nope.txt"}, env)
		if err != nil {
			t.Fatalf("Measure on missing file: unexpected error %v", err)
		}
		if ev["exists"] != false {
			t.Errorf("evidence[exists] = %v, want false", ev["exists"])
		}
		if _, ok := ev["size"]; ok {
			t.Errorf("evidence has size for a missing file: %v", ev)
		}
	})

	content := []byte("hello, grid\n")
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), content, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	sum := sha256.Sum256(content)
	wantSHA := hex.EncodeToString(sum[:])

	t.Run("present file reports exists, size, sha256", func(t *testing.T) {
		ev, err := checker.Measure(context.Background(), spec.Check{Path: "hello.txt"}, env)
		if err != nil {
			t.Fatalf("Measure: unexpected error: %v", err)
		}
		if ev["exists"] != true {
			t.Errorf("evidence[exists] = %v, want true", ev["exists"])
		}
		if ev["size"] != int64(len(content)) {
			t.Errorf("evidence[size] = %v, want %d", ev["size"], len(content))
		}
		if ev["sha256"] != wantSHA {
			t.Errorf("evidence[sha256] = %v, want %s", ev["sha256"], wantSHA)
		}
	})

	if runtime.GOOS == "windows" {
		t.Skip("permission-denied semantics are unix-assertive (plan Assumption 11)")
	}
	t.Run("unreadable path is an error, not exists:false", func(t *testing.T) {
		locked := filepath.Join(dir, "locked.txt")
		if err := os.WriteFile(locked, []byte("secret"), 0o000); err != nil {
			t.Fatalf("write locked fixture: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o644) })
		if os.Geteuid() == 0 {
			t.Skip("running as root ignores file permissions")
		}
		_, err := checker.Measure(context.Background(), spec.Check{Path: "locked.txt"}, env)
		if err == nil {
			t.Fatal("Measure on an unreadable file: want error, got nil")
		}
	})
}

// TestFileContainsExcerptMatchWindow proves the ±5-lines-around-first-match
// excerpt: a 7-line file with the match on line 4 keeps every line (lo
// clamps to 0, hi clamps to the file's end) — so this also exercises the
// clamping at both edges, not just the common case.
func TestFileContainsExcerptMatchWindow(t *testing.T) {
	fixture := filepath.Join("..", "..", "..", "testdata", "checks", "sample.txt")
	content, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sample.txt"), content, 0o644); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	env := runner.NewEnv(dir, nil)

	ev, err := (fileContainsChecker{}).Measure(context.Background(), spec.Check{
		Path: "sample.txt",
		Text: "the needle is here",
	}, env)
	if err != nil {
		t.Fatalf("Measure: unexpected error: %v", err)
	}
	excerpt, _ := ev["excerpt"].(string)
	if !strings.Contains(excerpt, "the needle is here") {
		t.Fatalf("excerpt %q does not contain the matched line", excerpt)
	}
	if !strings.Contains(excerpt, "line one") || !strings.Contains(excerpt, "line seven") {
		t.Errorf("excerpt %q should keep the whole 7-line file (±5 lines around a line-4 match covers it entirely)", excerpt)
	}
	if ev["truncated"] == true {
		t.Errorf("a 7-line file should never be marked truncated: %v", ev)
	}
}

// TestFileContainsHeadCap proves the 32 KB head cap applies when there is
// no pattern/text to locate a match around: a file far larger than 32 KB
// with no Pattern/Text set yields an excerpt that is exactly the head,
// capped, with truncated:true.
func TestFileContainsHeadCap(t *testing.T) {
	dir := t.TempDir()
	line := strings.Repeat("a", 100) + "\n"
	var b strings.Builder
	for b.Len() < 64<<10 { // well past the 32 KB excerpt cap
		b.WriteString(line)
	}
	full := b.String()
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(full), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	env := runner.NewEnv(dir, nil)

	ev, err := (fileContainsChecker{}).Measure(context.Background(), spec.Check{Path: "big.txt"}, env)
	if err != nil {
		t.Fatalf("Measure: unexpected error: %v", err)
	}
	excerpt, _ := ev["excerpt"].(string)
	if len(excerpt) > runner.ExcerptLimit {
		t.Errorf("excerpt is %d bytes, want <= %d (the 32 KB head cap)", len(excerpt), runner.ExcerptLimit)
	}
	if len(excerpt) == len(full) {
		t.Errorf("excerpt was not capped at all (len %d == full file length)", len(excerpt))
	}
	if ev["truncated"] != true {
		t.Errorf("evidence[truncated] = %v, want true for a file far past the excerpt cap", ev["truncated"])
	}
	// size/sha256 describe the whole file, not the capped excerpt.
	if ev["size"] != int64(len(full)) {
		t.Errorf("evidence[size] = %v, want the full file's length %d (size is not capped)", ev["size"], len(full))
	}
}

// TestFileHashNeverComparesLocally proves file.hash reports only { sha256 }
// — never a pass/fail against a `sha256` field the spec might also carry
// (the public spec omits it; a judge-complete Check struct might still set
// it, and the checker must still ignore it).
func TestFileHashNeverComparesLocally(t *testing.T) {
	dir := t.TempDir()
	content := []byte("some file content\n")
	if err := os.WriteFile(filepath.Join(dir, "f.bin"), content, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	sum := sha256.Sum256(content)
	want := hex.EncodeToString(sum[:])
	env := runner.NewEnv(dir, nil)

	ev, err := (fileHashChecker{}).Measure(context.Background(), spec.Check{
		Path:   "f.bin",
		SHA256: "0000000000000000000000000000000000000000000000000000000000000000", // deliberately wrong
	}, env)
	if err != nil {
		t.Fatalf("Measure: unexpected error: %v", err)
	}
	if ev["sha256"] != want {
		t.Errorf("evidence[sha256] = %v, want the file's real hash %s regardless of check.SHA256", ev["sha256"], want)
	}
	if _, ok := ev["passed"]; ok {
		t.Errorf("file.hash evidence must never carry a verdict field: %v", ev)
	}

	if _, err := (fileHashChecker{}).Measure(context.Background(), spec.Check{Path: "missing.bin"}, env); err == nil {
		t.Fatal("file.hash on a missing file: want error (it cannot be measured), got nil")
	}
}

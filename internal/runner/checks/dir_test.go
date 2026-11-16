package checks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-cli/internal/spec"
)

// TestDirExistsReportsObservedFact proves dir.exists's { exists } shape:
// missing path -> false (not an error); a directory -> true; a plain file
// at that path -> false ("a directory does not exist there" is itself a
// valid observation, per fileExistsChecker's symmetric handling of a
// directory where a file was expected).
func TestDirExistsReportsObservedFact(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	env := runner.NewEnv(dir, nil)
	checker := dirExistsChecker{}

	cases := []struct {
		path string
		want bool
	}{
		{"missing", false},
		{"sub", true},
		{"file.txt", false},
	}
	for _, c := range cases {
		ev, err := checker.Measure(context.Background(), spec.Check{Path: c.path}, env)
		if err != nil {
			t.Fatalf("Measure(%q): unexpected error: %v", c.path, err)
		}
		if ev["exists"] != c.want {
			t.Errorf("Measure(%q)[exists] = %v, want %v", c.path, ev["exists"], c.want)
		}
	}
}

// TestDirTreeRelativeSortedAndCapped proves dir.tree's three contracts in
// one fixture: entries are relative (not absolute) and forward-slashed
// (not OS-separated — exercised by nesting), sorted ascending, and capped
// at 500 even when the real tree has more.
func TestDirTreeRelativeSortedAndCapped(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const total = 520
	for i := 0; i < total; i++ {
		name := fmt.Sprintf("nested/file-%04d.txt", i)
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	env := runner.NewEnv(root, nil)

	ev, err := (dirTreeChecker{}).Measure(context.Background(), spec.Check{Path: "."}, env)
	if err != nil {
		t.Fatalf("Measure: unexpected error: %v", err)
	}
	entries, ok := ev["entries"].([]string)
	if !ok {
		t.Fatalf("evidence[entries] is %T, want []string", ev["entries"])
	}
	if len(entries) != dirTreeEntryCap {
		t.Fatalf("len(entries) = %d, want exactly the %d cap (tree has %d files + 1 dir)", len(entries), dirTreeEntryCap, total)
	}
	if ev["truncated"] != true {
		t.Errorf("evidence[truncated] = %v, want true past the 500 cap", ev["truncated"])
	}
	if !sort.StringsAreSorted(entries) {
		t.Error("entries are not sorted ascending")
	}
	for _, e := range entries {
		if filepath.IsAbs(e) {
			t.Fatalf("entry %q is absolute, want relative to the checked path", e)
		}
		if filepath.ToSlash(e) != e {
			t.Errorf("entry %q is not forward-slashed", e)
		}
	}
	// "nested" (the directory itself) sorts before any "nested/file-..."
	// entry, so it survives the cap and proves directories are listed too.
	if entries[0] != "nested" {
		t.Errorf("entries[0] = %q, want \"nested\" (the directory itself, sorting first)", entries[0])
	}
}

// TestDirTreeMaxDepth proves max_depth bounds the walk: depth 1 lists only
// the root's immediate children, never anything inside "sub".
func TestDirTreeMaxDepth(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub", "deeper"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "deeper", "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "top.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	env := runner.NewEnv(root, nil)

	ev, err := (dirTreeChecker{}).Measure(context.Background(), spec.Check{Path: ".", MaxDepth: 1}, env)
	if err != nil {
		t.Fatalf("Measure: unexpected error: %v", err)
	}
	entries := ev["entries"].([]string)
	want := []string{"sub", "top.txt"}
	if len(entries) != len(want) {
		t.Fatalf("entries = %v, want %v (max_depth: 1 excludes anything under sub/)", entries, want)
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Errorf("entries[%d] = %q, want %q", i, entries[i], want[i])
		}
	}
}

// TestDirTreeMissingPathIsError proves dir.tree has no "exists" evidence
// field to fall back to — a missing directory cannot be measured at all.
func TestDirTreeMissingPathIsError(t *testing.T) {
	env := runner.NewEnv(t.TempDir(), nil)
	if _, err := (dirTreeChecker{}).Measure(context.Background(), spec.Check{Path: "nope"}, env); err == nil {
		t.Fatal("Measure on a missing directory: want error, got nil")
	}
}

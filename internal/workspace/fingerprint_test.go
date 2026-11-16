package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestFingerprintSortedCappedAndIgnores proves Fingerprint's §5.3 contract:
// sorted forward-slash paths, a 500-entry cap on Files that does not shrink
// FileCount, and that .grid/ and .git/ are never walked.
func TestFingerprintSortedCappedAndIgnores(t *testing.T) {
	dir := t.TempDir()

	const total = 520
	for i := 0; i < total; i++ {
		name := fmt.Sprintf("file-%04d.txt", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	// Ignored metadata directories, with content that must never surface.
	mustIgnore := []string{
		filepath.Join(dir, gridDir, "attempt.json"),
		filepath.Join(dir, ".git", "HEAD"),
		filepath.Join(dir, ".git", "objects", "pack", "pack.idx"),
	}
	for _, p := range mustIgnore {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte("ignored"), 0o644); err != nil {
			t.Fatalf("seed ignored file %s: %v", p, err)
		}
	}

	ws, err := Fingerprint(dir)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if ws.FileCount != total {
		t.Errorf("FileCount = %d, want %d (uncapped total, .grid/.git excluded)", ws.FileCount, total)
	}
	if len(ws.Files) != FingerprintCap {
		t.Fatalf("len(Files) = %d, want cap %d", len(ws.Files), FingerprintCap)
	}

	paths := make([]string, len(ws.Files))
	for i, f := range ws.Files {
		paths[i] = f.Path
		if filepath.IsAbs(f.Path) {
			t.Errorf("Files[%d].Path is absolute: %q", i, f.Path)
		}
		if f.Path != filepath.ToSlash(f.Path) {
			t.Errorf("Files[%d].Path is not forward-slashed: %q", i, f.Path)
		}
		if f.SHA256 == "" || len(f.SHA256) != 64 {
			t.Errorf("Files[%d].SHA256 looks wrong: %q", i, f.SHA256)
		}
		if f.Size != 1 {
			t.Errorf("Files[%d].Size = %d, want 1", i, f.Size)
		}
		if pathHasComponent(f.Path, gridDir) || pathHasComponent(f.Path, ".git") {
			t.Errorf("Fingerprint did not ignore %q", f.Path)
		}
	}
	if !sort.StringsAreSorted(paths) {
		t.Error("Files is not sorted by path")
	}
}

// TestFingerprintSkipsSymlinks proves the defense-in-depth fix: a symlink
// sitting in a workspace — by any means, not just a starter-repo clone —
// must be skipped, never hashed. Hashing through hashFile's os.Open would
// fingerprint whatever the symlink points at (here, a file outside the
// workspace entirely) as if it were the workspace's own content.
func TestFingerprintSkipsSymlinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("seed real.txt: %v", err)
	}

	outside := t.TempDir()
	secretPath := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("TOP-SECRET-CANARY\n"), 0o644); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	linkPath := filepath.Join(dir, "link-to-secret")
	if err := os.Symlink(secretPath, linkPath); err != nil {
		t.Skipf("cannot create symlinks on this host: %v", err)
	}

	ws, err := Fingerprint(dir)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if ws.FileCount != 1 {
		t.Errorf("FileCount = %d, want 1 (the symlink must be skipped, not counted)", ws.FileCount)
	}
	for _, f := range ws.Files {
		if f.Path == "link-to-secret" {
			t.Errorf("Fingerprint hashed a symlink instead of skipping it: %+v", f)
		}
	}
}

// pathHasComponent reports whether a forward-slash path contains dirName as
// one of its slash-separated components.
func pathHasComponent(path, dirName string) bool {
	for _, part := range strings.Split(path, "/") {
		if part == dirName {
			return true
		}
	}
	return false
}

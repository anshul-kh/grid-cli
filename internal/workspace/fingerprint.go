package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// FingerprintCap is the §5.3 cap on the workspace proof's file walk: at
// most 500 entries ride on the wire, however many files the workspace
// actually has.
const FingerprintCap = 500

// ignoredDirs are never walked into: `.grid` is the CLI's own metadata, and
// `.git` is version-control plumbing, neither of which is lesson content a
// check could plausibly measure.
var ignoredDirs = map[string]bool{
	gridDir: true,
	".git":  true,
}

// FileHash is one fingerprinted workspace file: a forward-slash relative
// path, its bare lowercase hex sha256, and its size.
type FileHash struct {
	Path   string
	SHA256 string
	Size   int64
}

// Tree is the result of the Fingerprint walk: the true file count plus the
// ≤500 hashed entries, sorted by path — the input the submission envelope's
// workspaceProof.saltedTreeHash is computed from (GRID_SPEC §5.3/A4).
type Tree struct {
	FileCount int
	Files     []FileHash
}

// Fingerprint walks the workspace rooted at dir and hashes every regular
// file (GRID_SPEC §5.3), honouring the ignore rules and the 500-entry cap.
// A symlink is skipped entirely rather than hashed — GRID_SPEC §4
// cross-platform rule 5 already says "no check may depend on ...
// symlinks", and hashing through one via hashFile's os.Open would
// fingerprint whatever it points at, not the workspace's own content. This
// is defense in depth against a workspace that ends up with a symlink by
// any means (CloneStarter already refuses to bring one in via a starter
// repo clone in the first place). The returned FileCount is the true total
// of regular files counted; Files is sorted by path (forward slashes) and
// capped — the cap trims the list, never the count.
func Fingerprint(dir string) (Tree, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && ignoredDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return Tree{}, fmt.Errorf("fingerprinting workspace %s: %w", dir, err)
	}
	sort.Strings(paths)

	out := Tree{FileCount: len(paths)}
	capped := paths
	if len(capped) > FingerprintCap {
		capped = capped[:FingerprintCap]
	}
	out.Files = make([]FileHash, 0, len(capped))
	for _, rel := range capped {
		sum, size, err := hashFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return Tree{}, fmt.Errorf("hashing %s: %w", rel, err)
		}
		out.Files = append(out.Files, FileHash{Path: rel, SHA256: sum, Size: size})
	}
	return out, nil
}

// noGitHeadSha is the 40-zero stand-in GRID_SPEC §5.3 pins for a workspace
// with no commits (or no repo at all) — the proof field is always 40 hex.
const noGitHeadSha = "0000000000000000000000000000000000000000"

// GitHeadSHA returns the workspace's `git rev-parse HEAD`, or 40 zeros when
// the workspace has no repo or no commits (GRID_SPEC §5.3).
func GitHeadSHA(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return noGitHeadSha
	}
	sha := strings.TrimSpace(string(out))
	if len(sha) != len(noGitHeadSha) {
		return noGitHeadSha
	}
	return sha
}

// hashFile returns a file's hex sha256 digest and size, streaming so large
// workspace files never load fully into memory.
func hashFile(path string) (sha string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

package runner

import (
	"runtime"
	"strings"
	"testing"
)

// TestCappedBufferBoundsMemory proves the capture buffer underlying Run's
// stdout/stderr never grows past its cap, however much a command writes —
// the portable half of "a command emitting 10 MB of stdout does not grow
// memory unboundedly" (the real-subprocess half lives in exec_unix_test.go;
// `yes` is not available on Windows, this unit test runs on every OS).
func TestCappedBufferBoundsMemory(t *testing.T) {
	b := newCappedBuffer(1024)
	chunk := make([]byte, 64<<10) // 64 KB per write
	for i := range chunk {
		chunk[i] = 'x'
	}
	const writes = 160 // 160 * 64 KB = 10 MB total written
	for i := 0; i < writes; i++ {
		if _, err := b.Write(chunk); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if len(b.Bytes()) != 1024 {
		t.Errorf("buffer holds %d bytes after 10 MB written, want exactly the 1024-byte cap", len(b.Bytes()))
	}
	if !b.Truncated() {
		t.Error("Truncated() = false, want true after writing past the cap")
	}
}

// TestCappedBufferDefaultsWhenCapNotPositive proves a non-positive cap falls
// back to DefaultCaptureCap rather than capping at zero (which would make
// every command's output look truncated).
func TestCappedBufferDefaultsWhenCapNotPositive(t *testing.T) {
	b := newCappedBuffer(0)
	if b.cap != DefaultCaptureCap {
		t.Errorf("newCappedBuffer(0).cap = %d, want DefaultCaptureCap (%d)", b.cap, DefaultCaptureCap)
	}
}

// TestShellArgsShape pins the §4 rule 1 escape hatch's exact argv shape:
// `cmd /c` on Windows, `sh -c` elsewhere — built as this platform would
// build it (shellArgs is not build-tagged; it switches on runtime.GOOS).
func TestShellArgsShape(t *testing.T) {
	got := shellArgs("echo hi")
	if runtime.GOOS == "windows" {
		if len(got) != 3 || got[0] != "cmd" || got[1] != "/c" || got[2] != "echo hi" {
			t.Errorf("shellArgs on windows = %v, want [cmd /c \"echo hi\"]", got)
		}
		return
	}
	if len(got) != 3 || got[0] != "sh" || got[1] != "-c" || got[2] != "echo hi" {
		t.Errorf("shellArgs on %s = %v, want [sh -c \"echo hi\"]", runtime.GOOS, got)
	}
}

// TestTaskkillArgsShape pins the Windows process-tree kill command's exact
// argv — the "equivalent assertion" this pass's Windows path gets even
// though the real-kill test only spawns processes on !windows (taskkillArgs
// is a pure function and compiles everywhere, so this runs on every OS).
func TestTaskkillArgsShape(t *testing.T) {
	got := taskkillArgs(4242)
	want := []string{"taskkill", "/T", "/F", "/PID", "4242"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("taskkillArgs(4242) = %v, want %v", got, want)
	}
}

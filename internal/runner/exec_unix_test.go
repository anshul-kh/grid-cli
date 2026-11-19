//go:build !windows

package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// alive reports whether pid still exists, by sending the null signal
// (syscall.Kill(pid, 0)) — the standard unix way to probe a process without
// affecting it. ESRCH means it is gone; EPERM means it exists but is owned
// by someone else (still "alive" for this test's purposes).
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// waitUntilDead polls pid for up to 2s and fails the test if it is still
// alive at the end — giving the OS a moment to reap a just-killed process
// without the test racing it.
func waitUntilDead(t *testing.T, label string, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("%s (pid %d) is still alive 2s after the run should have killed its process group", label, pid)
}

// TestRunKillsEntireProcessGroupOnTimeout is the pass 4 hard invariant
// (TASK.md §8 / plan pass 4 item 5): a command that itself spawns a
// grandchild which *ignores SIGTERM* must leave no survivor once the
// command's own timeout expires. killGroup sends SIGKILL to the whole
// process group (negative PID), which cannot be trapped, so this holds even
// though SIGKILL is a stronger signal than the grandchild defeats — the
// point under test is that the *group* (child + grandchild), not just the
// direct child, is gone, not which signal does it.
func TestRunKillsEntireProcessGroupOnTimeout(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	// The child ignores TERM, backgrounds a grandchild that also ignores
	// TERM and sleeps far longer than the test's timeout, records the
	// grandchild's pid, then waits on it — so the child stays alive long
	// enough for the group kill to matter, and the grandchild stays in the
	// same process group (it never calls setsid/setpgid itself).
	script := fmt.Sprintf(`trap '' TERM; ( trap '' TERM; sleep 30 ) & echo $! > %q; wait`, pidFile)

	env := NewEnv(t.TempDir(), nil)
	run := func(timeout time.Duration) error {
		_, err := env.Run(context.Background(), CmdSpec{
			Run:     []string{"sh", "-c", script},
			Timeout: timeout,
		})
		return err
	}

	err := run(300 * time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("Run error = %v, want ErrTimeout", err)
	}
	// A loaded CI runner (macOS especially) can take longer than 300ms just
	// to start `sh` and record the pid. That is not what this test is about,
	// so give the script a second, generous budget when it never got that
	// far; it still must time out well before the grandchild's 30s sleep.
	if _, statErr := os.Stat(pidFile); statErr != nil {
		err = run(5 * time.Second)
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("retry: Run error = %v, want ErrTimeout", err)
		}
	}

	raw, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("grandchild never recorded its pid (script failed to start?): %v", readErr)
	}
	var grandchildPID int
	if _, scanErr := fmt.Sscanf(string(raw), "%d", &grandchildPID); scanErr != nil {
		t.Fatalf("pid file %q unreadable: %v", raw, scanErr)
	}

	waitUntilDead(t, "grandchild (SIGTERM-ignoring)", grandchildPID)
}

// TestRunCapturesHighVolumeStdoutWithoutUnboundedGrowth is the real-process
// half of the 10 MB capture-cap criterion: `yes` can emit far more than the
// capture cap in a fraction of a second, so a short timeout guarantees the
// buffer would overflow the cap many times over if it were not bounded.
func TestRunCapturesHighVolumeStdoutWithoutUnboundedGrowth(t *testing.T) {
	env := NewEnv(t.TempDir(), nil)
	res, err := env.Run(context.Background(), CmdSpec{
		Run:     []string{"sh", "-c", "yes"},
		Timeout: 200 * time.Millisecond,
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("Run error = %v, want ErrTimeout", err)
	}
	if res == nil {
		t.Fatal("Run returned a nil result alongside ErrTimeout")
	}
	if len(res.Stdout) > env.CaptureCap {
		t.Errorf("captured %d bytes of stdout, want <= CaptureCap (%d)", len(res.Stdout), env.CaptureCap)
	}
	if !res.StdoutTruncated {
		t.Error("StdoutTruncated = false, want true (yes emits far more than the cap in 200ms)")
	}
}

// TestRunShellTrueWarns proves the shell:true escape hatch logs a verbose
// warning (GRID_SPEC §4 rule 1: "discouraged ... must be justified", TASK.md
// pass 4 item 4: "log a warning under --verbose").
func TestRunShellTrueWarns(t *testing.T) {
	var warnings []string
	env := NewEnv(t.TempDir(), nil)
	env.Warnf = func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	}
	_, err := env.Run(context.Background(), CmdSpec{
		Run:   []string{"echo", "hi"},
		Shell: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(warnings) == 0 {
		t.Fatal("shell: true produced no --verbose warning")
	}
}

// TestRunTimeoutReturnsErrTimeout proves a plain (no grandchild) long-running
// command surfaces the typed ErrTimeout rather than a bare context error, so
// callers can map it to status "error" (§4 rule 6) without string-matching.
func TestRunTimeoutReturnsErrTimeout(t *testing.T) {
	env := NewEnv(t.TempDir(), nil)
	_, err := env.Run(context.Background(), CmdSpec{
		Run:     []string{"sleep", "5"},
		Timeout: 100 * time.Millisecond,
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("Run error = %v, want ErrTimeout", err)
	}
}

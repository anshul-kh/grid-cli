//go:build windows

package runner

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRunKillsProcessTreeOnTimeoutWindows is the Windows side of the pass 4
// hard invariant (TASK.md §8): `ping` with a large count run via cmd /c
// stands in for a long-running tree (cmd.exe -> ping.exe share the new
// process group created by setSysProcAttr), and the short timeout must
// force the taskkill /T /F path (killGroup, exec_windows.go) rather than
// leaving ping running past the run's budget. The pure-function shape of
// that taskkill invocation is pinned platform-independently by
// TestTaskkillArgsShape in exec_test.go.
func TestRunKillsProcessTreeOnTimeoutWindows(t *testing.T) {
	env := NewEnv(t.TempDir(), nil)
	start := time.Now()
	_, err := env.Run(context.Background(), CmdSpec{
		Run:     []string{"cmd", "/c", "ping", "-n", "60", "127.0.0.1"},
		Timeout: 300 * time.Millisecond,
	})
	elapsed := time.Since(start)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("Run error = %v, want ErrTimeout", err)
	}
	// The process tree must be reaped well inside the 60s ping would
	// otherwise take; a generous bound keeps this robust on loaded CI
	// runners while still catching a taskkill that silently no-ops.
	if elapsed > 10*time.Second {
		t.Errorf("Run took %s to return after a 300ms timeout — the process tree likely outlived it", elapsed)
	}
}

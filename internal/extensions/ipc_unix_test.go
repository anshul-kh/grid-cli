//go:build !windows

package extensions

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/anshul-kh/grid-extensions/pkg/attest"
)

// spawnIgnoredOrphan backgrounds a grandchild that ignores SIGTERM and
// outlives the helper, recording its pid — the exec_unix_test.go orphan
// shape, so the test proves the group kill reaches past the direct child.
func spawnIgnoredOrphan() error {
	script := fmt.Sprintf(`trap '' TERM; ( trap '' TERM; sleep 30 ) & echo $! > %q; wait`, os.Getenv(ipcOrphanPidEnv))
	cmd := exec.Command("sh", "-c", script)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Start()
}

// alive reports whether pid still exists (the null-signal probe).
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// waitUntilDead polls pid for up to 2s, failing the test if it survives.
func waitUntilDead(t *testing.T, label string, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("%s (pid %d) is still alive 2s after the IPC call should have killed its process group", label, pid)
}

// TestIPCKillsEntireProcessGroupOnTimeout is ipc.go's hard invariant: an
// extension subprocess that spawns a SIGTERM-ignoring grandchild and then
// hangs leaves no survivor once the IPC deadline expires — the group is
// SIGKILLed, not just the direct child (and not on the polite signal).
func TestIPCKillsEntireProcessGroupOnTimeout(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	t.Setenv(ipcOrphanPidEnv, pidFile)
	key := attest.DeterministicTestKey()
	if _, err := callHelper(t, key, "orphan", "hang", 300*time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatalf("Call error = %v, want ErrTimeout", err)
	}

	raw, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("grandchild never recorded its pid (helper failed to start?): %v", readErr)
	}
	var grandchildPID int
	if _, scanErr := fmt.Sscanf(string(raw), "%d", &grandchildPID); scanErr != nil {
		t.Fatalf("pid file %q unreadable: %v", raw, scanErr)
	}
	waitUntilDead(t, "grandchild (SIGTERM-ignoring)", grandchildPID)
}

//go:build !windows

package runner

import (
	"os/exec"
	"syscall"
)

// setSysProcAttr makes the command its own process-group leader so a
// timeout can kill the whole group — the command plus every process it
// spawned — with a single negative-PID signal (TASK.md §4 / §8).
func setSysProcAttr(cmd *exec.Cmd) { SetProcessGroup(cmd) }

// SetProcessGroup is the exported form of setSysProcAttr, for subprocess
// managers outside this package that need the same discipline (the
// extension IPC client runs live pipes, not Env.Run's captured streams)
// and must not re-derive the per-OS mechanics.
func SetProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup kills the process group led by pid with SIGKILL. SIGKILL rather
// than SIGTERM is deliberate: the §8 orphan test spawns a grandchild that
// *ignores* SIGTERM, and an ignorable signal would leave orphans behind.
// ESRCH means the group is already gone, which is success here.
func killGroup(pid int) error { return KillGroup(pid) }

// KillGroup is the exported form of killGroup: SIGKILL the whole process
// group led by pid, treating an already-gone group as success. Same
// reasoning as killGroup's SIGKILL choice above.
func KillGroup(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

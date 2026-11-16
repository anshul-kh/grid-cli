//go:build windows

package runner

import (
	"os/exec"
	"syscall"
)

// setSysProcAttr starts the command in a new process group so a timeout can
// take down the whole tree with taskkill /T /F (the Windows equivalent of
// unix's Setpgid + negative-PID kill; full job objects are not needed because
// taskkill /T walks the descendant tree).
func setSysProcAttr(cmd *exec.Cmd) { SetProcessGroup(cmd) }

// SetProcessGroup is the exported form of setSysProcAttr (see exec_unix.go)
// — the Windows side starts the command in a new process group so
// KillGroup's taskkill /T walk reaches the whole tree.
func SetProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// killGroup kills the process tree rooted at pid via
// taskkill /T /F /PID — /T includes descendants, /F forces, so a child
// ignoring the polite console event cannot survive its budget.
func killGroup(pid int) error { return KillGroup(pid) }

// KillGroup is the exported form of killGroup (see exec_unix.go).
func KillGroup(pid int) error {
	return exec.Command(taskkillArgs(pid)[0], taskkillArgs(pid)[1:]...).Run()
}

//go:build unix

package scanexec

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// ownGroup makes cmd start in a process group of its own and makes the
// cancellation of its context kill that whole group, so the processes a
// scanner starts end with it.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}

// killGroup kills what is left of the process group of cmd, which has
// exited, such as a child process that still holds its output open.
func killGroup(cmd *exec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // ESRCH means the group is already gone, which is the goal
}

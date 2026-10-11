//go:build unix

package tool

import (
	"os/exec"
	"syscall"
)

// killProcessGroupOnCancel makes cancellation kill the command and every
// process it started, not only the shell.
func killProcessGroupOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

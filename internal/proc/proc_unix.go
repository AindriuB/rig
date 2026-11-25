//go:build !windows

package proc

import (
	"os/exec"
	"syscall"
)

// killTree puts the child in its own process group and, on cancel, kills the
// whole group. The returned hooks do nothing here.
func killTree(cmd *exec.Cmd) (afterStart func(), done func()) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return func() {}, func() {}
}

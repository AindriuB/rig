//go:build windows

package proc

import "os/exec"

// killTree kills the child on cancel. Windows has no process groups to signal;
// WaitDelay stops Run waiting on anything the child left behind.
func killTree(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return cmd.Process.Kill() }
}

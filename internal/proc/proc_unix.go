//go:build !windows

package proc

import (
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// killTree puts the child in its own process group and, on cancel, ends the
// whole group: SIGKILL at once, or with o.Grace SIGTERM first and SIGKILL once
// the grace has passed. done sweeps the group with SIGKILL if a cancel
// happened, so a descendant that outlived a gentle end does not linger.
func killTree(cmd *exec.Cmd, o Options) (afterStart func(), done func()) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var (
		mu        sync.Mutex
		cancelled bool
		timer     *time.Timer
	)
	cmd.Cancel = func() error {
		pgid := cmd.Process.Pid
		if o.Grace <= 0 {
			return syscall.Kill(-pgid, syscall.SIGKILL)
		}
		mu.Lock()
		defer mu.Unlock()
		cancelled = true
		err := syscall.Kill(-pgid, syscall.SIGTERM)
		timer = time.AfterFunc(o.Grace, func() { syscall.Kill(-pgid, syscall.SIGKILL) })
		return err
	}
	done = func() {
		mu.Lock()
		defer mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		if cancelled && cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	return func() {}, done
}

// detach makes the child a session leader, outside the caller's process group.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

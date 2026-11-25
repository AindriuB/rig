//go:build windows

package proc

import (
	"os/exec"
	"sync"

	"golang.org/x/sys/windows"
)

// killTree makes cancel end the whole process tree. Windows has no process
// groups to signal, so afterStart puts the child in a Job Object and cancel
// terminates the job. If the job cannot be made or assigned, cancel kills the
// direct child only. The job does not kill on close, so descendants of a
// successful run keep running, as on Unix. done closes the job handle.
func killTree(cmd *exec.Cmd) (afterStart func(), done func()) {
	var (
		mu  sync.Mutex
		job windows.Handle
	)
	cmd.Cancel = func() error {
		mu.Lock()
		h := job
		mu.Unlock()
		if h != 0 {
			if err := windows.TerminateJobObject(h, 1); err == nil {
				return nil
			}
		}
		return cmd.Process.Kill()
	}
	afterStart = func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			return
		}
		p, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
		if err != nil {
			windows.CloseHandle(h)
			return
		}
		defer windows.CloseHandle(p)
		if err := windows.AssignProcessToJobObject(h, p); err != nil {
			windows.CloseHandle(h)
			return
		}
		mu.Lock()
		job = h
		mu.Unlock()
	}
	done = func() {
		mu.Lock()
		defer mu.Unlock()
		if job != 0 {
			windows.CloseHandle(job)
			job = 0
		}
	}
	return afterStart, done
}

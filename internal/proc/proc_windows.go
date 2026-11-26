//go:build windows

package proc

import (
	"errors"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// killTree makes cancel end the whole process tree. Windows has no process
// groups to signal, so afterStart puts the child in a Job Object and cancel
// terminates the job. If the job cannot be made or assigned, cancel kills the
// direct child only. The job does not kill on close, so descendants of a
// successful run keep running, as on Unix. done closes the job handle.
func killTree(cmd *exec.Cmd, o Options) (afterStart func(), done func()) {
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
		if o.JobBreakaway {
			info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
			info.BasicLimitInformation.LimitFlags = jobLimitFlags(true)
			if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
				uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
				windows.CloseHandle(h)
				return
			}
		}
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

// jobLimitFlags returns the job's limit flags: none, or breakaway-allowed.
func jobLimitFlags(breakaway bool) uint32 {
	if breakaway {
		return windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
	}
	return 0
}

// detach starts the child outside the caller's console and process group, and
// outside its job when breakaway is true.
func detach(cmd *exec.Cmd, breakaway bool) {
	flags := uint32(windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS)
	if breakaway {
		flags |= windows.CREATE_BREAKAWAY_FROM_JOB
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
}

// breakawayDenied reports whether CreateProcess refused the breakaway flag.
func breakawayDenied(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED)
}

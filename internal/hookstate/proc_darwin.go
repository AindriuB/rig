package hookstate

import (
	"golang.org/x/sys/unix"
)

// processStart returns pid's start time (microseconds since the epoch) and
// whether the process exists.
func processStart(pid int) (uint64, bool, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		// An unknown pid comes back as a short read, which Go reports as EIO.
		if err == unix.ESRCH || err == unix.EIO {
			return 0, false, nil
		}
		return 0, false, err
	}
	if kp.Proc.P_pid != int32(pid) {
		return 0, false, nil
	}
	if kp.Proc.P_stat == 5 { // SZOMB
		return 0, false, nil
	}
	st := kp.Proc.P_starttime
	return uint64(st.Sec)*1_000_000 + uint64(st.Usec), true, nil
}

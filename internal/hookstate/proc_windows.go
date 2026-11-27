package hookstate

import (
	"golang.org/x/sys/windows"
)

// processStart returns pid's creation time (FILETIME ticks) and whether the
// process exists.
func processStart(pid int) (uint64, bool, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if err == windows.ERROR_INVALID_PARAMETER {
			return 0, false, nil
		}
		return 0, false, err
	}
	defer windows.CloseHandle(h)
	// A zero-timeout wait is signalled once the process has exited, even if
	// its exit code happens to be 259 (STILL_ACTIVE).
	ev, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return 0, false, err
	}
	if ev != uint32(windows.WAIT_TIMEOUT) {
		return 0, false, nil
	}
	var c, e, k, u windows.Filetime
	if err := windows.GetProcessTimes(h, &c, &e, &k, &u); err != nil {
		return 0, false, err
	}
	return uint64(c.HighDateTime)<<32 | uint64(c.LowDateTime), true, nil
}

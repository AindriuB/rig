package hookstate

import (
	"golang.org/x/sys/windows"
)

// processStart returns pid's creation time (FILETIME ticks) and whether the
// process exists.
func processStart(pid int) (uint64, bool, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if err == windows.ERROR_INVALID_PARAMETER {
			return 0, false, nil
		}
		return 0, false, err
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return 0, false, err
	}
	if code != 259 { // STILL_ACTIVE
		return 0, false, nil
	}
	var c, e, k, u windows.Filetime
	if err := windows.GetProcessTimes(h, &c, &e, &k, &u); err != nil {
		return 0, false, err
	}
	return uint64(c.HighDateTime)<<32 | uint64(c.LowDateTime), true, nil
}

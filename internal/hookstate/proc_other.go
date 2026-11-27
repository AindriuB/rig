//go:build !linux && !darwin && !windows

package hookstate

import "golang.org/x/sys/unix"

// processStart has no start time on this platform, so liveness is by pid
// alone.
func processStart(pid int) (uint64, bool, error) {
	err := unix.Kill(pid, 0)
	if err == unix.ESRCH {
		return 0, false, nil
	}
	if err != nil && err != unix.EPERM {
		return 0, false, err
	}
	return 0, true, nil
}

package hookstate

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// processStart returns pid's start time (clock ticks since boot) and whether
// the process exists.
func processStart(pid int) (uint64, bool, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if os.IsNotExist(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')') // the name may hold spaces and parens
	if i < 0 {
		return 0, false, fmt.Errorf("parse /proc/%d/stat", pid)
	}
	f := strings.Fields(s[i+1:])
	// f[0] is field 3 (state); starttime is field 22.
	if len(f) < 20 {
		return 0, false, fmt.Errorf("parse /proc/%d/stat", pid)
	}
	if f[0] == "Z" || f[0] == "X" {
		return 0, false, nil
	}
	v, err := strconv.ParseUint(f[19], 10, 64)
	if err != nil {
		return 0, false, err
	}
	return v, true, nil
}

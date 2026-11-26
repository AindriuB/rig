//go:build !windows

package proc

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSpawnDetachesAndStripsHookVars(t *testing.T) {
	t.Setenv("RIG_HOOK_PARENT", "p")
	out := filepath.Join(t.TempDir(), "env")
	start := time.Now()
	pid, refused, err := Spawn(Options{Env: []string{"RIG_HOOK_OPT=o"}}, "sh", "-c", "env > "+out+"; sleep 10")
	if err != nil {
		t.Fatal(err)
	}
	if refused {
		t.Fatal("breakaway refused on unix")
	}
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
	if time.Since(start) > time.Second {
		t.Fatalf("Spawn took %v", time.Since(start))
	}
	if pgid, _ := syscall.Getpgid(pid); pgid != pid {
		t.Fatalf("pgid %d, want %d", pgid, pid)
	}
	var data []byte
	for i := 0; i < 100 && len(data) == 0; i++ {
		time.Sleep(50 * time.Millisecond)
		data, _ = os.ReadFile(out)
	}
	if len(data) == 0 || strings.Contains(string(data), "RIG_HOOK_") {
		t.Fatalf("env %q", data)
	}
}

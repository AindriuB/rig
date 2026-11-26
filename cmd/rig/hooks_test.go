//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func spawnPid(t *testing.T, out string) int {
	t.Helper()
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		t.Fatalf("pid from %q: %v", out, err)
	}
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}

func TestHooksSpawnReturnsAtOnce(t *testing.T) {
	start := time.Now()
	r := rig(t, t.TempDir(), nil, nil, "hooks", "spawn", "--", "sleep", "10")
	if r.code != 0 || r.errOut != "" {
		t.Fatalf("exit %d, stderr %q", r.code, r.errOut)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
	pid := spawnPid(t, r.out)
	time.Sleep(200 * time.Millisecond)
	if syscall.Kill(pid, 0) != nil {
		t.Fatalf("pid %d is not running", pid)
	}
}

func TestHooksSpawnStripsHookEnv(t *testing.T) {
	t.Setenv("RIG_HOOK_EVENT", "e")
	t.Setenv("RIG_HOOK_TASK", "t")
	t.Setenv("RIG_HOOK_NAME", "n")
	t.Setenv("RIG_WORKFLOW", "w")
	dir := t.TempDir()
	out := filepath.Join(dir, "env")
	r := rig(t, dir, nil, nil, "hooks", "spawn", "sh", "-c", "env > "+out)
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q", r.code, r.errOut)
	}
	var data []byte
	for i := 0; i < 100 && len(data) == 0; i++ {
		time.Sleep(50 * time.Millisecond)
		data, _ = os.ReadFile(out)
	}
	env := string(data)
	if len(data) == 0 || strings.Contains(env, "RIG_HOOK_") {
		t.Fatalf("env %q", env)
	}
	if !strings.Contains(env, "\nPATH=") && !strings.HasPrefix(env, "PATH=") || !strings.Contains(env, "RIG_WORKFLOW=w") {
		t.Fatalf("PATH or RIG_WORKFLOW missing: %q", env)
	}
}

func TestHooksSpawnErrors(t *testing.T) {
	for _, args := range [][]string{
		{"hooks", "spawn"},
		{"hooks", "spawn", "--"},
		{"hooks", "spawn", "--", "rig-no-such-program"},
	} {
		r := rig(t, t.TempDir(), nil, nil, args...)
		if r.code != 2 || r.out != "" || strings.Count(r.errOut, "\n") != 1 || !strings.HasPrefix(r.errOut, "rig hooks spawn: ") {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, r.code, r.out, r.errOut)
		}
	}
}

func TestHooksSpawnJSON(t *testing.T) {
	r := rig(t, t.TempDir(), nil, nil, "hooks", "spawn", "--json", "--", "sleep", "10")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q", r.code, r.errOut)
	}
	pid := spawnPid(t, strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(r.out), `{"pid":`), "}"))
	if want := `{"pid":` + strconv.Itoa(pid) + "}\n"; r.out != want {
		t.Fatalf("stdout %q, want %q", r.out, want)
	}
}

func TestHooksUsage(t *testing.T) {
	for _, args := range [][]string{{"hooks"}, {"hooks", "bogus"}} {
		r := rig(t, t.TempDir(), nil, nil, args...)
		if r.code != 2 || r.out != "" || !strings.Contains(r.errOut, "rig hooks spawn") {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, r.code, r.out, r.errOut)
		}
	}
}

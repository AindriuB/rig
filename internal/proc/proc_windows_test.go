//go:build windows

package proc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestMain lets the test binary act as the child and grandchild. RIG_PROC_ROLE
// is "child-wait" (start a grandchild, print its pid, sleep), "child-exit"
// (same, then exit 0) or "grandchild" (sleep).
func TestMain(m *testing.M) {
	switch os.Getenv("RIG_PROC_ROLE") {
	case "child-wait", "child-exit":
		exe, _ := os.Executable()
		gc := exec.Command(exe)
		gc.Env = append(os.Environ(), "RIG_PROC_ROLE=grandchild")
		if err := gc.Start(); err != nil {
			os.Exit(2)
		}
		fmt.Println(gc.Process.Pid)
		if os.Getenv("RIG_PROC_ROLE") == "child-exit" {
			os.Exit(0)
		}
		time.Sleep(time.Minute)
		os.Exit(0)
	case "spawn-wait":
		pid, refused, err := Start(Options{Env: []string{"RIG_PROC_ROLE=grandchild"}}, os.Args[0])
		if err != nil {
			os.Exit(2)
		}
		if os.Getenv("RIG_PROC_EXPECT") == "refused" {
			fmt.Println(refused)
			fmt.Println(pid)
			os.Exit(0)
		}
		fmt.Println(pid)
		time.Sleep(time.Minute)
		os.Exit(0)
	case "grandchild":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func running(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	return windows.GetExitCodeProcess(h, &code) == nil && code == 259 // STILL_ACTIVE
}

func grandchildPid(t *testing.T, out []byte) int {
	t.Helper()
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("grandchild pid from %q: %v", out, err)
	}
	t.Cleanup(func() {
		if p, err := os.FindProcess(pid); err == nil {
			p.Kill()
		}
	})
	return pid
}

func waitGone(pid int) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !running(pid) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return !running(pid)
}

func helperExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestRunTimeoutKillsGrandchild(t *testing.T) {
	res, err := Run(context.Background(), Options{Timeout: 3 * time.Second, Env: []string{"RIG_PROC_ROLE=child-wait"}}, helperExe(t))
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if pid := grandchildPid(t, res.Stdout); !waitGone(pid) {
		t.Fatalf("grandchild %d still running", pid)
	}
}

func TestRunSuccessLeavesGrandchild(t *testing.T) {
	res, err := Run(context.Background(), Options{Env: []string{"RIG_PROC_ROLE=child-exit"}}, helperExe(t))
	if err != nil {
		t.Fatal(err)
	}
	pid := grandchildPid(t, res.Stdout)
	time.Sleep(500 * time.Millisecond)
	if !running(pid) {
		t.Fatalf("grandchild %d was killed by a successful run", pid)
	}
}

func TestRunCancelKillsGrandchild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(3 * time.Second)
		cancel()
	}()
	res, err := Run(ctx, Options{Env: []string{"RIG_PROC_ROLE=child-wait"}}, helperExe(t))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if pid := grandchildPid(t, res.Stdout); !waitGone(pid) {
		t.Fatalf("grandchild %d still running", pid)
	}
}

func TestJobLimitFlags(t *testing.T) {
	if jobLimitFlags(false) != 0 || jobLimitFlags(true)&windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK == 0 {
		t.Fatal("breakaway flag not wired")
	}
}

func TestStartSurvivesBreakawayRunTimeout(t *testing.T) {
	res, err := Run(context.Background(), Options{Timeout: 3 * time.Second, JobBreakaway: true, Env: []string{"RIG_PROC_ROLE=spawn-wait"}}, helperExe(t))
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	pid := grandchildPid(t, res.Stdout)
	time.Sleep(500 * time.Millisecond)
	if !running(pid) {
		t.Fatalf("spawned child %d died with the job", pid)
	}
}

func TestStartFallsBackWhenBreakawayRefused(t *testing.T) {
	res, err := Run(context.Background(), Options{Timeout: 3 * time.Second, JobBreakaway: false, Env: []string{"RIG_PROC_ROLE=spawn-wait", "RIG_PROC_EXPECT=refused"}}, helperExe(t))
	if err != nil {
		t.Fatalf("run: %v (a failed Start exits 2)", err)
	}
	lines := strings.Fields(string(res.Stdout))
	if len(lines) != 2 || lines[0] != "true" {
		t.Fatalf("output %q, want refused=true and a pid", res.Stdout)
	}
	pid := grandchildPid(t, []byte(lines[1]))
	time.Sleep(500 * time.Millisecond)
	if !running(pid) {
		t.Fatalf("spawned child %d not running", pid)
	}
}

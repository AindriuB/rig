//go:build !windows

package proc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunCapturesOutputAndExitCode(t *testing.T) {
	res, err := Run(context.Background(), Options{}, "sh", "-c", "echo out; echo err >&2; exit 3")
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 3 {
		t.Fatalf("err = %v, want exit 3", err)
	}
	if string(res.Stdout) != "out\n" || string(res.Stderr) != "err\n" || string(ee.Stderr) != "err\n" {
		t.Fatalf("stdout %q stderr %q", res.Stdout, res.Stderr)
	}
}

func TestRunPassesArgumentsVerbatim(t *testing.T) {
	arg := "a b; $(touch pwned) `id` && x"
	res, err := Run(context.Background(), Options{Dir: t.TempDir()}, "printf", "%s", arg)
	if err != nil || string(res.Stdout) != arg {
		t.Fatalf("got %q, %v", res.Stdout, err)
	}
}

func TestRunDirAndEnv(t *testing.T) {
	dir := t.TempDir()
	res, err := Run(context.Background(), Options{Dir: dir, Env: []string{"RIG_X=1"}}, "sh", "-c", "pwd -P; echo $RIG_X")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(dir)
	if got := strings.Split(string(res.Stdout), "\n"); got[0] != want || got[1] != "1" {
		t.Fatalf("got %q", res.Stdout)
	}
}

func TestRunMissingProgram(t *testing.T) {
	_, err := Run(context.Background(), Options{}, "rig-no-such-program")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunTimeoutKillsGrandchildren(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "survived")
	start := time.Now()
	// The grandchild holds the output pipes and would write the marker.
	_, err := Run(context.Background(), Options{Timeout: 300 * time.Millisecond},
		"sh", "-c", "(sleep 3; touch '"+marker+"') & sleep 30")
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want timeout", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
	time.Sleep(3500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("grandchild outlived the timeout")
	}
}

func TestRunCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	_, err := Run(ctx, Options{}, "sleep", "30")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunGraceLetsTermTrapFinish(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	script := "trap 'echo done > " + marker + "; exit 0' TERM; while :; do sleep 0.1; done"
	_, err := Run(context.Background(), Options{Timeout: 500 * time.Millisecond, Grace: 3 * time.Second}, "sh", "-c", script)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker missing: %v", err)
	}
}

func TestRunGraceKillsTermIgnorer(t *testing.T) {
	start := time.Now()
	_, err := Run(context.Background(), Options{Timeout: 500 * time.Millisecond, Grace: time.Second}, "sh", "-c", "trap '' TERM; while :; do sleep 0.1; done")
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if d := time.Since(start); d > 500*time.Millisecond+time.Second+2*time.Second {
		t.Fatalf("took %v", d)
	}
}

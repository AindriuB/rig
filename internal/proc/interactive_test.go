//go:build !windows

package proc

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestInteractiveWiresStdioAndReturnsCode(t *testing.T) {
	var out, errb bytes.Buffer
	o := Options{Stdin: strings.NewReader("hello\n"), Stdout: &out, Stderr: &errb}
	code, err := Interactive(context.Background(), o, "sh", "-c", "read x; echo got $x; echo oops >&2; exit 4")
	if err != nil || code != 4 {
		t.Fatalf("code %d, err %v", code, err)
	}
	if out.String() != "got hello\n" || errb.String() != "oops\n" {
		t.Fatalf("stdout %q stderr %q", out.String(), errb.String())
	}
}

func TestInteractivePassesArgumentsVerbatim(t *testing.T) {
	arg := "a b; $(touch pwned) `id` && x"
	var out bytes.Buffer
	code, err := Interactive(context.Background(), Options{Dir: t.TempDir(), Stdout: &out}, "printf", "%s", arg)
	if err != nil || code != 0 || out.String() != arg {
		t.Fatalf("got %q, %d, %v", out.String(), code, err)
	}
}

func TestInteractiveMissingProgram(t *testing.T) {
	_, err := Interactive(context.Background(), Options{}, "rig-no-such-program")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestInteractiveTimeout(t *testing.T) {
	start := time.Now()
	var out bytes.Buffer
	_, err := Interactive(context.Background(), Options{Timeout: 300 * time.Millisecond, Stdout: &out},
		"sh", "-c", "sleep 30 & sleep 30")
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want timeout", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestInteractiveStaysInParentProcessGroup(t *testing.T) {
	var out bytes.Buffer
	code, err := Interactive(context.Background(), Options{Stdout: &out}, "sh", "-c", "ps -o pgid= -p $$")
	if err != nil || code != 0 {
		t.Fatalf("code %d, err %v", code, err)
	}
	got := strings.TrimSpace(out.String())
	if want := strconv.Itoa(syscall.Getpgrp()); got != want {
		t.Fatalf("child pgid %q, want parent's %q", got, want)
	}
}

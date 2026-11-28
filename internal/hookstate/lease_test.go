package hookstate

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func writeLease(t *testing.T, dir, hook string, h Holder) {
	t.Helper()
	if err := writeAtomic(leasePath(dir, hook), h); err != nil {
		t.Fatal(err)
	}
}

func ownStart(t *testing.T) uint64 {
	t.Helper()
	s, ok, err := processStart(os.Getpid())
	if err != nil || !ok {
		t.Fatalf("own start: %v %v", ok, err)
	}
	return s
}

func TestAC3LiveHolderKeepsLeaseDespiteOldBeat(t *testing.T) {
	dir := t.TempDir()
	l, err := Acquire(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	h := l.me
	h.Beat = time.Now().Add(-24 * time.Hour)
	writeLease(t, dir, "chat", h)
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(leasePath(dir, "chat"), old, old)
	_, err = Acquire(dir, "chat")
	var he *HeldError
	if !errors.As(err, &he) || he.Holder.PID != os.Getpid() {
		t.Fatalf("err %v", err)
	}
	beat, live, err := Beat(dir, "chat")
	if err != nil || !live || time.Since(beat) < 23*time.Hour {
		t.Fatalf("beat %v %v %v", beat, live, err)
	}
	if err := l.Touch(); err != nil {
		t.Fatal(err)
	}
	if beat, _, _ := Beat(dir, "chat"); time.Since(beat) > time.Minute {
		t.Fatal("touch did not refresh")
	}
}

func TestAC4ExitedChildIsTakenOver(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperSleep$")
	cmd.Env = append(os.Environ(), "HOOKSTATE_HELPER_SLEEP=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := false
	defer func() {
		if !reaped {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	pid := cmd.Process.Pid
	var start uint64
	for i := 0; i < 100; i++ {
		s, ok, err := processStart(pid)
		if err == nil && ok {
			start = s
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if start == 0 {
		t.Fatal("no start time for running child")
	}
	dir := t.TempDir()
	writeLease(t, dir, "chat", Holder{PID: pid, Start: start, Token: "x", Beat: time.Now()})
	if _, live, _ := Beat(dir, "chat"); !live {
		t.Fatal("running child reported not live")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	// Not reaped yet: the start time is still readable, so only the alive
	// bit (zombie, exited) can say the holder is gone.
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, live, _ := Beat(dir, "chat")
		if !live {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("exited child still reported live")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := Acquire(dir, "chat"); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	reaped = true
}

func TestHelperSleep(t *testing.T) {
	if os.Getenv("HOOKSTATE_HELPER_SLEEP") != "1" {
		return
	}
	time.Sleep(time.Minute)
}

func TestAC5DifferentStartTimeIsTakenOver(t *testing.T) {
	dir := t.TempDir()
	writeLease(t, dir, "chat", Holder{PID: os.Getpid(), Start: ownStart(t) + 12345, Token: "x", Beat: time.Now()})
	if _, err := Acquire(dir, "chat"); err != nil {
		t.Fatal(err)
	}
}

func TestAC6OneWinnerOnStaleLease(t *testing.T) {
	dir := t.TempDir()
	writeLease(t, dir, "chat", Holder{PID: os.Getpid(), Start: ownStart(t) + 1, Token: "x", Beat: time.Now()})
	var wins atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := Acquire(dir, "chat"); err == nil {
				wins.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if n := wins.Load(); n != 1 {
		t.Fatalf("%d winners", n)
	}
}

func TestAC7ReleaseAfterTakeoverKeepsNewHolder(t *testing.T) {
	dir := t.TempDir()
	old, err := Acquire(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	// Make the first lease stale, then let another acquirer take over.
	h := old.me
	h.Start++
	writeLease(t, dir, "chat", h)
	nl, err := Acquire(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Touch(); err == nil {
		t.Fatal("Touch after takeover succeeded")
	}
	if h, _ := readLease(leasePath(dir, "chat")); h == nil || h.Token != nl.me.Token {
		t.Fatal("Touch disturbed the new holder's lease")
	}
	if err := old.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(dir, "chat"); err == nil {
		t.Fatal("new holder's lease was removed")
	}
	if err := nl.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(dir, "chat"); err != nil {
		t.Fatal(err)
	}
}

func TestUnparsableLeaseIsStale(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(leasePath(dir, "chat"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(dir, "chat"); err != nil {
		t.Fatal(err)
	}
}

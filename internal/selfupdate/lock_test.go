package selfupdate

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLockPathIsBesideTheBinary(t *testing.T) {
	got := lockPath(filepath.Join("d", "rig.exe"))
	if got != filepath.Join("d", ".rig.exe.update.lock") {
		t.Fatal(got)
	}
}

// I1: a held lock refuses Update with Check "lock", zero downloads.
func TestUpdateRefusedWhileLockHeld(t *testing.T) {
	exe, old := installed(t)
	l, err := tryLock(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer l.release()
	downloads := 0
	inner := fakeRelease("v0.5.3", "new rig", "sig")
	r := Update(context.Background(), opts(exe, "0.5.2", "", rt(func(q *http.Request) (*http.Response, error) {
		if strings.Contains(q.URL.Path, "/rig-linux-amd64") {
			downloads++
		}
		return inner.RoundTrip(q)
	})))
	if r.Action != ActionRefused || r.Check != "lock" || r.ExitCode() != 1 ||
		r.Message != "another update of "+exe+" is in progress" {
		t.Fatalf("%+v", r)
	}
	if downloads != 0 {
		t.Fatalf("%d downloads", downloads)
	}
	unchanged(t, exe, old)
}

// I1 across processes, on every OS.
func TestUpdateRefusedWhileAnotherProcessHoldsTheLock(t *testing.T) {
	exe, old := installed(t)
	holdInChild(t, exe)
	r := Update(context.Background(), opts(exe, "0.5.2", "", fakeRelease("v0.5.3", "new rig", "sig")))
	if r.Action != ActionRefused || r.Check != "lock" {
		t.Fatalf("%+v", r)
	}
	unchanged(t, exe, old)
}

// I2: a leftover unheld lock file does not block.
func TestLeftoverLockFileDoesNotBlock(t *testing.T) {
	exe, _ := installed(t)
	if err := os.WriteFile(lockPath(exe), []byte("pid 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := Update(context.Background(), opts(exe, "0.5.2", "", fakeRelease("v0.5.3", "new rig", "sig")))
	if r.Action != ActionUpdated {
		t.Fatalf("%+v", r)
	}
}

// I2: the lock file stays after Update and can be taken again.
func TestLockFileRemainsAndIsFreeAfterUpdate(t *testing.T) {
	exe, _ := installed(t)
	r := Update(context.Background(), opts(exe, "0.5.2", "", fakeRelease("v0.5.3", "new rig", "sig")))
	if r.Action != ActionUpdated {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(lockPath(exe)); err != nil {
		t.Fatal("lock file removed:", err)
	}
	l, err := tryLock(exe)
	if err != nil {
		t.Fatal("lock still held:", err)
	}
	l.release()
}

// I2: killing the holder frees the lock at once.
func TestKillingTheHolderFreesTheLock(t *testing.T) {
	exe, _ := installed(t)
	child := holdInChild(t, exe)
	if _, err := tryLock(exe); !errors.Is(err, errLockBusy) {
		t.Fatalf("want busy, got %v", err)
	}
	_ = child.Process.Kill()
	_ = child.Wait()
	deadline := time.Now().Add(2 * time.Second)
	for {
		l, err := tryLock(exe)
		if err == nil {
			l.release()
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("lock not freed:", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// I6: the guard refuses the real network.
func TestGuardRefusesTheNetwork(t *testing.T) {
	_, err := NewClient(nil).Lookup(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), errNetworkInTest.Error()) {
		t.Fatalf("got %v", err)
	}
}

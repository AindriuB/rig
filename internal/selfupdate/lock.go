package selfupdate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// errLockBusy means another process holds the update lock.
var errLockBusy = errors.New("selfupdate: update lock is held")

// lockPath is the lock file for an install: beside the resolved binary, as
// .<base>.update.lock.
func lockPath(target string) string {
	return filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+".update.lock")
}

// updateLock is a kernel advisory lock on the lock file. The file is never
// deleted; the kernel drops the lock when the holder closes it or dies.
type updateLock struct{ f *os.File }

// tryLock takes the lock without waiting. It returns errLockBusy when another
// holder has it and a *NotWritableError when the lock file cannot be opened
// for want of permission.
func tryLock(target string) (*updateLock, error) {
	path := lockPath(target)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return nil, &NotWritableError{Dir: filepath.Dir(path), Err: err}
		}
		return nil, err
	}
	if err := lockFileNB(f); err != nil {
		_ = f.Close()
		if errors.Is(err, errLockBusy) {
			return nil, err
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	// Diagnostics only: who holds it and since when.
	_ = f.Truncate(0)
	_, _ = fmt.Fprintf(f, "pid %d\nstarted %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	return &updateLock{f: f}, nil
}

// release unlocks and closes. The file stays.
func (l *updateLock) release() {
	if l == nil || l.f == nil {
		return
	}
	unlockFile(l.f)
	_ = l.f.Close()
	l.f = nil
}

package gitref

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const (
	// lockWait is how long a write waits for the store lock before giving up.
	lockWait = 60 * time.Second
	// lockStale is the age (by mtime) past which a lock file is taken to have
	// been left by a writer that died, and is removed.
	lockStale = 60 * time.Second

	minLockPoll = 2 * time.Millisecond
	maxLockPoll = 10 * time.Millisecond
)

// lock takes the store-wide write lock, <git common dir>/rig/store.lock, and
// returns the function that releases it. The lock file is created with
// O_EXCL and holds the pid and a random token; release removes it only if it
// still holds that token. While another writer holds it, lock polls every
// 2-10 ms for up to s.lockWait, then returns a wrapped ErrConflict. A lock
// file older than lockStale is removed and the take retried.
//
// The compare-and-swap in commit protects every existing ref, whatever the
// lock does. Uniqueness of external links for new tasks relies on the lock:
// two writers can both break one stale lock and briefly both hold it, and
// then each may create a task for the same issue. That is best-effort across
// a stale-lock break; the duplicates show as ErrAmbiguous on Resolve.
func (s *Store) lock(ctx context.Context) (func(), error) {
	s.mu.Lock()
	release, err := s.lockFile(ctx)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	return func() {
		release()
		s.mu.Unlock()
	}, nil
}

func (s *Store) lockFile(ctx context.Context) (func(), error) {
	if s.lockPath == "" {
		common, err := s.git.CommonDir(ctx)
		if err != nil {
			return nil, fmt.Errorf("store lock: %w", err)
		}
		s.lockPath = filepath.Join(common, "rig", "store.lock")
	}
	path := s.lockPath
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("store lock: %w", err)
	}
	tok := make([]byte, 16)
	if _, err := crand.Read(tok); err != nil {
		return nil, fmt.Errorf("store lock: %w", err)
	}
	content := []byte(fmt.Sprintf("%d %s\n", os.Getpid(), hex.EncodeToString(tok)))

	deadline := time.Now().Add(s.lockWait)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		held, err := tryLock(path, content)
		if err != nil {
			return nil, fmt.Errorf("store lock: %w", err)
		}
		if !held {
			return func() { unlockFile(path, content) }, nil
		}
		if fi, err := os.Stat(path); err == nil && time.Since(fi.ModTime()) > lockStale {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("store lock: remove stale %s: %w", path, err)
			}
			continue
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("store lock %s held for %s: %w", path, s.lockWait, ErrConflict)
		}
		poll := minLockPoll + rand.N(maxLockPoll-minLockPoll+1)
		if err := sleepCtx(ctx, poll); err != nil {
			return nil, err
		}
	}
}

// tryLock creates path holding content. held reports that another writer
// has the lock.
func tryLock(path string, content []byte) (held bool, err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		// On Windows a lock file another writer is deleting refuses access.
		if errors.Is(err, fs.ErrExist) || (runtime.GOOS == "windows" && errors.Is(err, fs.ErrPermission)) {
			return true, nil
		}
		return false, err
	}
	_, werr := f.Write(content)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(path)
		return false, err
	}
	return false, nil
}

// unlockFile removes path if it still holds content, so a writer whose lock
// was broken as stale does not remove its successor's.
func unlockFile(path string, content []byte) {
	if got, err := os.ReadFile(path); err == nil && bytes.Equal(got, content) {
		_ = os.Remove(path)
	}
}

package hookstate

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Holder is who a lease names.
type Holder struct {
	PID   int       `json:"pid"`
	Start uint64    `json:"start"` // process start time, in the platform's own unit
	Token string    `json:"token"`
	Beat  time.Time `json:"beat"`
}

// HeldError reports that a live process holds the lease.
type HeldError struct{ Holder Holder }

func (e *HeldError) Error() string {
	return fmt.Sprintf("hook is delivered by pid %d (last heartbeat %s)", e.Holder.PID, e.Holder.Beat.Format(time.RFC3339))
}

// Lease is a held hook lease.
type Lease struct {
	path string
	me   Holder
}

func leasePath(dir, hook string) string { return filepath.Join(dir, hook+".lease") }

// holderLive reports whether the process a lease names is still the one that
// took it. A lease is never stale by age, only when that process is gone or
// its pid now belongs to a process started at another time.
func holderLive(h Holder) bool {
	start, alive, err := processStart(h.PID)
	if err != nil {
		return true // cannot tell: keep the lease rather than steal it
	}
	return alive && start == h.Start
}

func readLease(path string) (*Holder, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var h Holder
	if err := json.Unmarshal(b, &h); err != nil {
		return nil, nil // unreadable lease names no process: stale
	}
	return &h, nil
}

// Acquire takes hook's lease in dir. If a live process holds it, it returns
// a *HeldError naming the holder. A stale lease is taken over under a short
// lock, so of any number of racing callers exactly one wins.
func Acquire(dir, hook string) (*Lease, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("acquire lease: %w", err)
	}
	path := leasePath(dir, hook)
	unlock, err := lockFile(path + ".lock")
	if err != nil {
		return nil, fmt.Errorf("acquire lease: %w", err)
	}
	defer unlock()
	if h, err := readLease(path); err != nil {
		return nil, fmt.Errorf("acquire lease: %w", err)
	} else if h != nil && holderLive(*h) {
		return nil, &HeldError{Holder: *h}
	}
	pid := os.Getpid()
	start, _, err := processStart(pid)
	if err != nil {
		return nil, fmt.Errorf("acquire lease: %w", err)
	}
	var tok [8]byte
	if _, err := rand.Read(tok[:]); err != nil {
		return nil, fmt.Errorf("acquire lease: %w", err)
	}
	l := &Lease{path: path, me: Holder{PID: pid, Start: start, Token: hex.EncodeToString(tok[:]), Beat: time.Now().UTC()}}
	if err := writeAtomic(path, l.me); err != nil {
		return nil, fmt.Errorf("acquire lease: %w", err)
	}
	return l, nil
}

// Touch rewrites the heartbeat time. It never affects staleness. It fails if
// the lease is no longer this process's.
func (l *Lease) Touch() error {
	unlock, err := lockFile(l.path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	h, err := readLease(l.path)
	if err != nil {
		return err
	}
	if h == nil || h.Token != l.me.Token {
		return fmt.Errorf("lease %s was taken over", l.path)
	}
	l.me.Beat = time.Now().UTC()
	return writeAtomic(l.path, l.me)
}

// Release removes the lease only if this process still owns it.
func (l *Lease) Release() error {
	unlock, err := lockFile(l.path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	h, err := readLease(l.path)
	if err != nil {
		return err
	}
	if h == nil || h.Token != l.me.Token {
		return nil
	}
	if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Beat returns the holder's last heartbeat and whether its process is live.
// With no lease it returns the zero time and false.
func Beat(dir, hook string) (time.Time, bool, error) {
	h, err := readLease(leasePath(dir, hook))
	if err != nil {
		return time.Time{}, false, err
	}
	if h == nil {
		return time.Time{}, false, nil
	}
	return h.Beat, holderLive(*h), nil
}

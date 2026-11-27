// Package hookstate stores the delivery state of each queued hook (its cursor
// and its parked events) and a liveness lease that lets one runner at a time
// deliver a hook.
package hookstate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/AindriuB/rig/internal/store"
)

// Format is the state file format this rig writes and reads.
const Format = 1

// Parked is one event the runner has stopped retrying.
type Parked struct {
	Attempts int       `json:"attempts"`
	Reason   string    `json:"reason"`
	At       time.Time `json:"at"`
}

// State is one hook's delivery state. Cursor is nil until the hook has
// started. Parked is keyed by task id ("" for repository notes), then event id.
type State struct {
	Format int                          `json:"format"`
	Cursor *store.Cursor                `json:"cursor"`
	Parked map[string]map[string]Parked `json:"parked"`
}

// Path is where hook's state lives, under the repository's common dir.
func Path(common, workflow, hook string) string {
	return filepath.Join(common, "rig", "hooks", workflow, hook+".json")
}

func newState() *State {
	return &State{Format: Format, Parked: map[string]map[string]Parked{}}
}

// Load reads the state at path. A missing file returns nil and no error.
// On Windows a read outside Update can make a concurrent Update's rename
// fail; callers that read while a runner writes should read inside Update.
func Load(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read hook state: %w", err)
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("read hook state %s: %w", path, err)
	}
	if s.Format != Format {
		return nil, fmt.Errorf("read hook state %s: format %d is not one this rig reads", path, s.Format)
	}
	if s.Parked == nil {
		s.Parked = map[string]map[string]Parked{}
	}
	return &s, nil
}

// Update reads the state (a new one if none), applies fn and writes the
// result through a temporary file and a rename, all under a short lock, so
// concurrent updaters never lose each other's change. If fn fails nothing is
// written.
func Update(path string, fn func(*State) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("update hook state: %w", err)
	}
	unlock, err := lockFile(path + ".lock")
	if err != nil {
		return fmt.Errorf("update hook state: %w", err)
	}
	defer unlock()
	s, err := Load(path)
	if err != nil {
		return err
	}
	if s == nil {
		s = newState()
	}
	if err := fn(s); err != nil {
		return err
	}
	s.Format = Format
	return writeAtomic(path, s)
}

func writeAtomic(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	tmp := f.Name()
	_, werr := f.Write(append(b, '\n'))
	if werr == nil {
		werr = f.Sync()
	}
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp)
		if werr == nil {
			werr = cerr
		}
		return fmt.Errorf("write %s: %w", path, werr)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

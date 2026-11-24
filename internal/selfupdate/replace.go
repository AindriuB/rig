package selfupdate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// NotWritableError reports that the directory holding the rig binary cannot
// be written, so it cannot be replaced in place.
type NotWritableError struct {
	Dir string
	Err error
}

func (e *NotWritableError) Error() string {
	return fmt.Sprintf("directory %s is not writable: %v", e.Dir, e.Err)
}

func (e *NotWritableError) Unwrap() error { return e.Err }

// Staged is a fully written new binary sitting beside its target.
type Staged struct {
	target string
	path   string
	swap   func(target, staged string) error
}

// Path is the staged file, which can be run before Commit.
func (s Staged) Path() string { return s.path }

// Stage resolves exe through symlinks and writes data to a synced temp file in
// the same directory, with the target's permission bits.
func Stage(exe string, data []byte) (Staged, error) {
	target, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return Staged{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return Staged{}, err
	}
	dir := filepath.Dir(target)
	f, err := os.CreateTemp(dir, "."+filepath.Base(target)+".new-*")
	if err != nil {
		if errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrExist) {
			return Staged{}, &NotWritableError{Dir: dir, Err: err}
		}
		return Staged{}, err
	}
	name := f.Name()
	fail := func(err error) (Staged, error) {
		f.Close()
		os.Remove(name)
		return Staged{}, err
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Chmod(info.Mode().Perm()); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return Staged{}, err
	}
	return Staged{target: target, path: name, swap: platformSwap}, nil
}

// Commit puts the staged file in place of the target.
func (s Staged) Commit() error {
	if err := s.swap(s.target, s.path); err != nil {
		return err
	}
	return nil
}

// Discard removes the staged file and leaves the target alone.
func (s Staged) Discard() error {
	if err := os.Remove(s.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// CleanupOld removes a stale <exe>.old left by a Windows swap. Errors are ignored.
func CleanupOld(exe string) {
	_ = os.Remove(exe + ".old")
}

// renameOver is the Unix strategy: one rename over the target.
func renameOver(target, staged string) error {
	return os.Rename(staged, target)
}

// windowsSwap moves the target aside to <target>.old, then renames the staged
// file into place, restoring the target if that second rename fails.
func windowsSwap(target, staged string) error {
	old := target + ".old"
	_ = os.Remove(old)
	if err := os.Rename(target, old); err != nil {
		return err
	}
	if err := os.Rename(staged, target); err != nil {
		if rerr := os.Rename(old, target); rerr != nil {
			return fmt.Errorf("%w (restoring %s also failed: %v)", err, target, rerr)
		}
		return err
	}
	return nil
}

package secret

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// File returns a Store that keeps a JSON map of account to secret in path.
// The file is written with mode 0600 and its directory created with 0700.
// Every call refuses a file or directory that group or others can access.
// Windows always has Credential Manager, so there every call fails.
func File(path string) Store { return &fileStore{path: path} }

type fileStore struct {
	mu   sync.Mutex
	path string
}

func (f *fileStore) Get(ctx context.Context, account string) (Secret, error) {
	if err := checkAccount(account); err != nil {
		return Secret{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return Secret{}, err
	}
	s, ok := m[account]
	if !ok {
		return Secret{}, fmt.Errorf("%w: %s", ErrNotFound, account)
	}
	return s, nil
}

func (f *fileStore) Set(ctx context.Context, account string, s Secret) error {
	if err := checkAccount(account); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.supported(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return fmt.Errorf("secret: create directory: %w", err)
	}
	m, err := f.load()
	if err != nil {
		return err
	}
	m[account] = s
	return f.save(m)
}

func (f *fileStore) Delete(ctx context.Context, account string) error {
	if err := checkAccount(account); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	if _, ok := m[account]; !ok {
		return nil
	}
	delete(m, account)
	return f.save(m)
}

func (f *fileStore) supported() error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("secret: %w: use Credential Manager on Windows", ErrUnsupported)
	}
	return nil
}

// load reads the map after checking permissions. A missing file is empty.
func (f *fileStore) load() (map[string]Secret, error) {
	if err := f.supported(); err != nil {
		return nil, err
	}
	if err := checkMode(filepath.Dir(f.path), true); err != nil {
		return nil, err
	}
	if err := checkMode(f.path, false); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]Secret{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("secret: read %s: %w", f.path, err)
	}
	m := map[string]Secret{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("secret: %s is not valid: %w", f.path, err)
	}
	return m, nil
}

// checkMode refuses a path that group or others can read, write or enter.
// A path that does not exist passes.
func checkMode(path string, dir bool) error {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("secret: %w", err)
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		want := "0600"
		if dir {
			want = "0700"
		}
		return fmt.Errorf("secret: %s has mode %04o and others can access it; run chmod %s %s", path, mode, want, path)
	}
	return nil
}

// save writes the map to a temporary file beside the target and renames it
// into place, so a crash never leaves half a file.
func (f *fileStore) save(m map[string]Secret) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("secret: encode: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.path), ".credentials-*")
	if err != nil {
		return fmt.Errorf("secret: write %s: %w", f.path, err)
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("secret: write %s: %w", f.path, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("secret: write %s: %w", f.path, err)
	}
	if err := os.Rename(name, f.path); err != nil {
		os.Remove(name)
		return fmt.Errorf("secret: write %s: %w", f.path, err)
	}
	return nil
}

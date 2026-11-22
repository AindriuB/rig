package config

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AindriuB/rig/internal/proc"
)

// gitTimeout bounds the one git call a local write makes.
const gitTimeout = 30 * time.Second

// ignoreLine is what a local write appends to the main checkout's .gitignore.
const ignoreLine = "/.rig/"

// Set writes key = value in scope, which is ScopeLocal, ScopeRepo or
// ScopeGlobal. It refuses credential-like keys, unknown keys and the
// global-only keys, and checks the value first. The write is atomic.
func (l *Layers) Set(scope, key, value string) error {
	if err := checkSettable(key); err != nil {
		return err
	}
	if err := checkValue(key, value); err != nil {
		return err
	}
	if key == KeyTrackerProject {
		if err := checkProject(l.str(KeyTrackerKind), value); err != nil {
			return err
		}
	}
	return l.write(scope, key, value)
}

// Unset removes key from scope. Removing a key that is not set is not an
// error and writes nothing.
func (l *Layers) Unset(scope, key string) error {
	if err := checkSettable(key); err != nil {
		return err
	}
	return l.write(scope, key, nil)
}

// write sets key to value in scope's file, or removes it when value is nil.
func (l *Layers) write(scope, key string, value any) error {
	var path string
	var section func(raw map[string]any) map[string]any
	switch scope {
	case ScopeGlobal:
		path = l.globalPath
		section = func(raw map[string]any) map[string]any { return raw }
	case ScopeRepo:
		if l.repoKey == "" {
			if l.remoteErr != nil {
				return fmt.Errorf("no repo scope: %w", l.remoteErr)
			}
			return errors.New("no repo scope: the repository has no origin remote")
		}
		path = l.globalPath
		section = func(raw map[string]any) map[string]any {
			repos, ok := raw["repos"].(map[string]any)
			if !ok {
				repos = map[string]any{}
				raw["repos"] = repos
			}
			s, ok := repos[l.repoKey].(map[string]any)
			if !ok {
				s = map[string]any{}
				repos[l.repoKey] = s
			}
			return s
		}
	case ScopeLocal:
		if l.root == "" {
			return errors.New("no local scope outside a repository")
		}
		path = l.localPath
		section = func(raw map[string]any) map[string]any { return raw }
	default:
		return fmt.Errorf("cannot write scope %q: use local, repo or global", scope)
	}

	raw, err := readRaw(path)
	if err != nil {
		return err
	}
	changed := setNested(section(raw), splitKey(key), value)
	if scope == ScopeRepo {
		pruneRepos(raw, l.repoKey)
	}
	if !changed {
		return nil
	}
	if scope == ScopeLocal {
		if err := l.makeLocalDir(); err != nil {
			return err
		}
	} else if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config directory: %w", err)
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(path, append(data, '\n')); err != nil {
		return err
	}
	layer := l.layer(scope)
	if value == nil {
		delete(layer, key)
	} else {
		layer[key] = value
	}
	return nil
}

// setNested sets or, for a nil value, removes parts in m, pruning objects
// left empty. It reports whether m changed.
func setNested(m map[string]any, parts []string, value any) bool {
	if len(parts) == 1 {
		old, had := m[parts[0]]
		if value == nil {
			delete(m, parts[0])
			return had
		}
		m[parts[0]] = value
		s, ok := old.(string)
		return !had || !ok || s != value
	}
	sub, ok := m[parts[0]].(map[string]any)
	if !ok {
		if value == nil {
			return false
		}
		sub = map[string]any{}
		m[parts[0]] = sub
	}
	changed := setNested(sub, parts[1:], value)
	if len(sub) == 0 {
		delete(m, parts[0])
	}
	return changed
}

// pruneRepos drops this repository's section, and repos itself, when empty.
func pruneRepos(raw map[string]any, key string) {
	repos, ok := raw["repos"].(map[string]any)
	if !ok {
		return
	}
	if s, ok := repos[key].(map[string]any); ok && len(s) == 0 {
		delete(repos, key)
	}
	if len(repos) == 0 {
		delete(raw, "repos")
	}
}

// writeAtomic writes data to a temporary file in path's directory and
// renames it into place, so a crash never leaves half a file.
func writeAtomic(path string, data []byte) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	if _, err = f.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err = os.Chmod(f.Name(), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// makeLocalDir creates <root>/.rig. Only when it creates it, and nothing
// under .rig is tracked, it appends /.rig/ to <root>/.gitignore unless a
// line there already ignores it.
func (l *Layers) makeLocalDir() error {
	dir := filepath.Join(l.root, Dir)
	if _, err := os.Stat(dir); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	res, err := proc.Run(ctx, proc.Options{Dir: l.root}, "git", "ls-files", "-z", "--", Dir)
	if err != nil {
		return fmt.Errorf("git ls-files %s: %w", Dir, err)
	}
	tracked := len(bytes.TrimSpace(res.Stdout)) > 0
	if err := os.Mkdir(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if tracked {
		return nil
	}
	return addIgnoreLine(filepath.Join(l.root, ".gitignore"))
}

// addIgnoreLine appends /.rig/ to the .gitignore at path, creating it, unless
// a line already ignores .rig.
func addIgnoreLine(path string) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		switch strings.TrimSpace(sc.Text()) {
		case "/.rig/", "/.rig", ".rig/", ".rig", "/.rig/*", ".rig/*", "**/.rig", "**/.rig/":
			return nil
		}
	}
	add := ignoreLine + "\n"
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		add = "\n" + add
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(add); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

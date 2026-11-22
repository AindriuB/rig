package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Scopes a value can come from, highest precedence first.
const (
	ScopeLocal   = "local"    // <root>/.rig/config.json
	ScopeRepo    = "repo"     // the global file's repos."<repo key>" section
	ScopeRigJSON = "rig.json" // <root>/.claude/rig.json
	ScopeGlobal  = "global"   // the global file's top level
	ScopeDefault = "default"  // built in
)

// Dir is rig's config directory, below the home directory for the global
// file and below the main checkout's root for the local one; ConfigFile is
// the file in it.
const (
	Dir        = ".rig"
	ConfigFile = "config.json"
)

// Options say where the layers live.
type Options struct {
	// Home returns the user's home directory. Nil: os.UserHomeDir.
	Home func() (string, error)
	// Root is the main checkout's root; "" outside a repository.
	Root string
	// Remote is the origin remote's URL; "" when there is none.
	Remote string
}

// Value is one key's value and the scope it came from. Value is a string,
// or a []string for push.prefixes.
type Value struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
	Scope string `json:"scope"`
}

// Tracker is the tracker in effect.
type Tracker struct {
	Kind    string
	URL     string
	Project string
}

// Layers is every config scope of one checkout, read once by Open.
type Layers struct {
	root       string
	repoKey    string // "" when the remote has none
	remoteErr  error  // why repoKey is "", if a remote was given
	globalPath string
	localPath  string

	local, repo, rigJSON, global map[string]any
	warnings                     []string
}

// Open reads ~/.rig/config.json (its top level and the repos section for
// this repository), <Root>/.rig/config.json and <Root>/.claude/rig.json.
// Missing files are empty layers. A bad key or value in any file is an
// error naming the file.
func Open(o Options) (*Layers, error) {
	homeFn := o.Home
	if homeFn == nil {
		homeFn = os.UserHomeDir
	}
	home, err := homeFn()
	if err != nil {
		return nil, fmt.Errorf("home directory: %w", err)
	}
	l := &Layers{
		root:       o.Root,
		globalPath: filepath.Join(home, Dir, ConfigFile),
		local:      map[string]any{},
		repo:       map[string]any{},
		rigJSON:    map[string]any{},
		global:     map[string]any{},
	}
	if o.Remote != "" {
		l.repoKey, l.remoteErr = RepoKey(o.Remote)
	}

	raw, err := readRaw(l.globalPath)
	if err != nil {
		return nil, err
	}
	repos, err := reposOf(l.globalPath, raw)
	if err != nil {
		return nil, err
	}
	delete(raw, "repos")
	if l.global, _, err = flattenFile(l.globalPath, raw, nil); err != nil {
		return nil, err
	}
	if section, ok := repos[l.repoKey]; ok && l.repoKey != "" {
		where := fmt.Sprintf("%s repos[%q]", l.globalPath, l.repoKey)
		if l.repo, _, err = flattenFile(where, section, nil); err != nil {
			return nil, err
		}
	}

	if o.Root != "" {
		l.localPath = filepath.Join(o.Root, Dir, ConfigFile)
		raw, err := readRaw(l.localPath)
		if err != nil {
			return nil, err
		}
		var ignored []string
		if l.local, ignored, err = flattenFile(l.localPath, raw, globalOnly); err != nil {
			return nil, err
		}
		for _, k := range ignored {
			l.warnings = append(l.warnings, fmt.Sprintf(
				"%s: %s is read only from the global file; ignored here", l.localPath, k))
		}
		c, err := read(o.Root)
		if err != nil {
			return nil, err
		}
		if c.Forge != "" {
			l.rigJSON[KeyForgeKind] = c.Forge
		}
		if c.Host != "" {
			if err := checkValue(KeyForgeHost, c.Host); err != nil {
				return nil, fmt.Errorf("%s: %w", filepath.Join(o.Root, File), err)
			}
			l.rigJSON[KeyForgeHost] = c.Host
		}
		if len(c.PushPrefixes) > 0 {
			l.rigJSON[KeyPushPrefixes] = c.PushPrefixes
		}
	}
	return l, nil
}

// RepoKey is this repository's key in the global file, "" if it has none.
func (l *Layers) RepoKey() string { return l.repoKey }

// order is where key is looked up, highest precedence first.
func order(key string) []string {
	switch key {
	case KeyPushPrefixes:
		// rig.json's pushPrefixes wins when present; a project .rig file
		// never sets it.
		return []string{ScopeRigJSON, ScopeRepo, ScopeGlobal, ScopeDefault}
	case KeyWorkflow:
		return []string{ScopeRepo, ScopeGlobal, ScopeDefault}
	}
	return []string{ScopeLocal, ScopeRepo, ScopeRigJSON, ScopeGlobal, ScopeDefault}
}

func (l *Layers) layer(scope string) map[string]any {
	switch scope {
	case ScopeLocal:
		return l.local
	case ScopeRepo:
		return l.repo
	case ScopeRigJSON:
		return l.rigJSON
	case ScopeGlobal:
		return l.global
	}
	return defaults
}

var defaults = map[string]any{
	KeyTrackerKind:  TrackerLocal,
	KeyWorkflow:     DefaultWorkflow,
	KeyPushPrefixes: DefaultPushPrefixes,
}

// Get returns the value of key in effect and its scope. It is false for a
// key that is not set anywhere and has no default, or that is not a key.
func (l *Layers) Get(key string) (Value, bool) {
	if !slices.Contains(keys, key) {
		return Value{}, false
	}
	for _, scope := range order(key) {
		if v, ok := l.layer(scope)[key]; ok {
			return Value{Key: key, Value: clone(v), Scope: scope}, true
		}
	}
	return Value{}, false
}

// GetIn returns key's value in one scope only.
func (l *Layers) GetIn(scope, key string) (Value, bool) {
	if !slices.Contains(keys, key) || !slices.Contains(order(key), scope) {
		return Value{}, false
	}
	v, ok := l.layer(scope)[key]
	if !ok {
		return Value{}, false
	}
	return Value{Key: key, Value: clone(v), Scope: scope}, true
}

// List returns every key in effect, sorted by key.
func (l *Layers) List() []Value {
	var out []Value
	for _, k := range keys {
		if v, ok := l.Get(k); ok {
			out = append(out, v)
		}
	}
	return out
}

// Warnings returns what is wrong but not fatal: keys a project file may not
// set, and a tracker that a command needing it would refuse.
func (l *Layers) Warnings() []string {
	out := slices.Clone(l.warnings)
	if _, err := l.Tracker(); err != nil {
		out = append(out, err.Error())
	}
	return out
}

func (l *Layers) str(key string) string {
	v, _ := l.Get(key)
	s, _ := v.Value.(string)
	return s
}

// Tracker returns the tracker in effect, or an error naming the key it is
// missing: jira needs tracker.url and tracker.project.
func (l *Layers) Tracker() (Tracker, error) {
	t := Tracker{Kind: l.str(KeyTrackerKind), URL: l.str(KeyTrackerURL), Project: l.str(KeyTrackerProject)}
	if t.Kind == TrackerJira {
		if t.URL == "" {
			return t, fmt.Errorf("tracker.kind is %q but %s is not set", t.Kind, KeyTrackerURL)
		}
		if t.Project == "" {
			return t, fmt.Errorf("tracker.kind is %q but %s is not set", t.Kind, KeyTrackerProject)
		}
	}
	if t.Project != "" {
		if err := checkProject(t.Kind, t.Project); err != nil {
			return t, err
		}
	}
	return t, nil
}

func clone(v any) any {
	if s, ok := v.([]string); ok {
		return slices.Clone(s)
	}
	return v
}

// readRaw reads a JSON object file. A missing file is an empty object.
func readRaw(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if raw == nil {
		raw = map[string]any{}
	}
	return raw, nil
}

// reposOf returns the global file's repos sections.
func reposOf(path string, raw map[string]any) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	r, ok := raw["repos"]
	if !ok {
		return out, nil
	}
	m, ok := r.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: repos must be an object", path)
	}
	for k, v := range m {
		section, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: repos[%q] must be an object", path, k)
		}
		out[k] = section
	}
	return out, nil
}

// flattenFile turns nested objects into dotted keys and checks each key and
// value. Keys in ignore are dropped unchecked and returned, sorted.
func flattenFile(where string, raw map[string]any, ignore []string) (map[string]any, []string, error) {
	out := map[string]any{}
	var ignored []string
	var walk func(prefix string, m map[string]any) error
	walk = func(prefix string, m map[string]any) error {
		for k, v := range m {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			if slices.Contains(ignore, key) {
				ignored = append(ignored, key)
				continue
			}
			if sub, ok := v.(map[string]any); ok {
				if err := walk(key, sub); err != nil {
					return err
				}
				continue
			}
			if err := checkKey(key); err != nil {
				return fmt.Errorf("%s: %w", where, err)
			}
			val, err := parseValue(key, v)
			if err != nil {
				return fmt.Errorf("%s: %w", where, err)
			}
			out[key] = val
		}
		return nil
	}
	if err := walk("", raw); err != nil {
		return nil, nil, err
	}
	slices.Sort(ignored)
	return out, ignored, nil
}

// splitKey splits a dotted key into its nested object path.
func splitKey(key string) []string { return strings.Split(key, ".") }

// Package config reads rig's settings. Load reads a repository's
// `.claude/rig.json`, where every key is optional and a missing file is the
// defaults. Open reads every layer: the global ~/.rig/config.json (its top
// level and a section per repository), the checkout's .rig/config.json and
// rig.json, and Set and Unset write them.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

// File is the config file's path below the repository root.
const File = ".claude/rig.json"

// Config is the content of rig.json.
type Config struct {
	// Forge is "github" or "gitlab". Empty: detect from the origin host.
	Forge string `json:"forge,omitempty"`
	// Host is the forge's host, for a self-hosted instance. Empty: the
	// origin remote's host.
	Host string `json:"host,omitempty"`
	// PushPrefixes are the branch prefixes `rig push` may push: "task"
	// allows task/<anything>.
	PushPrefixes []string `json:"pushPrefixes,omitempty"`
}

// DefaultPushPrefixes are the loop's own branch prefixes.
var DefaultPushPrefixes = []string{"task", "record"}

var prefixRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Default is the config of a repository with no rig.json.
func Default() Config {
	return Config{PushPrefixes: append([]string(nil), DefaultPushPrefixes...)}
}

// Load reads <root>/.claude/rig.json. An unknown key is an error, so a typo
// is not read as "use the default".
func Load(root string) (Config, error) {
	c, err := read(root)
	if err != nil {
		return Config{}, err
	}
	if len(c.PushPrefixes) == 0 {
		c.PushPrefixes = append([]string(nil), DefaultPushPrefixes...)
	}
	return c, nil
}

// read reads and checks <root>/.claude/rig.json without filling defaults, so
// a caller can tell an unset key from a default. A missing file is the zero
// Config.
func read(root string) (Config, error) {
	path := filepath.Join(root, File)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	switch c.Forge {
	case "", "github", "gitlab":
	default:
		return Config{}, fmt.Errorf("%s: forge must be \"github\" or \"gitlab\", not %q", path, c.Forge)
	}
	for _, p := range c.PushPrefixes {
		if !prefixRE.MatchString(p) {
			return Config{}, fmt.Errorf("%s: pushPrefixes entry %q is not a plain branch prefix", path, p)
		}
	}
	return c, nil
}

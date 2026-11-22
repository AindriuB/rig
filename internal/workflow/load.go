package workflow

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed presets/*.json
var presets embed.FS

// Source says where a workflow came from.
type Source struct {
	// Preset is the built-in preset's name, or "" for a file.
	Preset string `json:"preset,omitempty"`
	// Path is the file's path, or "" for a preset.
	Path string `json:"path,omitempty"`
}

func (s Source) String() string {
	if s.Preset != "" {
		return "preset " + s.Preset
	}
	return s.Path
}

// Presets lists the built-in presets' names, sorted.
func Presets() []string {
	entries, _ := presets.ReadDir("presets")
	var out []string
	for _, e := range entries {
		out = append(out, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(out)
	return out
}

// Load reads a workflow: nameOrPath is a built-in preset's name, such as
// "basic", or an absolute path to a workflow file. When the workflow parses
// but breaks the format's rules, Load returns it together with an
// *InvalidError listing the problems. A workflow returned with an
// *InvalidError is for reporting only: Check fails every gate of it, and no
// caller should move a task under it.
func Load(nameOrPath string) (*Workflow, Source, error) {
	var (
		data []byte
		src  Source
		err  error
	)
	switch {
	case nameOrPath == "":
		return nil, Source{}, fmt.Errorf("load workflow: no preset name or path given")
	case filepath.IsAbs(nameOrPath):
		src = Source{Path: nameOrPath}
		data, err = os.ReadFile(nameOrPath)
		if err != nil {
			return nil, src, fmt.Errorf("load workflow: %w", err)
		}
	case strings.ContainsAny(nameOrPath, `/\.`):
		return nil, Source{}, fmt.Errorf("load workflow: %q is neither a preset nor an absolute path", nameOrPath)
	default:
		src = Source{Preset: nameOrPath}
		data, err = presets.ReadFile("presets/" + nameOrPath + ".json")
		if err != nil {
			return nil, src, fmt.Errorf("load workflow: no preset %q; presets are %s", nameOrPath, strings.Join(Presets(), ", "))
		}
	}
	w, err := Parse(data)
	if err != nil {
		return nil, src, fmt.Errorf("load workflow %s: %w", src, err)
	}
	if ps := w.Validate(); len(ps) > 0 {
		return w, src, &InvalidError{Name: src.String(), Problems: ps}
	}
	return w, src, nil
}

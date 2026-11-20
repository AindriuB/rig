package main

import (
	"fmt"
	"strings"
)

// flagSet is a strict flag parser: a flag it was not told about is an error,
// never a positional and never ignored.
type flagSet struct {
	strs  map[string]*string
	bools map[string]*bool
}

func newFlags() *flagSet {
	return &flagSet{strs: map[string]*string{}, bools: map[string]*bool{}}
}

func (f *flagSet) str(name string) *string {
	v := new(string)
	f.strs[name] = v
	return v
}

func (f *flagSet) boolean(name string) *bool {
	v := new(bool)
	f.bools[name] = v
	return v
}

// parse accepts `--name value`, `--name=value` and `--flag`, and returns the
// positionals. Anything else that starts with "-" is an error.
func (f *flagSet) parse(args []string) ([]string, error) {
	var pos []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			pos = append(pos, arg)
			continue
		}
		name, val, hasVal := strings.Cut(arg, "=")
		if s, ok := f.strs[name]; ok {
			if !hasVal {
				if i+1 >= len(args) {
					return nil, fmt.Errorf("%s needs a value", name)
				}
				i++
				val = args[i]
			}
			*s = val
			continue
		}
		if b, ok := f.bools[name]; ok && !hasVal {
			*b = true
			continue
		}
		return nil, fmt.Errorf("unknown option %q", arg)
	}
	return pos, nil
}

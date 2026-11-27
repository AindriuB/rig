package main

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// flagSet is a strict flag parser: a flag it was not told about is an error,
// never a positional and never ignored.
type flagSet struct {
	strs  map[string]*string
	bools map[string]*bool
	given []string // string flags seen by parse, in order
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

// knownOptions is every option any rig command defines. An unknown option is
// named in an error only when it is one of these (a real flag used on a
// command that lacks it): any other argument may be a secret typed in the
// wrong place, typos included. A test fails when a flag a command registers
// is missing here, whether it is registered with str/boolean or named in a
// []string literal handed to splitRepeated or takeRepeated.
var knownOptions = map[string]bool{
	"--actor": true, "--all": true, "--base": true, "--blocking": true, "--body": true,
	"--body-file": true, "--budget": true, "--check": true, "--children": true, "--comment": true,
	"--draft": true, "--dry-run": true, "--field": true, "--findings": true, "--format": true,
	"--from": true, "--global": true, "--head": true, "--help": true, "--history": true,
	"--include": true, "--once": true, "--json": true, "--key": true, "--kind": true, "--label": true, "--limit": true,
	"--local": true, "--markdown": true, "--mine": true, "--notes": true, "--parent": true,
	"--pat": true, "--path": true, "--pr": true, "--query": true, "--reason": true,
	"--repo": true, "--role": true, "--roots": true, "--run": true, "--severity": true,
	"--sha": true, "--show-origin": true, "--slug": true, "--state": true, "--status": true,
	"--store": true, "--summary": true, "--task": true, "--task-file": true, "--text": true,
	"--title": true, "--to": true, "--token": true, "--tree": true, "--url": true, "--user": true,
	"--version": true, "--who": true, "--worktree": true, "-h": true,
}

// shownOption is how an unknown option appears in an error. The value after
// "=" is never shown, and neither is a name that is not a known rig option:
// the argument may be a secret typed in the wrong place.
func shownOption(name string) string {
	if knownOptions[name] {
		return fmt.Sprintf("%q", name)
	}
	return "(not shown)"
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
			f.given = append(f.given, name)
			continue
		}
		if b, ok := f.bools[name]; ok && !hasVal {
			*b = true
			continue
		}
		return nil, fmt.Errorf("unknown option %s", shownOption(name))
	}
	return pos, nil
}

// actorFlags registers --actor and --role (an alias) on fs, for a command that
// writes the store. Call the returned func after fs.parse succeeds: it checks
// the values and, when one was given, sets a.actorFlag so the store the command
// opens stamps its events with it. Empty, control-character or conflicting
// values are an error and leave a.actorFlag alone.
func (a *app) actorFlags(fs *flagSet) func() error {
	actor, role := fs.str("--actor"), fs.str("--role")
	seen := func(name string) bool {
		for _, n := range fs.given {
			if n == name {
				return true
			}
		}
		return false
	}
	return func() error {
		var vals []string
		for _, f := range []struct {
			name string
			v    *string
		}{{"--actor", actor}, {"--role", role}} {
			if !seen(f.name) {
				continue
			}
			v := strings.TrimSpace(*f.v)
			if v == "" {
				return fmt.Errorf("%s must not be empty", f.name)
			}
			if strings.IndexFunc(v, func(r rune) bool {
				return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
			}) >= 0 {
				return fmt.Errorf("%s must not contain control, format or line-separator characters", f.name)
			}
			vals = append(vals, v)
		}
		if len(vals) == 2 && vals[0] != vals[1] {
			return errors.New("--actor and --role are the same flag and disagree")
		}
		if len(vals) > 0 {
			a.actorFlag = vals[0]
		}
		return nil
	}
}

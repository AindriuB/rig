package main

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/AindriuB/rig/internal/config"
	"github.com/AindriuB/rig/internal/git"
)

// configKeys mirrors internal/config's allow-list, so that `get` can tell an
// unknown key (exit 2) from one that is merely not set (exit 1).
var configKeys = []string{
	config.KeyForgeHost, config.KeyForgeKind, config.KeyPushPrefixes,
	config.KeyTrackerKind, config.KeyTrackerProject, config.KeyTrackerURL, config.KeyWorkflow,
}

// config implements `rig config get|set|unset|list`.
func (a *app) config(ctx context.Context, args []string) int {
	if len(args) == 0 {
		return a.fail("config", "usage: rig config get|set|unset|list")
	}
	sub, rest := args[0], args[1:]
	nargs, ok := map[string]int{"get": 1, "set": 2, "unset": 1, "list": 0}[sub]
	if !ok {
		return a.fail("config", "unknown subcommand %q", sub)
	}
	cmd := "config " + sub

	f := newFlags()
	global, repo, local := f.boolean("--global"), f.boolean("--repo"), f.boolean("--local")
	var asJSON, origin *bool
	if sub == "get" || sub == "list" {
		asJSON = f.boolean("--json")
	}
	if sub == "list" {
		origin = f.boolean("--show-origin")
	}
	pos, err := f.parse(rest)
	if err != nil {
		return a.fail(cmd, "%v", err)
	}
	var scopes []string
	for _, s := range []struct {
		set  bool
		name string
	}{{*global, config.ScopeGlobal}, {*repo, config.ScopeRepo}, {*local, config.ScopeLocal}} {
		if s.set {
			scopes = append(scopes, s.name)
		}
	}
	if len(scopes) > 1 {
		return a.fail(cmd, "give at most one of --global, --repo, --local")
	}
	scope := ""
	if len(scopes) == 1 {
		scope = scopes[0]
	}
	if len(pos) != nargs {
		return a.fail(cmd, "expected %d argument(s), got %d", nargs, len(pos))
	}
	if (sub == "set" || sub == "unset") && scope == "" {
		return a.fail(cmd, "a scope is required: --global, --repo or --local")
	}

	layers, err := a.openConfig(ctx)
	if err != nil {
		return a.fail(cmd, "%v", err)
	}
	switch sub {
	case "get":
		key := pos[0]
		if !slices.Contains(configKeys, key) {
			return a.fail(cmd, "unknown config key %q", key)
		}
		var v config.Value
		var found bool
		if scope == "" {
			v, found = layers.Get(key)
		} else {
			v, found = layers.GetIn(scope, key)
		}
		if !found {
			return 1
		}
		if *asJSON {
			return a.printJSON(v)
		}
		fmt.Fprintln(a.stdout, valueText(v.Value))
		return 0
	case "set":
		if err := layers.Set(scope, pos[0], pos[1]); err != nil {
			return a.fail(cmd, "%v", err)
		}
		return 0
	case "unset":
		if err := layers.Unset(scope, pos[0]); err != nil {
			return a.fail(cmd, "%v", err)
		}
		return 0
	}

	// list
	for _, w := range layers.Warnings() {
		fmt.Fprintf(a.stderr, "rig config: warning: %s\n", w)
	}
	vals := []config.Value{}
	if scope == "" {
		vals = layers.List()
	} else {
		for _, k := range configKeys {
			if v, found := layers.GetIn(scope, k); found {
				vals = append(vals, v)
			}
		}
	}
	if *asJSON {
		return a.printJSON(vals)
	}
	for _, v := range vals {
		if *origin {
			fmt.Fprintf(a.stdout, "%s=%s\t(%s)\n", v.Key, valueText(v.Value), v.Scope)
		} else {
			fmt.Fprintf(a.stdout, "%s=%s\n", v.Key, valueText(v.Value))
		}
	}
	return 0
}

// valueText prints a config value; a list is comma-separated.
func valueText(v any) string {
	if s, ok := v.([]string); ok {
		return strings.Join(s, ",")
	}
	return fmt.Sprint(v)
}

// openConfig opens the layered config for the current directory. Outside a
// repository there is no Root, and a repository with no origin has no Remote.
func (a *app) openConfig(ctx context.Context) (*config.Layers, error) {
	o := config.Options{Home: a.homeDir}
	r := git.Repo{Dir: a.cwd}
	if wts, err := r.Worktrees(ctx); err == nil && len(wts) > 0 {
		o.Root = wts[0].Path
		if url, err := r.RemoteURL(ctx, "origin"); err == nil {
			o.Remote = url
		}
	}
	return config.Open(o)
}

// homeDir is the user's home directory, from the app's environment first.
func (a *app) homeDir() (string, error) {
	for _, k := range []string{"HOME", "USERPROFILE"} {
		if v := a.getenv(k); v != "" {
			return v, nil
		}
	}
	return os.UserHomeDir()
}

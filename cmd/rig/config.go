package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"

	"github.com/AindriuB/rig/internal/config"
	"github.com/AindriuB/rig/internal/git"
)

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

	layers, root, remote, err := a.openConfig(ctx)
	if err != nil {
		return a.fail(cmd, "%v", err)
	}
	if sub == "get" || sub == "list" {
		if scope == config.ScopeLocal && root == "" {
			return a.fail(cmd, "no local scope: not in a git repository")
		}
		if scope == config.ScopeRepo {
			if root == "" {
				return a.fail(cmd, "no repo scope: not in a git repository")
			}
			if _, err := config.RepoKey(remote); err != nil || remote == "" {
				return a.fail(cmd, "no repo scope: the repository has no origin remote with a repo key")
			}
		}
	}
	switch sub {
	case "get":
		key := pos[0]
		if !slices.Contains(config.Keys(), key) {
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
		for _, k := range config.Keys() {
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

// openConfig opens the layered config for the current directory and returns
// the Root and Remote it used. Outside a repository there is no Root, and a
// repository with no origin has no Remote. In a bare repository there is no
// main checkout, so the current worktree's top level is the Root.
func (a *app) openConfig(ctx context.Context) (layers *config.Layers, root, remote string, err error) {
	o := config.Options{Home: a.homeDir}
	r := git.Repo{Dir: a.cwd}
	if wts, err := r.Worktrees(ctx); err == nil && len(wts) > 0 {
		if wts[0].Bare {
			if top, err := r.TopLevel(ctx); err == nil {
				o.Root = top
			}
		} else {
			o.Root = wts[0].Path
		}
		if url, err := r.RemoteURL(ctx, "origin"); err == nil {
			o.Remote = url
		}
	}
	layers, err = config.Open(o)
	return layers, o.Root, o.Remote, err
}

// homeDir is the user's home directory, from the app's environment first.
func (a *app) homeDir() (string, error) {
	names := []string{"HOME"}
	if runtime.GOOS == "windows" {
		names = []string{"USERPROFILE", "HOME"}
	}
	for _, k := range names {
		if v := a.getenv(k); v != "" {
			return v, nil
		}
	}
	return os.UserHomeDir()
}

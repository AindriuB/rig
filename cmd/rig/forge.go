package main

import (
	"context"
	"fmt"

	"github.com/AindriuB/rig/internal/config"
	"github.com/AindriuB/rig/internal/forge"
	"github.com/AindriuB/rig/internal/forge/github"
	"github.com/AindriuB/rig/internal/forge/gitlab"
	"github.com/AindriuB/rig/internal/git"
)

// forgeKind decides which forge hosts the repository at dir: the layered
// forge.kind if set, else a guess from the host of the origin remote (or the
// layered forge.host). Anything it cannot place is GitHub, which is what the
// scripts rig replaces assumed: outside a repository, with no origin, or with
// a host that names no forge, such as an SSH config alias. home nil means
// os.UserHomeDir.
func forgeKind(ctx context.Context, dir string, home func() (string, error)) (forge.Kind, error) {
	repo := git.Repo{Dir: dir}
	top, err := repo.TopLevel(ctx)
	if err != nil {
		return forge.GitHub, nil
	}
	layers, err := openLayersAt(ctx, top, home)
	if err != nil {
		return "", err
	}
	kindV, kindOK := forgeValue(layers, config.KeyForgeKind)
	hostV, hostOK := forgeValue(layers, config.KeyForgeHost)
	// A kind from a less specific scope never overrides a host from a more
	// specific one (a global kind does not override a repo's host).
	if kindOK && (!hostOK || forgeRank(kindV.Scope) <= forgeRank(hostV.Scope)) {
		return forge.Kind(kindV.Value.(string)), nil
	}
	host := ""
	if hostOK {
		host = hostV.Value.(string)
	}
	if host == "" {
		url, err := repo.RemoteURL(ctx, "origin")
		if err != nil {
			return forge.GitHub, nil
		}
		host = git.HostOf(url)
	}
	if host == "" {
		return forge.GitHub, nil
	}
	if kind, ok := forge.KindForHost(host); ok {
		return kind, nil
	}
	return forge.GitHub, nil
}

// forgeScopes lists the scopes a forge value can come from, most specific
// first. .claude/rig.json is the most specific, as it was before the layered
// config existed.
var forgeScopes = []string{config.ScopeRigJSON, config.ScopeLocal, config.ScopeRepo, config.ScopeGlobal}

func forgeRank(scope string) int {
	for i, s := range forgeScopes {
		if s == scope {
			return i
		}
	}
	return len(forgeScopes)
}

// forgeValue returns the most specific non-empty string value of key, by
// forgeScopes order.
func forgeValue(l *config.Layers, key string) (config.Value, bool) {
	for _, scope := range forgeScopes {
		if v, ok := l.GetIn(scope, key); ok {
			if s, _ := v.Value.(string); s != "" {
				return v, true
			}
		}
	}
	return config.Value{}, false
}

// openLayersAt opens the layered config for the repository whose working tree
// is at top: the main checkout is the project root, and origin's URL picks the
// repos[] section of the global file. home nil means os.UserHomeDir.
func openLayersAt(ctx context.Context, top string, home func() (string, error)) (*config.Layers, error) {
	o := config.Options{Home: home, Root: top}
	r := git.Repo{Dir: top}
	if wts, err := r.Worktrees(ctx); err == nil && len(wts) > 0 && !wts[0].Bare {
		o.Root = wts[0].Path
	}
	if url, err := r.RemoteURL(ctx, "origin"); err == nil {
		o.Remote = url
	}
	return config.Open(o)
}

// selectForge picks the adapter for dir, reading the user's real home.
func selectForge(ctx context.Context, dir string) (forge.Forge, error) {
	return selectForgeWith(nil)(ctx, dir)
}

// selectForgeWith is selectForge reading the global config from home (nil
// means os.UserHomeDir), so a caller can use the same home as its app.
func selectForgeWith(home func() (string, error)) func(context.Context, string) (forge.Forge, error) {
	return func(ctx context.Context, dir string) (forge.Forge, error) {
		kind, err := forgeKind(ctx, dir, home)
		if err != nil {
			return nil, err
		}
		switch kind {
		case forge.GitHub:
			return github.New(dir), nil
		case forge.GitLab:
			return gitlab.New(dir), nil
		}
		return nil, fmt.Errorf("no adapter for forge %q in this build", kind)
	}
}

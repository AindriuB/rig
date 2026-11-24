package main

import (
	"context"
	"fmt"
	"os"

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
	// With no home directory the global layer is empty: forge selection
	// goes on with the repository's own files and origin. (rig push does not:
	// its policy may live in the global file, so it fails closed.)
	if home == nil {
		home = os.UserHomeDir
	}
	layers, err := openLayersAt(ctx, top, func() (string, error) {
		h, herr := home()
		if herr != nil {
			return "", nil
		}
		return h, nil
	})
	if err != nil {
		return "", err
	}
	// Both keys resolve in the order rig config get reports. A kind from a
	// less specific scope than the host never overrides that host (a global
	// kind does not override a repo's host).
	kindV, kindOK := forgeValue(layers, config.KeyForgeKind)
	hostV, hostOK := forgeValue(layers, config.KeyForgeHost)
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
	// A host that names no forge does not end the search: a less specific
	// kind still applies.
	if hostOK && kindOK {
		return forge.Kind(kindV.Value.(string)), nil
	}
	return forge.GitHub, nil
}

// forgeRank is scope's place in the lookup order of forge.kind and forge.host
// (the same order), most specific first.
func forgeRank(scope string) int {
	order := config.Order(config.KeyForgeKind)
	for i, s := range order {
		if s == scope {
			return i
		}
	}
	return len(order)
}

// forgeValue returns key as rig config get reports it: the first scope in
// lookup order that sets it. An empty string counts as unset.
func forgeValue(l *config.Layers, key string) (config.Value, bool) {
	v, ok := l.Get(key)
	if !ok {
		return config.Value{}, false
	}
	if s, _ := v.Value.(string); s == "" {
		return config.Value{}, false
	}
	return v, true
}

// openLayersAt opens the layered config for the repository whose working tree
// is at top: the main checkout is the project root, and origin's URL picks the
// repos[] section of the global file. A bare main checkout gives no project
// root. home nil means os.UserHomeDir.
func openLayersAt(ctx context.Context, top string, home func() (string, error)) (*config.Layers, error) {
	o := config.Options{Home: home, Root: top}
	r := git.Repo{Dir: top}
	if wts, err := r.Worktrees(ctx); err == nil && len(wts) > 0 {
		// A bare main checkout has no project files to trust: a linked
		// worktree's own copy must not supply policy, so no root.
		o.Root = wts[0].Path
		if wts[0].Bare {
			o.Root = ""
		}
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

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
	if v, ok := layers.Get(config.KeyForgeKind); ok {
		if s, _ := v.Value.(string); s != "" {
			return forge.Kind(s), nil
		}
	}
	host := ""
	if v, ok := layers.Get(config.KeyForgeHost); ok {
		host, _ = v.Value.(string)
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

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

// forgeKind decides which forge hosts the repository at dir: rig.json's
// "forge" if set, else a guess from the host of the origin remote (or
// rig.json's "host"). Anything it cannot place is GitHub, which is what the
// scripts rig replaces assumed: outside a repository, with no origin, or with
// a host that names no forge, such as an SSH config alias.
func forgeKind(ctx context.Context, dir string) (forge.Kind, error) {
	repo := git.Repo{Dir: dir}
	top, err := repo.TopLevel(ctx)
	if err != nil {
		return forge.GitHub, nil
	}
	cfg, err := config.Load(top)
	if err != nil {
		return "", err
	}
	if cfg.Forge != "" {
		return forge.Kind(cfg.Forge), nil
	}
	host := cfg.Host
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

func selectForge(ctx context.Context, dir string) (forge.Forge, error) {
	kind, err := forgeKind(ctx, dir)
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

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/AindriuB/rig/internal/git"
	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/store/gitref"
)

// openStore is app.store's default: the git-ref store of the repository that
// contains dir, stamping events with a.now and a.actor.
func (a *app) openStore(ctx context.Context, dir string) (store.Store, error) {
	repo := git.Repo{Dir: dir}
	if _, err := repo.CommonDir(ctx); err != nil {
		return nil, fmt.Errorf("open store: %s is not in a git repository: %w", dir, err)
	}
	return gitref.Open(repo, store.Options{Now: a.now, Actor: a.actor(ctx, dir)})
}

// actor is who events record: $RIG_ACTOR, else git's user.name for the
// repository at dir, else "unknown".
func (a *app) actor(ctx context.Context, dir string) string {
	if v := strings.TrimSpace(a.getenv("RIG_ACTOR")); v != "" {
		return v
	}
	name, err := git.Repo{Dir: dir}.UserName(ctx)
	if v := strings.TrimSpace(name); err == nil && v != "" {
		return v
	}
	return "unknown"
}

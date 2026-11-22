package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/AindriuB/rig/internal/git"
	"github.com/AindriuB/rig/internal/proc"
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
	res, err := proc.Run(ctx, proc.Options{Env: []string{"LC_ALL=C", "GIT_TERMINAL_PROMPT=0"}, Timeout: 30 * time.Second},
		"git", "-C", dir, "config", "user.name")
	if v := strings.TrimSpace(string(res.Stdout)); err == nil && v != "" {
		return v
	}
	return "unknown"
}

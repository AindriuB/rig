package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/AindriuB/rig/internal/config"
	"github.com/AindriuB/rig/internal/git"
)

// push implements `rig push <task-id|branch> [--worktree <path>]`.
//
// Exit 0: pushed. Exit 1: git ran and the push failed. Exit 2: refused, and
// git push was never run.
//
// The command line is not a git command line. It takes one task id or branch
// and one optional worktree; every other option is refused, so there is no
// spelling of force, tags, a refspec or another remote to let through. What
// survives the checks reaches git as an argument vector.
func (a *app) push(ctx context.Context, args []string) int {
	refuse := func(format string, v ...any) int {
		fmt.Fprintf(a.stderr, "rig push: refused: %s\n", fmt.Sprintf(format, v...))
		return 2
	}
	var target, wt string
	targets := 0
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--worktree":
			if i+1 >= len(args) {
				return refuse("--worktree needs a path")
			}
			i++
			wt = args[i]
		case strings.HasPrefix(arg, "--worktree="):
			wt = strings.TrimPrefix(arg, "--worktree=")
		case strings.HasPrefix(arg, "-"):
			return refuse("option %q is not allowed; rig push takes one task id or branch, and --worktree <path>", arg)
		default:
			target = arg
			targets++
		}
	}
	if targets != 1 || target == "" {
		return refuse("give exactly one task id or branch")
	}
	if wt == "" {
		wt = a.cwd
	}
	if fi, err := os.Stat(wt); err != nil || !fi.IsDir() {
		return refuse("no such directory: %s", wt)
	}
	repo := git.Repo{Dir: wt}
	top, err := repo.TopLevel(ctx)
	if err != nil {
		return refuse("not a git working tree: %s", wt)
	}
	cfg, err := config.Load(top)
	if err != nil {
		return refuse("%v", err)
	}

	branch := target
	if !strings.Contains(target, "/") {
		if branch, err = taskBranch(ctx, repo, target); err != nil {
			return refuse("%v", err)
		}
	}
	if err := checkBranch(branch, cfg.PushPrefixes); err != nil {
		return refuse("%v", err)
	}
	if def, _ := repo.DefaultBranch(ctx); def != "" && branch == def {
		return refuse("%s is the default branch", branch)
	}
	if ok, err := repo.BranchExists(ctx, branch); err != nil {
		return refuse("%v", err)
	} else if !ok {
		return refuse("no local branch %s", branch)
	}

	if err := repo.Push(ctx, "origin", branch); err != nil {
		if errors.Is(err, git.ErrRejected) {
			fmt.Fprintf(a.stderr, "rig push: %s: %v\n", branch, err)
		} else {
			fmt.Fprintf(a.stderr, "rig push: %s: %v\n", branch, err)
		}
		return 1
	}
	fmt.Fprintf(a.stdout, "pushed %s to origin\n", branch)
	return 0
}

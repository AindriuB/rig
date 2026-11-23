package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/git"
)

// newWorkspace makes a multi-repo workspace: a parent repository ws whose
// docs/plan/tasks holds 07-foo.md, and a code repository cloned inside it at
// ws/code from a bare origin. The child has no task files of its own and two
// branches for task 07, so the branch fallback alone is ambiguous.
func newWorkspace(t *testing.T) (ws, child, remote string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	remote, ws = filepath.Join(root, "origin.git"), filepath.Join(root, "ws")
	gitc(t, root, "init", "-q", "--bare", "-b", "main", remote)
	seed := filepath.Join(root, "seed")
	gitc(t, root, "init", "-q", "-b", "main", seed)
	gitc(t, seed, "commit", "-q", "--allow-empty", "-m", "first")
	gitc(t, seed, "push", "-q", remote, "main")

	gitc(t, root, "init", "-q", "-b", "main", ws)
	writeTask(t, ws, "07-foo.md")
	gitc(t, ws, "add", ".")
	gitc(t, ws, "commit", "-q", "-m", "plan")

	child = filepath.Join(ws, "code")
	gitc(t, ws, "clone", "-q", remote, child)
	gitc(t, child, "branch", "task/07-foo")
	gitc(t, child, "branch", "task/07-other")
	return ws, child, remote
}

func writeTask(t *testing.T, repo, name string) {
	t.Helper()
	dir := filepath.Join(repo, "docs", "plan", "tasks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("# task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTaskBranchFromTheWorkspaceRepo(t *testing.T) {
	_, child, _ := newWorkspace(t)
	got, err := taskBranch(ctx, git.Repo{Dir: child}, "07")
	if err != nil || got != "task/07-foo" {
		t.Fatalf("taskBranch = %q, %v; want task/07-foo", got, err)
	}
}

func TestTaskBranchFromAWorktreeOfTheChild(t *testing.T) {
	ws, child, _ := newWorkspace(t)
	wt := filepath.Join(filepath.Dir(ws), "wt")
	gitc(t, child, "worktree", "add", "-q", wt, "task/07-other")
	got, err := taskBranch(ctx, git.Repo{Dir: wt}, "07")
	if err != nil || got != "task/07-foo" {
		t.Fatalf("taskBranch = %q, %v; want task/07-foo", got, err)
	}
}

func TestTaskBranchPrefersTheChildsOwnTaskFile(t *testing.T) {
	_, child, _ := newWorkspace(t)
	writeTask(t, child, "07-other.md")
	got, err := taskBranch(ctx, git.Repo{Dir: child}, "07")
	if err != nil || got != "task/07-other" {
		t.Fatalf("taskBranch = %q, %v; want task/07-other", got, err)
	}
}

func TestTaskBranchStopsAtTheFirstEnclosingRepo(t *testing.T) {
	ws, child, _ := newWorkspace(t)
	os.RemoveAll(filepath.Join(ws, "docs"))
	// A repository further out still has the task file; it must not be read.
	outer := filepath.Dir(ws)
	gitc(t, outer, "init", "-q", "-b", "main")
	writeTask(t, outer, "07-foo.md")
	_, err := taskBranch(ctx, git.Repo{Dir: child}, "07")
	if err == nil || !strings.Contains(err.Error(), "matches 2 branches") {
		t.Fatalf("err = %v; want the ambiguous-branch error", err)
	}
}

func TestTaskBranchWithNoEnclosingRepoKeepsTheOldErrors(t *testing.T) {
	repo, _ := newRepo(t)
	os.Remove(filepath.Join(repo, "docs", "plan", "tasks", "07-foo.md"))
	gitc(t, repo, "branch", "-D", "task/07-foo")
	_, err := taskBranch(ctx, git.Repo{Dir: repo}, "07")
	if err == nil || !strings.Contains(err.Error(), "no task file docs/plan/tasks/07-*.md and no branch task/07-*") {
		t.Fatalf("err = %v", err)
	}
	if _, err := taskBranch(ctx, git.Repo{Dir: repo}, "../07"); err == nil {
		t.Fatal("a climbing id was accepted")
	}
}

func TestPushByTaskIDInAWorkspace(t *testing.T) {
	_, child, remote := newWorkspace(t)
	r := rig(t, child, nil, nil, "push", "07")
	if r.code != 0 || r.out != "pushed task/07-foo to origin\n" {
		t.Fatalf("push 07: %+v", r)
	}
	gitc(t, remote, "rev-parse", "task/07-foo")
}

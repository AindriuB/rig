package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AindriuB/rig/internal/config"
	"github.com/AindriuB/rig/internal/git"
)

const (
	taskStartUsage  = "usage: rig task start <slug> [--task-file <path>] [--json] [--repo <dir>]"
	taskFinishUsage = "usage: rig task finish <task> --sha <sha> [--json] [--repo <dir>]"

	// markerName is the file in a task worktree's private git directory that
	// ties it to its task file. The kit's owns-guard reads it.
	markerName = "kit-task"
)

// beforeWorktreeAdd runs just before git is asked to add the worktree. Tests
// use it to make another start win the race.
var beforeWorktreeAdd = func() {}

// writeMarker writes the marker file. Tests replace it to make it fail.
var writeMarker = os.WriteFile

// mainCheckout resolves --repo (or the cwd) to the repository's main
// checkout, never a linked worktree.
func (a *app) mainCheckout(ctx context.Context, repoFlag string) (git.Repo, error) {
	dir, err := a.repoDir(repoFlag)
	if err != nil {
		return git.Repo{}, err
	}
	wts, err := git.Repo{Dir: dir}.Worktrees(ctx)
	if err != nil {
		return git.Repo{}, err
	}
	if len(wts) == 0 {
		return git.Repo{}, fmt.Errorf("not a git working tree: %s", dir)
	}
	return git.Repo{Dir: filepath.FromSlash(wts[0].Path)}, nil
}

// baseBranch is the branch a task starts from and merges into: origin's HEAD
// branch, else main, master, trunk or develop, else the current branch, else
// HEAD.
func baseBranch(ctx context.Context, repo git.Repo) (string, error) {
	b, err := repo.DefaultBranch(ctx)
	if err != nil {
		return "", err
	}
	if b == "" {
		if b, err = repo.CurrentBranch(ctx); err != nil {
			return "", err
		}
	}
	if b == "" {
		b = "HEAD"
	}
	return b, nil
}

// underRealPath refuses a target whose nearest existing ancestor, with
// symlinks resolved, is not under the real path of the main checkout.
func underRealPath(main, target string) error {
	realMain, err := filepath.EvalSymlinks(main)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", main, err)
	}
	p := target
	for {
		if _, err := os.Lstat(p); err == nil {
			break
		}
		next := filepath.Dir(p)
		if next == p {
			return fmt.Errorf("no existing ancestor of %s", target)
		}
		p = next
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", p, err)
	}
	rel, err := filepath.Rel(realMain, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("worktrees.root resolves to %s, outside the checkout %s: a project file may not point outside the checkout; set it in the global config", real, realMain)
	}
	return nil
}

// taskStart implements `rig task start`: it makes the task's worktree and
// branch and ties the worktree to its task file.
//
// Exit 0: made. Exit 1: refused (no task file, or the path or branch is
// taken); nothing was created. Exit 2: usage, or an error; whatever was half
// made is removed again.
func (a *app) taskStart(ctx context.Context, args []string) int {
	const cmd = "task start"
	fs := newFlags()
	repoFlag, taskFlag := fs.str("--repo"), fs.str("--task-file")
	asJSON := fs.boolean("--json")
	pos, err := fs.parse(args)
	if err != nil || len(pos) != 1 {
		return a.fail(cmd, "%s", taskStartUsage)
	}
	slug := pos[0]
	if !validTaskName(slug) {
		return a.fail(cmd, "%q is not a task id", slug)
	}
	repo, err := a.mainCheckout(ctx, *repoFlag)
	if err != nil {
		return a.fail(cmd, "%v", err)
	}
	main := repo.Dir

	taskFile, code := a.startTaskFile(ctx, cmd, repo, slug, *taskFlag)
	if code != 0 {
		return code
	}

	remote, _ := repo.RemoteURL(ctx, "origin")
	layers, err := config.Open(config.Options{Root: main, Remote: remote})
	if err != nil {
		return a.fail(cmd, "read config: %v", err)
	}
	root, err := layers.WorktreesRoot()
	if err != nil {
		return a.fail(cmd, "%v", err)
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join(main, root)
	}
	if v, ok := layers.Get(config.KeyWorktreesRoot); !ok || v.Scope == config.ScopeLocal || v.Scope == config.ScopeRigJSON || v.Scope == config.ScopeDefault {
		if err := underRealPath(main, filepath.Join(root, filepath.Base(main), slug)); err != nil {
			return a.fail(cmd, "%v", err)
		}
	}
	wt := filepath.Join(root, filepath.Base(main), slug)
	branch := "task/" + slug

	if _, err := os.Lstat(wt); err == nil {
		return a.refuse(cmd, "worktree already exists: %s (another agent may hold it)", wt)
	}
	if ok, err := repo.BranchExists(ctx, branch); err != nil {
		return a.fail(cmd, "%v", err)
	} else if ok {
		return a.refuse(cmd, "branch already exists: %s: reuse it or pick a new task id", branch)
	}

	base, err := baseBranch(ctx, repo)
	if err != nil {
		return a.fail(cmd, "%v", err)
	}
	_ = repo.Fetch(ctx, "origin", base) // best effort
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		return a.fail(cmd, "%v", err)
	}
	beforeWorktreeAdd()
	// What existed before the add is not ours to take back.
	_, statErr := os.Lstat(wt)
	pathExisted := statErr == nil
	branchExisted, _ := repo.BranchExists(ctx, branch)
	if err := repo.WorktreeAdd(ctx, wt, branch, base); err != nil {
		// git may have made the branch and worktree before it failed (a
		// post-checkout hook, a timeout): take back only what is ours.
		return a.fail(cmd, "%s", rollBack(ctx, repo, wt, branch, base, branchExisted, pathExisted, err))
	}

	marked := filepath.ToSlash(taskFile)
	if err := a.mark(ctx, wt, marked); err != nil {
		// Never leave a worktree that owns-guard would treat as unguarded.
		return a.fail(cmd, "%s", rollBack(ctx, repo, wt, branch, base, false, false, err))
	}

	if *asJSON {
		return a.printJSON(map[string]string{"worktree": wt, "branch": branch, "base": base, "taskFile": marked})
	}
	fmt.Fprintf(a.stdout, "WORKTREE: %s\nBRANCH:   %s\nBASE:     %s\nOWNS:     enforced from %s\n", wt, branch, base, marked)
	return 0
}

// rollBack removes what a failed start made: the worktree if it exists (never
// with --force), then the branch if it exists and still points at base. It
// takes back nothing that existed before the add (branchExisted, pathExisted),
// and nothing at all when git says the branch or path already exists: another
// start won the race. It returns the message to print, which states only what
// was removed.
func rollBack(ctx context.Context, repo git.Repo, wt, branch, base string, branchExisted, pathExisted bool, cause error) string {
	if branchExisted || pathExisted || strings.Contains(cause.Error(), "already exists") {
		return fmt.Sprintf("%v (another start holds %s or %s: nothing was removed)", cause, wt, branch)
	}
	var removed []string
	done := func(note string) string {
		if len(removed) == 0 {
			return fmt.Sprintf("%v%s (nothing was removed)", cause, note)
		}
		return fmt.Sprintf("%v%s (removed again: %s)", cause, note, strings.Join(removed, " and "))
	}
	if _, err := os.Lstat(wt); err == nil {
		if rerr := repo.WorktreeRemove(ctx, wt); rerr != nil {
			return fmt.Sprintf("%v; and the worktree %s could not be removed again: %v", cause, wt, rerr)
		}
		removed = append(removed, "the worktree")
	}
	if ok, _ := repo.BranchExists(ctx, branch); ok {
		tip, _, _ := repo.ResolveRef(ctx, "refs/heads/"+branch)
		want, _, _ := repo.ResolveRef(ctx, base+"^{commit}")
		if tip == "" || tip != want {
			return done(fmt.Sprintf("; the branch %s was left: it is not at %s", branch, base))
		}
		if derr := repo.DeleteBranch(ctx, branch, true); derr != nil {
			return done(fmt.Sprintf("; and the branch %s could not be removed again: %v", branch, derr))
		}
		removed = append(removed, "the branch")
	}
	return done("")
}

// startTaskFile finds the task file for slug: --task-file, else the exact
// <slug>.md the shared lookup finds. A non-zero code is the exit code, and the
// reason is already printed.
func (a *app) startTaskFile(ctx context.Context, cmd string, repo git.Repo, slug, flag string) (string, int) {
	if flag != "" {
		p := flag
		if !filepath.IsAbs(p) {
			p = filepath.Join(a.cwd, p)
		}
		if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
			return "", a.refuse(cmd, "--task-file %s is not a file", flag)
		}
		return filepath.Clean(p), 0
	}
	want := slug + ".md"
	p, _, ok, err := findTaskFile(ctx, repo, func(name string) bool { return name == want })
	if err != nil {
		return "", a.fail(cmd, "%v", err)
	}
	if !ok {
		return "", a.refuse(cmd, "no task file %s in docs/plan/tasks of %s or of its workspace; pass --task-file <path>", want, repo.Dir)
	}
	return p, 0
}

// mark writes the marker: one line, the task file's path, in the worktree's
// private git directory, where it is never committed.
func (a *app) mark(ctx context.Context, wt, taskFile string) error {
	gitDir, err := git.Repo{Dir: wt}.AbsGitDir(ctx)
	if err != nil {
		return fmt.Errorf("write marker: %w", err)
	}
	if err := writeMarker(filepath.Join(filepath.FromSlash(gitDir), markerName), []byte(taskFile+"\n"), 0o644); err != nil {
		return fmt.Errorf("write marker: %w", err)
	}
	return nil
}

// refuse prints a refusal and returns exit code 1.
func (a *app) refuse(cmd, format string, v ...any) int {
	fmt.Fprintf(a.stderr, "rig %s: %s\n", cmd, fmt.Sprintf(format, v...))
	return 1
}

// validTaskName says whether name may name a task worktree and branch: the
// rules `rig push` applies to a task id, so no "/", "\", ":" or "..".
func validTaskName(name string) bool {
	return taskIDRE.MatchString(name) && !strings.Contains(name, "..")
}

// finishOut is what `rig task finish --json` prints.
type finishOut struct {
	Merged  finishMerged `json:"merged"`
	Kept    *finishKept  `json:"kept,omitempty"`
	Removed string       `json:"removed,omitempty"`
}

type finishMerged struct {
	Branch string `json:"branch"`
	Sha    string `json:"sha"`
	Base   string `json:"base"`
}

type finishKept struct {
	Worktree string `json:"worktree"`
	Reason   string `json:"reason"`
}

// taskFinish implements `rig task finish`: it merges exactly the commit that
// was verified into the default branch, then removes the task's worktree.
//
// Exit 0: merged (the worktree is kept, with the reason, when it could not be
// removed). Exit 1: refused, with nothing changed, or a conflict, aborted.
// Exit 2: usage or another error.
func (a *app) taskFinish(ctx context.Context, args []string) int {
	const cmd = "task finish"
	fs := newFlags()
	repoFlag, sha := fs.str("--repo"), fs.str("--sha")
	asJSON := fs.boolean("--json")
	pos, err := fs.parse(args)
	if err != nil || len(pos) != 1 || *sha == "" {
		return a.fail(cmd, "%s", taskFinishUsage)
	}
	task := strings.TrimPrefix(pos[0], "task/")
	if !validTaskName(task) {
		return a.fail(cmd, "%q is not a task id", pos[0])
	}
	repo, err := a.mainCheckout(ctx, *repoFlag)
	if err != nil {
		return a.fail(cmd, "%v", err)
	}

	wt, branch, code := a.finishWorktree(ctx, cmd, repo, task)
	if code != 0 {
		return code
	}
	if n, err := (git.Repo{Dir: wt}).DirtyCount(ctx); err != nil {
		return a.fail(cmd, "%v", err)
	} else if n > 0 {
		return a.refuse(cmd, "uncommitted changes in %s: the implementer did not finish", wt)
	}
	want, ok, err := repo.CommitOf(ctx, *sha)
	if err != nil {
		return a.fail(cmd, "%v", err)
	}
	if !ok {
		return a.refuse(cmd, "%s is not a commit in %s", *sha, filepath.Base(repo.Dir))
	}
	tip, _, err := repo.ResolveRef(ctx, "refs/heads/"+branch)
	if err != nil {
		return a.fail(cmd, "%v", err)
	}
	if tip != want {
		return a.refuse(cmd, "%s is at %s, but /verify passed %s. The new commits are untested: run /verify again.", branch, short(tip), short(want))
	}
	base, err := baseBranch(ctx, repo)
	if err != nil {
		return a.fail(cmd, "%v", err)
	}

	if err := repo.Checkout(ctx, base); err != nil {
		return a.refuse(cmd, "cannot switch the main checkout to %s: %v", base, err)
	}
	msg := fmt.Sprintf("merge %s: %s @ %s", task, branch, short(want))
	if err := repo.MergeNoFF(ctx, msg, want); err != nil {
		if errors.Is(err, git.ErrMergeConflict) {
			return a.refuse(cmd, "merge conflict on %s: aborted, nothing changed. Resolve by hand.", branch)
		}
		return a.fail(cmd, "%v", err)
	}

	out := finishOut{Merged: finishMerged{Branch: branch, Sha: want, Base: base}}
	// The tip may have moved while the merge ran. If so, keep the worktree and
	// the branch: the extra commits are not merged and must not be lost.
	if now, _, err := repo.ResolveRef(ctx, "refs/heads/"+branch); err != nil || now != want {
		out.Kept = &finishKept{Worktree: wt, Reason: "branch moved during the merge; its new commits are not merged"}
		return a.printFinish(out, *asJSON, "%s", out.Kept.Reason)
	}
	if err := repo.WorktreeRemove(ctx, wt); err != nil {
		reason := fmt.Sprintf("worktree remove failed: %v; close programs using it, then git worktree remove %s", err, wt)
		out.Kept = &finishKept{Worktree: wt, Reason: reason}
		return a.printFinish(out, *asJSON, "%s", reason)
	}
	out.Removed = wt
	if err := repo.DeleteBranch(ctx, branch, false); err != nil {
		a.printFinish(out, *asJSON, "")
		return a.fail(cmd, "merged and removed the worktree, but the branch was not deleted: %v", err)
	}
	return a.printFinish(out, *asJSON, "")
}

// printFinish prints the outcome: JSON, or the MERGED line followed by KEPT
// (with the reason) or REMOVED.
func (a *app) printFinish(out finishOut, asJSON bool, format string, v ...any) int {
	if asJSON {
		return a.printJSON(out)
	}
	fmt.Fprintf(a.stdout, "MERGED: %s @ %s -> %s\n", out.Merged.Branch, short(out.Merged.Sha), out.Merged.Base)
	if out.Kept != nil {
		fmt.Fprintf(a.stdout, "KEPT:   %s (%s)\n", out.Kept.Worktree, fmt.Sprintf(format, v...))
		return 0
	}
	fmt.Fprintf(a.stdout, "REMOVED: %s\n", out.Removed)
	return 0
}

// finishWorktree finds the task's worktree and branch in `git worktree list`:
// the worktree whose directory name is exactly task wins, else the one whose
// name starts with "<task>-". Several is a refusal that lists them.
func (a *app) finishWorktree(ctx context.Context, cmd string, repo git.Repo, task string) (wt, branch string, code int) {
	wts, err := repo.Worktrees(ctx)
	if err != nil {
		return "", "", a.fail(cmd, "%v", err)
	}
	var exact *git.Worktree
	var matches []git.Worktree
	for i, w := range wts {
		if i == 0 {
			continue // the main checkout
		}
		name := filepath.Base(filepath.FromSlash(w.Path))
		switch {
		case name == task:
			exact = &wts[i]
		case strings.HasPrefix(name, task+"-"):
			matches = append(matches, w)
		}
	}
	var found git.Worktree
	switch {
	case exact != nil:
		found = *exact
	case len(matches) == 1:
		found = matches[0]
	case len(matches) > 1:
		paths := make([]string, len(matches))
		for i, m := range matches {
			paths[i] = filepath.FromSlash(m.Path)
		}
		return "", "", a.refuse(cmd, "task %s matches more than one worktree; pass the full name: %s", task, strings.Join(paths, " "))
	default:
		return "", "", a.refuse(cmd, "no worktree for task %s under %s", task, filepath.Base(repo.Dir))
	}
	if found.Branch == "" {
		return "", "", a.refuse(cmd, "the worktree %s has no branch checked out", found.Path)
	}
	return filepath.FromSlash(found.Path), found.Branch, 0
}

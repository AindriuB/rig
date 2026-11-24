package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/AindriuB/rig/internal/git"
)

var (
	taskIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	// branchRE is the whole alphabet a pushable branch may use. It has no
	// ":" (refspec), "+" (force), "*" (pattern), "~" "^" "@" "{" (revision
	// syntax), space or control character.
	branchRE = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

// checkBranch says why branch may not be pushed, or returns nil. A pushable
// branch is <prefix>/<name>, with the prefix one of prefixes, written in the
// plain alphabet above. So it is never a bare name such as main, never a
// refspec, never a tag or other ref, and never a path that climbs with "..".
func checkBranch(branch string, prefixes []string) error {
	if !branchRE.MatchString(branch) {
		return fmt.Errorf("%q is not a plain branch name", branch)
	}
	segs := strings.Split(branch, "/")
	if len(segs) < 2 {
		return fmt.Errorf("%q is not under %s", branch, prefixList(prefixes))
	}
	ok := false
	for _, p := range prefixes {
		ok = ok || segs[0] == p
	}
	if !ok {
		return fmt.Errorf("%q is not under %s", branch, prefixList(prefixes))
	}
	for _, s := range segs {
		switch {
		case s == "":
			return fmt.Errorf("%q has an empty path segment", branch)
		case strings.Contains(s, ".."):
			return fmt.Errorf("%q contains \"..\"", branch)
		case strings.HasPrefix(s, "."), strings.HasPrefix(s, "-"),
			strings.HasSuffix(s, "."), strings.HasSuffix(s, ".lock"):
			return fmt.Errorf("%q is not a valid branch name", branch)
		}
	}
	return nil
}

func prefixList(prefixes []string) string {
	out := make([]string, len(prefixes))
	for i, p := range prefixes {
		out[i] = p + "/"
	}
	return strings.Join(out, " or ")
}

// taskBranch resolves a task id to its branch, task/<id>-<slug>. The slug
// comes from the task file docs/plan/tasks/<id>-<slug>.md in the main
// checkout, the same lookup a worktree script would use; when the file is gone, as
// after /record, the one local branch task/<id>-* is used instead. In a
// multi-repo workspace the task files live in the enclosing workspace
// repository, so that repository's main checkout is read after the code
// repository's own; a branch named there is taken only when it exists
// locally. A code repository reached through a symlink is not handled: the
// walk up starts from git's resolved path.
func taskBranch(ctx context.Context, repo git.Repo, id string) (string, error) {
	if !taskIDRE.MatchString(id) || strings.Contains(id, "..") {
		return "", fmt.Errorf("%q is not a task id", id)
	}
	prefix := id + "-"
	file, inWorkspace, found, err := findTaskFile(ctx, repo, func(name string) bool {
		return strings.HasPrefix(name, prefix)
	})
	if err != nil {
		return "", err
	}
	if found {
		b := "task/" + strings.TrimSuffix(filepath.Base(file), ".md")
		// The workspace may plan for other repositories too, so its task
		// file counts only when its branch exists here.
		if !inWorkspace || hasBranch(ctx, repo, b) {
			return b, nil
		}
	}
	branches, err := repo.Branches(ctx, "task/"+id+"-*")
	if err != nil {
		return "", err
	}
	switch len(branches) {
	case 1:
		return branches[0], nil
	case 0:
		return "", fmt.Errorf("no task file docs/plan/tasks/%s-*.md and no branch task/%s-*", id, id)
	}
	return "", fmt.Errorf("task id %s matches %d branches (%s): name the branch", id, len(branches), strings.Join(branches, ", "))
}

// findTaskFile is the one task-file lookup `rig push` and `rig task start`
// share. It looks in docs/plan/tasks of the code repository's main checkout,
// then in the enclosing workspace repository's, and returns the first ".md"
// file, by name, that match accepts (match gets the file name with its
// extension). inWorkspace says which of the two it came from. The path is
// absolute, in the platform's own form.
func findTaskFile(ctx context.Context, repo git.Repo, match func(name string) bool) (path string, inWorkspace, ok bool, err error) {
	wts, err := repo.Worktrees(ctx)
	if err != nil {
		return "", false, false, err
	}
	if len(wts) == 0 {
		return "", false, false, nil
	}
	main := filepath.FromSlash(wts[0].Path)
	if p, ok := taskFileIn(main, match); ok {
		return p, false, true, nil
	}
	if ws, ok := workspaceMain(ctx, main); ok {
		if p, ok := taskFileIn(ws, match); ok {
			return p, true, true, nil
		}
	}
	return "", false, false, nil
}

// taskFileIn is the first ".md" file under root/docs/plan/tasks, by name,
// that match accepts.
func taskFileIn(root string, match func(name string) bool) (string, bool) {
	dir := filepath.Join(root, "docs", "plan", "tasks")
	entries, _ := os.ReadDir(dir) // sorted by name
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && strings.HasSuffix(name, ".md") && match(name) {
			return filepath.Join(dir, name), true
		}
	}
	return "", false
}

// workspaceMain is the main checkout of the nearest git repository enclosing
// top, the code repository's main checkout, or false when there is none.
// git does the walk up from top's parent and stops at the first repository.
func workspaceMain(ctx context.Context, top string) (string, bool) {
	parent := filepath.Dir(filepath.Clean(top))
	if parent == filepath.Clean(top) {
		return "", false // top is a filesystem root
	}
	wts, err := git.Repo{Dir: parent}.Worktrees(ctx)
	if err != nil || len(wts) == 0 {
		return "", false
	}
	return filepath.FromSlash(wts[0].Path), true
}

// hasBranch says whether the local branch exists. Errors count as no.
func hasBranch(ctx context.Context, repo git.Repo, branch string) bool {
	got, err := repo.Branches(ctx, branch)
	return err == nil && len(got) == 1 && got[0] == branch
}

package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AindriuB/rig/internal/proc"
)

// fetchTimeout bounds the best-effort fetch `rig task start` makes.
const fetchTimeout = time.Minute

// ErrMergeConflict reports a merge that conflicted. The merge has been
// aborted: the working tree and the branch are as they were.
var ErrMergeConflict = errors.New("merge conflict")

// Fetch updates origin's tracking ref for one branch. Callers that treat it as
// best effort ignore the error.
func (r Repo) Fetch(ctx context.Context, remote, branch string) error {
	if strings.HasPrefix(remote, "-") || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("git fetch: invalid remote or branch")
	}
	args := []string{"fetch", "--quiet", remote, branch}
	res, err := r.run(ctx, fetchTimeout, args...)
	if err != nil {
		return wrap(args, res, err)
	}
	return nil
}

// WorktreeAdd creates a worktree at path on a new branch started at base.
func (r Repo) WorktreeAdd(ctx context.Context, path, branch, base string) error {
	_, err := r.out(ctx, "worktree", "add", "-b", branch, "--", path, base)
	return err
}

// WorktreeRemove removes the worktree at path, never with --force. The error
// is one line: git's first line of complaint.
func (r Repo) WorktreeRemove(ctx context.Context, path string) error {
	args := []string{"worktree", "remove", "--", path}
	res, err := r.run(ctx, queryTimeout, args...)
	if err == nil {
		return nil
	}
	var ee *proc.ExitError
	if errors.As(err, &ee) {
		first, _, _ := strings.Cut(strings.TrimSpace(string(res.Stderr)), "\n")
		if first = strings.TrimSpace(first); first != "" {
			return errors.New(first)
		}
	}
	return wrap(args, res, err)
}

// AbsGitDir is the absolute path of this working tree's own git directory,
// which for a linked worktree is private to it.
func (r Repo) AbsGitDir(ctx context.Context) (string, error) {
	return r.out(ctx, "rev-parse", "--absolute-git-dir")
}

// CommitOf resolves rev to a commit id; ok is false when it is not a commit.
func (r Repo) CommitOf(ctx context.Context, rev string) (oid string, ok bool, err error) {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return "", false, nil
	}
	return r.ResolveRef(ctx, rev+"^{commit}")
}

// Checkout switches the working tree to an existing branch.
func (r Repo) Checkout(ctx context.Context, branch string) error {
	if strings.HasPrefix(branch, "-") {
		return fmt.Errorf("git checkout: invalid branch %q", branch)
	}
	_, err := r.out(ctx, "checkout", "--quiet", branch, "--")
	return err
}

// MergeNoFF merges the commit sha into the checked-out branch with a merge
// commit and the given message. On a conflict it aborts the merge and returns
// ErrMergeConflict.
func (r Repo) MergeNoFF(ctx context.Context, message, sha string) error {
	if strings.HasPrefix(sha, "-") {
		return fmt.Errorf("git merge: invalid commit %q", sha)
	}
	args := []string{"merge", "--no-ff", "-m", message, sha}
	res, err := r.run(ctx, queryTimeout, args...)
	if err == nil {
		return nil
	}
	var ee *proc.ExitError
	if errors.As(err, &ee) {
		// Best effort: if no merge is under way, there is nothing to abort.
		_, _ = r.run(ctx, queryTimeout, "merge", "--abort")
		return ErrMergeConflict
	}
	return wrap(args, res, err)
}

// DeleteBranch deletes a local branch: with -d, which refuses an unmerged
// one, or with -D when force is set.
func (r Repo) DeleteBranch(ctx context.Context, branch string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	if strings.HasPrefix(branch, "-") {
		return fmt.Errorf("git branch: invalid branch %q", branch)
	}
	_, err := r.out(ctx, "branch", flag, "--", branch)
	return err
}

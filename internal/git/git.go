// Package git wraps the handful of git calls rig makes. Every call is an
// argument vector run through proc; none goes through a shell.
package git

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/AindriuB/rig/internal/proc"
)

// Repo is a working tree, or a directory inside one.
type Repo struct {
	Dir string
}

const (
	queryTimeout = 30 * time.Second
	pushTimeout  = 5 * time.Minute
)

// env keeps git's messages parseable and stops it prompting for credentials,
// which would hang a caller that has no terminal.
var env = []string{"LC_ALL=C", "GIT_TERMINAL_PROMPT=0"}

func (r Repo) run(ctx context.Context, timeout time.Duration, args ...string) (proc.Result, error) {
	full := args
	if r.Dir != "" {
		full = append([]string{"-C", r.Dir}, args...)
	}
	return proc.Run(ctx, proc.Options{Env: env, Timeout: timeout}, "git", full...)
}

func (r Repo) out(ctx context.Context, args ...string) (string, error) {
	res, err := r.run(ctx, queryTimeout, args...)
	if err != nil {
		return "", wrap(args, res, err)
	}
	return strings.TrimRight(string(res.Stdout), "\r\n"), nil
}

func wrap(args []string, res proc.Result, err error) error {
	if msg := lastLine(res.Stderr); msg != "" {
		return fmt.Errorf("git %s: %s: %w", args[0], msg, err)
	}
	return fmt.Errorf("git %s: %w", args[0], err)
}

func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// TopLevel is the root of the working tree.
func (r Repo) TopLevel(ctx context.Context) (string, error) {
	return r.out(ctx, "rev-parse", "--show-toplevel")
}

// CommonDir is the absolute path of the git directory shared by every
// worktree of the repository.
func (r Repo) CommonDir(ctx context.Context) (string, error) {
	return r.out(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
}

// CurrentBranch is the checked-out branch, or "" on a detached HEAD.
func (r Repo) CurrentBranch(ctx context.Context) (string, error) {
	res, err := r.run(ctx, queryTimeout, "symbolic-ref", "--quiet", "--short", "HEAD")
	var ee *proc.ExitError
	if errors.As(err, &ee) && ee.Code == 1 {
		return "", nil
	}
	if err != nil {
		return "", wrap([]string{"symbolic-ref"}, res, err)
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

// BranchExists reports whether refs/heads/<branch> exists.
func (r Repo) BranchExists(ctx context.Context, branch string) (bool, error) {
	res, err := r.run(ctx, queryTimeout, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	var ee *proc.ExitError
	if errors.As(err, &ee) && ee.Code == 1 {
		return false, nil
	}
	if err != nil {
		return false, wrap([]string{"show-ref"}, res, err)
	}
	return true, nil
}

// Branches lists local branch names matching a for-each-ref pattern below
// refs/heads/, for example "task/07-*".
func (r Repo) Branches(ctx context.Context, pattern string) ([]string, error) {
	out, err := r.out(ctx, "for-each-ref", "--format=%(refname:short)", "refs/heads/"+pattern)
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

// RemoteURL is the fetch URL of a remote.
func (r Repo) RemoteURL(ctx context.Context, remote string) (string, error) {
	return r.out(ctx, "remote", "get-url", "--", remote)
}

// UserName is git's user.name for the repository. It is an error when
// user.name is not set.
func (r Repo) UserName(ctx context.Context) (string, error) {
	return r.out(ctx, "config", "user.name")
}

// DefaultBranch is origin's HEAD branch, falling back to the first of main,
// master, trunk and develop that exists locally. It is "" when none does.
func (r Repo) DefaultBranch(ctx context.Context) (string, error) {
	res, err := r.run(ctx, queryTimeout, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err == nil {
		if b := strings.TrimPrefix(strings.TrimSpace(string(res.Stdout)), "origin/"); b != "" {
			return b, nil
		}
	}
	for _, c := range []string{"main", "master", "trunk", "develop"} {
		ok, err := r.BranchExists(ctx, c)
		if err != nil {
			return "", err
		}
		if ok {
			return c, nil
		}
	}
	return "", nil
}

// Worktree is one entry of `git worktree list`.
type Worktree struct {
	Path     string
	Head     string
	Branch   string // short name; "" when detached or bare
	Bare     bool
	Detached bool
}

// Worktrees lists the repository's worktrees, the main one first.
func (r Repo) Worktrees(ctx context.Context) ([]Worktree, error) {
	out, err := r.out(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return parseWorktrees(out), nil
}

func parseWorktrees(out string) []Worktree {
	var wts []Worktree
	var cur *Worktree
	for _, line := range strings.Split(out, "\n") {
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			wts = append(wts, Worktree{Path: val})
			cur = &wts[len(wts)-1]
		case "HEAD":
			if cur != nil {
				cur.Head = val
			}
		case "branch":
			if cur != nil {
				cur.Branch = strings.TrimPrefix(val, "refs/heads/")
			}
		case "bare":
			if cur != nil {
				cur.Bare = true
			}
		case "detached":
			if cur != nil {
				cur.Detached = true
			}
		}
	}
	return wts
}

// DirtyCount is the number of changed or untracked paths in the working tree.
func (r Repo) DirtyCount(ctx context.Context) (int, error) {
	out, err := r.out(ctx, "status", "--porcelain")
	if err != nil || out == "" {
		return 0, err
	}
	return len(strings.Split(out, "\n")), nil
}

// AheadBehind counts the commits ref has that base lacks, and the reverse.
func (r Repo) AheadBehind(ctx context.Context, base, ref string) (ahead, behind int, err error) {
	out, err := r.out(ctx, "rev-list", "--left-right", "--count", base+"..."+ref, "--")
	if err != nil {
		return 0, 0, err
	}
	f := strings.Fields(out)
	if len(f) != 2 {
		return 0, 0, fmt.Errorf("git rev-list: unexpected output %q", out)
	}
	behind, err1 := strconv.Atoi(f[0])
	ahead, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("git rev-list: unexpected output %q", out)
	}
	return ahead, behind, nil
}

// Conflicts reports whether merging ref into base would conflict. It writes
// nothing to the working tree or the index.
func (r Repo) Conflicts(ctx context.Context, base, ref string) (bool, error) {
	args := []string{"merge-tree", "--write-tree", "--name-only", base, ref}
	res, err := r.run(ctx, queryTimeout, args...)
	var ee *proc.ExitError
	if errors.As(err, &ee) && ee.Code == 1 {
		return true, nil
	}
	if err != nil {
		return false, wrap(args, res, err)
	}
	return false, nil
}

// plainBranchRE is the alphabet Push accepts in a branch name.
var plainBranchRE = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// ErrRejected reports a push the remote, or a local hook, turned down.
var ErrRejected = errors.New("push rejected")

// Push sends one local branch to the branch of the same name on remote and
// sets it as the upstream. The refspec is spelled out in full, so neither
// `push.default` nor a configured `remote.<name>.push` can widen it, and it
// carries no `+`, so it can never force. Tags do not follow.
//
// Push does not decide whether the branch may be pushed; the caller does.
func (r Repo) Push(ctx context.Context, remote, branch string) error {
	if remote == "" || strings.HasPrefix(remote, "-") {
		return fmt.Errorf("git push: invalid remote %q", remote)
	}
	// A "*" would turn the refspec into a pattern over many branches, and
	// ":" or "+" would change its meaning, so only a plain name gets through.
	if !plainBranchRE.MatchString(branch) || strings.Contains(branch, "..") ||
		strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.Contains(branch, "//") {
		return fmt.Errorf("git push: %q is not a plain branch name", branch)
	}
	ref := "refs/heads/" + branch
	args := []string{"push", "--no-follow-tags", "--set-upstream", "--", remote, ref + ":" + ref}
	res, err := r.run(ctx, pushTimeout, args...)
	var ee *proc.ExitError
	if errors.As(err, &ee) {
		return fmt.Errorf("%w: %s", ErrRejected, lastLine(res.Stderr))
	}
	if err != nil {
		return wrap(args, res, err)
	}
	return nil
}

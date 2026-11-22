package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var ctx = context.Background()

// sh runs git in dir and fails the test on error.
func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRepo makes a repository with one commit on main and a bare origin.
func newRepo(t *testing.T) (repo, remote string) {
	t.Helper()
	root := t.TempDir()
	remote = filepath.Join(root, "origin.git")
	repo = filepath.Join(root, "repo")
	sh(t, root, "init", "-q", "--bare", "-b", "main", remote)
	sh(t, root, "init", "-q", "-b", "main", repo)
	sh(t, repo, "commit", "-q", "--allow-empty", "-m", "first")
	sh(t, repo, "remote", "add", "origin", remote)
	sh(t, repo, "push", "-q", "origin", "main")
	return repo, remote
}

func TestHostOf(t *testing.T) {
	cases := map[string]string{
		"https://github.com/o/r.git":            "github.com",
		"https://user@GitHub.com/o/r":           "github.com",
		"ssh://git@gitlab.example.com:2222/o/r": "gitlab.example.com",
		"git@github.com:o/r.git":                "github.com",
		"gitlab.com:o/r.git":                    "gitlab.com",
		"/srv/git/r.git":                        "",
		"../r.git":                              "",
		`C:\repos\r`:                            "",
		"./a:b":                                 "",
		"":                                      "",
	}
	for in, want := range cases {
		if got := HostOf(in); got != want {
			t.Errorf("HostOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestQueries(t *testing.T) {
	repo, remote := newRepo(t)
	r := Repo{Dir: repo}

	if b, err := r.CurrentBranch(ctx); err != nil || b != "main" {
		t.Fatalf("CurrentBranch = %q, %v", b, err)
	}
	if u, err := r.RemoteURL(ctx, "origin"); err != nil || u != remote {
		t.Fatalf("RemoteURL = %q, %v", u, err)
	}
	if b, err := r.DefaultBranch(ctx); err != nil || b != "main" {
		t.Fatalf("DefaultBranch = %q, %v", b, err)
	}
	if ok, _ := r.BranchExists(ctx, "nope"); ok {
		t.Fatal("BranchExists(nope) = true")
	}

	sh(t, repo, "checkout", "-q", "-b", "task/07-foo")
	sh(t, repo, "commit", "-q", "--allow-empty", "-m", "work")
	if a, b, err := r.AheadBehind(ctx, "main", "task/07-foo"); err != nil || a != 1 || b != 0 {
		t.Fatalf("AheadBehind = %d, %d, %v", a, b, err)
	}
	if got, err := r.Branches(ctx, "task/07-*"); err != nil || len(got) != 1 || got[0] != "task/07-foo" {
		t.Fatalf("Branches = %v, %v", got, err)
	}
	os.WriteFile(filepath.Join(repo, "new.txt"), []byte("x"), 0o644)
	if n, err := r.DirtyCount(ctx); err != nil || n != 1 {
		t.Fatalf("DirtyCount = %d, %v", n, err)
	}

	sh(t, repo, "checkout", "-q", "--detach")
	if b, err := r.CurrentBranch(ctx); err != nil || b != "" {
		t.Fatalf("detached CurrentBranch = %q, %v", b, err)
	}
}

func TestWorktrees(t *testing.T) {
	repo, _ := newRepo(t)
	wt := filepath.Join(filepath.Dir(repo), "wt")
	sh(t, repo, "worktree", "add", "-q", "-b", "task/01-a", wt)
	got, err := Repo{Dir: repo}.Worktrees(ctx)
	if err != nil || len(got) != 2 {
		t.Fatalf("Worktrees = %v, %v", got, err)
	}
	if got[0].Branch != "main" || got[1].Branch != "task/01-a" || got[1].Head == "" {
		t.Fatalf("Worktrees = %+v", got)
	}
	common, err := Repo{Dir: wt}.CommonDir(ctx)
	want, _ := filepath.EvalSymlinks(filepath.Join(repo, ".git"))
	if got, _ := filepath.EvalSymlinks(common); err != nil || got != want {
		t.Fatalf("CommonDir = %q, want %q (%v)", common, want, err)
	}
}

func TestConflicts(t *testing.T) {
	repo, _ := newRepo(t)
	write := func(branch, body string) {
		sh(t, repo, "checkout", "-q", "-B", branch, "main")
		os.WriteFile(filepath.Join(repo, "f.txt"), []byte(body), 0o644)
		sh(t, repo, "add", "f.txt")
		sh(t, repo, "commit", "-q", "-m", branch)
	}
	write("a", "one\n")
	write("b", "two\n")
	sh(t, repo, "checkout", "-q", "-B", "c", "main")
	sh(t, repo, "commit", "-q", "--allow-empty", "-m", "c")
	r := Repo{Dir: repo}
	if got, err := r.Conflicts(ctx, "a", "b"); err != nil || !got {
		t.Fatalf("Conflicts(a, b) = %v, %v", got, err)
	}
	if got, err := r.Conflicts(ctx, "a", "c"); err != nil || got {
		t.Fatalf("Conflicts(a, c) = %v, %v", got, err)
	}
}

func TestPush(t *testing.T) {
	repo, remote := newRepo(t)
	mainBefore := sh(t, remote, "rev-parse", "main")
	sh(t, repo, "checkout", "-q", "-b", "task/07-foo")
	sh(t, repo, "commit", "-q", "--allow-empty", "-m", "work")
	sh(t, repo, "tag", "-a", "v1", "-m", "v1")
	// Config that would widen a bare `git push` must not widen this one.
	sh(t, repo, "config", "remote.origin.push", "refs/heads/task/07-foo:refs/heads/main")
	sh(t, repo, "config", "push.followTags", "true")

	r := Repo{Dir: repo}
	if err := r.Push(ctx, "origin", "task/07-foo"); err != nil {
		t.Fatal(err)
	}
	if got, want := sh(t, remote, "rev-parse", "task/07-foo"), sh(t, repo, "rev-parse", "HEAD"); got != want {
		t.Fatalf("remote branch = %s, want %s", got, want)
	}
	if got := sh(t, remote, "rev-parse", "main"); got != mainBefore {
		t.Fatal("push moved the remote's main")
	}
	if tags := sh(t, remote, "tag", "-l"); tags != "" {
		t.Fatalf("tags followed: %q", tags)
	}
	if up := sh(t, repo, "rev-parse", "--abbrev-ref", "@{u}"); up != "origin/task/07-foo" {
		t.Fatalf("upstream = %q", up)
	}

	// A rewritten branch is not a fast-forward, and Push never forces.
	sh(t, repo, "commit", "-q", "--amend", "--allow-empty", "-m", "rewritten")
	if err := r.Push(ctx, "origin", "task/07-foo"); !errors.Is(err, ErrRejected) {
		t.Fatalf("non-fast-forward push: err = %v, want ErrRejected", err)
	}
	if err := r.Push(ctx, "origin", "task/no-such"); !errors.Is(err, ErrRejected) {
		t.Fatalf("missing branch: err = %v, want ErrRejected", err)
	}

	// A name that is not one plain branch never reaches git.
	sh(t, repo, "branch", "task/08-bar")
	for _, bad := range []string{"task/*", "task/07-foo:main", "+task/07-foo", "", "task/a b", "task/../main", "/task/x", "task//x", "task/x/", "refs/heads/*"} {
		err := r.Push(ctx, "origin", bad)
		if err == nil || errors.Is(err, ErrRejected) {
			t.Errorf("Push(%q) = %v, want a refusal before git runs", bad, err)
		}
	}
	if got := sh(t, remote, "for-each-ref", "--format=%(refname:short)", "refs/heads/task"); got != "task/07-foo" {
		t.Fatalf("remote task branches = %q", got)
	}
	if got := sh(t, remote, "rev-parse", "main"); got != mainBefore {
		t.Fatal("a refused push moved the remote's main")
	}
}

func TestUserName(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	repo, _ := newRepo(t)
	r := Repo{Dir: repo}
	if got, err := r.UserName(ctx); err == nil {
		t.Fatalf("UserName unset = %q, want an error", got)
	}
	sh(t, repo, "config", "user.name", "Ada Lovelace")
	if got, err := r.UserName(ctx); err != nil || got != "Ada Lovelace" {
		t.Fatalf("UserName = %q, %v", got, err)
	}
}

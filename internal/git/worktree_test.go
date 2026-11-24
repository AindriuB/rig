package git

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gitIdentity gives the merges the code under test runs an identity and no
// user config.
func gitIdentity(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
		"GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com",
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_SYSTEM": os.DevNull,
	} {
		t.Setenv(k, v)
	}
}

func TestWorktreeLifecycle(t *testing.T) {
	gitIdentity(t)
	repo, _ := newRepo(t)
	r := Repo{Dir: repo}
	wt := filepath.Join(t.TempDir(), "wt")

	if err := r.Fetch(ctx, "origin", "main"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if err := r.Fetch(ctx, "nowhere", "main"); err == nil {
		t.Fatal("Fetch from a missing remote succeeded")
	}
	if err := r.WorktreeAdd(ctx, wt, "task/a", "main"); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if err := r.WorktreeAdd(ctx, wt, "task/a", "main"); err == nil {
		t.Fatal("WorktreeAdd over an existing worktree succeeded")
	}
	w := Repo{Dir: wt}
	gd, err := w.AbsGitDir(ctx)
	if err != nil || !filepath.IsAbs(gd) {
		t.Fatalf("AbsGitDir = %q, %v", gd, err)
	}

	sh(t, wt, "commit", "-q", "--allow-empty", "-m", "work")
	sha := sh(t, wt, "rev-parse", "HEAD")
	if oid, ok, err := r.CommitOf(ctx, sha); err != nil || !ok || oid != sha {
		t.Fatalf("CommitOf = %q, %v, %v", oid, ok, err)
	}
	if _, ok, err := r.CommitOf(ctx, "deadbeef"); err != nil || ok {
		t.Fatalf("CommitOf(unknown) = %v, %v", ok, err)
	}
	if _, ok, err := r.CommitOf(ctx, "--all"); err != nil || ok {
		t.Fatalf("CommitOf(option) = %v, %v", ok, err)
	}

	if err := r.Checkout(ctx, "main"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := r.MergeNoFF(ctx, "merge msg", sha); err != nil {
		t.Fatalf("MergeNoFF: %v", err)
	}
	if got := sh(t, repo, "log", "-1", "--format=%s"); got != "merge msg" {
		t.Fatalf("merge subject = %q", got)
	}
	if err := r.WorktreeRemove(ctx, wt); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
	if _, err := os.Stat(wt); err == nil {
		t.Fatal("worktree directory still there")
	}
	if err := r.DeleteBranch(ctx, "task/a", false); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	if ok, _ := r.BranchExists(ctx, "task/a"); ok {
		t.Fatal("branch still there")
	}
}

func TestMergeNoFFConflictIsAborted(t *testing.T) {
	gitIdentity(t)
	repo, _ := newRepo(t)
	r := Repo{Dir: repo}
	sh(t, repo, "checkout", "-q", "-b", "side")
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("side\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sh(t, repo, "add", "f")
	sh(t, repo, "commit", "-q", "-m", "side")
	sha := sh(t, repo, "rev-parse", "HEAD")
	sh(t, repo, "checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sh(t, repo, "add", "f")
	sh(t, repo, "commit", "-q", "-m", "main")
	before := sh(t, repo, "rev-parse", "HEAD")

	err := r.MergeNoFF(ctx, "m", sha)
	if !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("err = %v, want ErrMergeConflict", err)
	}
	if got := sh(t, repo, "rev-parse", "HEAD"); got != before {
		t.Fatal("HEAD moved")
	}
	if out := sh(t, repo, "status", "--porcelain"); out != "" {
		t.Fatalf("merge left the tree dirty: %q", out)
	}
}

func TestWorktreeRemoveReasonIsTheFirstLine(t *testing.T) {
	repo, _ := newRepo(t)
	r := Repo{Dir: repo}
	wt := filepath.Join(t.TempDir(), "wt")
	if err := r.WorktreeAdd(ctx, wt, "task/b", "main"); err != nil {
		t.Fatal(err)
	}
	sh(t, repo, "worktree", "lock", wt)
	err := r.WorktreeRemove(ctx, wt)
	if err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("err = %v, want one naming the lock", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Fatalf("reason is more than one line: %q", err)
	}
}

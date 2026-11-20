package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hook installs a pre-push hook that leaves a marker, so a test can tell that
// `git push` never started, not just that nothing arrived.
func hook(t *testing.T, repo string) (ran func() bool) {
	t.Helper()
	marker := filepath.Join(filepath.Dir(repo), "pre-push-ran")
	path := filepath.Join(repo, ".git", "hooks", "pre-push")
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return func() bool { _, err := os.Stat(marker); return err == nil }
}

func TestPushByTaskIDAndBranch(t *testing.T) {
	repo, remote := newRepo(t)
	mainBefore := gitc(t, remote, "rev-parse", "main")
	gitc(t, repo, "checkout", "-q", "task/07-foo")
	gitc(t, repo, "commit", "-q", "--allow-empty", "-m", "work")

	r := rig(t, repo, nil, nil, "push", "07")
	if r.code != 0 || r.out != "pushed task/07-foo to origin\n" {
		t.Fatalf("push 07: %+v", r)
	}
	if got, want := gitc(t, remote, "rev-parse", "task/07-foo"), gitc(t, repo, "rev-parse", "HEAD"); got != want {
		t.Fatalf("remote task/07-foo = %s, want %s", got, want)
	}

	gitc(t, repo, "branch", "record/07-foo")
	if r := rig(t, repo, nil, nil, "push", "record/07-foo"); r.code != 0 {
		t.Fatalf("push record/07-foo: %+v", r)
	}
	if got := gitc(t, remote, "rev-parse", "main"); got != mainBefore {
		t.Fatal("the remote's main moved")
	}
}

func TestPushFromAnotherDirectoryWithWorktree(t *testing.T) {
	repo, remote := newRepo(t)
	wt := filepath.Join(filepath.Dir(repo), "wt")
	gitc(t, repo, "worktree", "add", "-q", wt, "task/07-foo")
	for _, args := range [][]string{{"push", "07", "--worktree", wt}, {"push", "--worktree=" + wt, "task/07-foo"}} {
		if r := rig(t, t.TempDir(), nil, nil, args...); r.code != 0 {
			t.Fatalf("%v: %+v", args, r)
		}
	}
	gitc(t, remote, "rev-parse", "task/07-foo")
}

func TestPushFallsBackToTheBranchWhenTheTaskFileIsGone(t *testing.T) {
	repo, _ := newRepo(t)
	os.Remove(filepath.Join(repo, "docs", "plan", "tasks", "07-foo.md"))
	if r := rig(t, repo, nil, nil, "push", "07"); r.code != 0 {
		t.Fatalf("push 07: %+v", r)
	}
	gitc(t, repo, "branch", "task/07-other")
	if r := rig(t, repo, nil, nil, "push", "07"); r.code != 2 || !strings.Contains(r.errOut, "matches 2 branches") {
		t.Fatalf("ambiguous id: %+v", r)
	}
}

// TestPushRefusals is the refusal table: every case from the "asks" list of
// claude-kit's push-guard tests that can be written as arguments, and more. Each must
// exit 2 before `git push` starts and leave the remote as it was.
func TestPushRefusals(t *testing.T) {
	repo, remote := newRepo(t)
	gitc(t, repo, "branch", "task/x")
	gitc(t, repo, "branch", "codex/143")
	gitc(t, repo, "tag", "v1.2.0")
	gitc(t, repo, "commit", "-q", "--allow-empty", "-m", "unpushed work on main")
	ran := hook(t, repo)
	before := gitc(t, remote, "for-each-ref")

	cases := [][]string{
		{},
		{""},
		{"main"},
		{"HEAD"},
		{"task/x:main"},
		{"07:main"},
		{"task/x", "main"},
		{"upstream", "task/07-foo"},
		{"origin", "task/x"},
		{"task/x", "--force"},
		{"task/x", "-f"},
		{"--force-with-lease", "task/x"},
		{"--force-with-lease=task/x", "task/x"},
		{"--force-if-includes", "task/x"},
		{"+task/x"},
		{"task/x", "--tags"},
		{"task/x", "--follow-tags"},
		{":main"},
		{":task/x"},
		{"--all"},
		{"--mirror"},
		{"--delete", "task/x"},
		{"-d", "task/x"},
		{"--prune", "task/x"},
		{"v1.2.0"},
		{"refs/tags/v1.2.0"},
		{"refs/heads/main"},
		{"task/../main"},
		{"task/..", "main"},
		{"../main"},
		{"07/../main"},
		{"task/$(echo main)"},
		{"task/x;git push origin main"},
		{"task/x main"},
		{"task/x\nmain"},
		{"task/x\x00main"},
		{"codex/143"},
		{"task/*"},
		{"task/x~1"},
		{"task/x^"},
		{"task/x@{u}"},
		{"@"},
		{"task/"},
		{"task"},
		{"/main"},
		{"task//x"},
		{"task/-x"},
		{"task/.x"},
		{"task/x.lock"},
		{"task/x."},
		{"-c", "push.default=current"},
		{"-c", "remote.origin.push=refs/heads/task/x:refs/heads/main", "task/x"},
		{"-u", "task/x"},
		{"-q", "task/x"},
		{"--", "main"},
		{"--", "task/x"},
		{"--repo", "elsewhere", "task/x"},
		{"--receive-pack=evil", "task/x"},
		{"--exec=evil", "task/x"},
		{"-o", "ci.skip", "task/x"},
		{"--no-verify", "task/x"},
		{"--worktree"},
		{"--worktree", filepath.Join(repo, "no-such-dir"), "task/x"},
		{"--worktree", "--force", "task/x"},
		{"--worktree=" + repo, "--force", "task/x"},
		{"task/no-such-branch"},
		{"99"},
	}
	for _, args := range cases {
		r := rig(t, repo, nil, nil, append([]string{"push"}, args...)...)
		if r.code != 2 || !strings.Contains(r.errOut, "rig push: refused: ") || r.out != "" {
			t.Errorf("rig push %q: exit %d, stdout %q, stderr %q; want a refusal, exit 2", args, r.code, r.out, r.errOut)
		}
	}
	if ran() {
		t.Error("git push started for a refused push")
	}
	if after := gitc(t, remote, "for-each-ref"); after != before {
		t.Errorf("the remote changed:\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestPushOutsideARepositoryIsRefused(t *testing.T) {
	if r := rig(t, t.TempDir(), nil, nil, "push", "task/x"); r.code != 2 {
		t.Fatalf("%+v", r)
	}
}

func TestPushPrefixesComeFromConfig(t *testing.T) {
	repo, remote := newRepo(t)
	gitc(t, repo, "branch", "codex/143")
	os.MkdirAll(filepath.Join(repo, ".claude"), 0o755)
	cfg := filepath.Join(repo, ".claude", "rig.json")

	os.WriteFile(cfg, []byte(`{"pushPrefixes":["codex"]}`), 0o644)
	if r := rig(t, repo, nil, nil, "push", "codex/143"); r.code != 0 {
		t.Fatalf("configured prefix: %+v", r)
	}
	if r := rig(t, repo, nil, nil, "push", "task/07-foo"); r.code != 2 {
		t.Fatalf("a prefix left out of the config was pushed: %+v", r)
	}
	// No prefix, however it is written, reaches the default branch.
	mainBefore := gitc(t, remote, "rev-parse", "main")
	gitc(t, repo, "commit", "-q", "--allow-empty", "-m", "more")
	for _, body := range []string{`{"pushPrefixes":["main"]}`, `{"pushPrefixes":["refs"]}`, `{"pushPrefixes":[""]}`, `{"pushPrefixes":["."]}`, `{"pushPrefixes":["*"]}`} {
		os.WriteFile(cfg, []byte(body), 0o644)
		for _, target := range []string{"main", "refs/heads/main", "main/", "/main", "*/main"} {
			if r := rig(t, repo, nil, nil, "push", target); r.code != 2 {
				t.Errorf("config %s: rig push %s: %+v", body, target, r)
			}
		}
	}
	if got := gitc(t, remote, "rev-parse", "main"); got != mainBefore {
		t.Fatal("the remote's main moved")
	}
}

func TestPushRejectedByTheRemoteExits1(t *testing.T) {
	repo, _ := newRepo(t)
	gitc(t, repo, "checkout", "-q", "task/07-foo")
	gitc(t, repo, "commit", "-q", "--allow-empty", "-m", "one")
	if r := rig(t, repo, nil, nil, "push", "07"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	gitc(t, repo, "commit", "-q", "--amend", "--allow-empty", "-m", "rewritten")
	r := rig(t, repo, nil, nil, "push", "07")
	if r.code != 1 || !strings.Contains(r.errOut, "push rejected") {
		t.Fatalf("non-fast-forward: %+v", r)
	}
}

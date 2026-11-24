package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/forge"
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
// an earlier push-guard hook's tests that can be written as arguments, and more. Each must
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

// F4: the refusal for an option never prints what was typed after it.
func TestPushOptionErrorNeverEchoesValue(t *testing.T) {
	repo, _ := newRepo(t)
	for _, args := range [][]string{
		{"--token=ghp_abc", "task/x"},
		{"--ghp_abc", "task/x"},
		{"-ghp_abc", "task/x"},
		{"--force=ghp_abc", "task/x"},
	} {
		r := rig(t, repo, nil, nil, append([]string{"push"}, args...)...)
		if r.code != 2 || strings.Contains(r.errOut+r.out, "ghp_abc") {
			t.Errorf("rig push %q: exit %d, stderr %q", args, r.code, r.errOut)
		}
	}
	r := rig(t, repo, nil, nil, "push", "--token=ghp_abc", "task/x")
	if !strings.Contains(r.errOut, `"--token"`) {
		t.Errorf("known option name not shown: %q", r.errOut)
	}
}

// globalHome writes body as a fresh home's ~/.rig/config.json and returns the
// environment that points rig at it.
func globalHome(t *testing.T, body string) map[string]string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".rig"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".rig", "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return map[string]string{"HOME": home, "USERPROFILE": home}
}

// The harness must not read the real home: a hostile config in the process
// HOME does not reach a push that passes no HOME of its own.
func TestHarnessIgnoresTheProcessHome(t *testing.T) {
	hostile := globalHome(t, `{"push":{"prefixes":["record"]}}`)
	t.Setenv("HOME", hostile["HOME"])
	t.Setenv("USERPROFILE", hostile["USERPROFILE"])
	repo, _ := newRepo(t)
	gitc(t, repo, "checkout", "-q", "task/07-foo")
	if r := rig(t, repo, nil, nil, "push", "task/07-foo"); r.code != 0 {
		t.Fatalf("the harness read the process home: %+v", r)
	}
}

// rig.json is read from the main checkout, not from a linked worktree: a
// worktree's own copy cannot widen the prefixes.
func TestPushPrefixesReadFromTheMainCheckout(t *testing.T) {
	repo, remote := newRepo(t)
	gitc(t, repo, "branch", "release/x")
	wt := filepath.Join(filepath.Dir(repo), "wt-root")
	gitc(t, repo, "worktree", "add", "-q", wt, "task/07-foo")
	os.MkdirAll(filepath.Join(wt, ".claude"), 0o755)
	os.WriteFile(filepath.Join(wt, ".claude", "rig.json"), []byte(`{"pushPrefixes":["release"]}`), 0o644)
	before := gitc(t, remote, "for-each-ref")
	if r := rig(t, wt, nil, nil, "push", "release/x"); r.code != 2 {
		t.Fatalf("the worktree's rig.json was honoured: %+v", r)
	}
	if gitc(t, remote, "for-each-ref") != before {
		t.Fatal("release/x reached the remote")
	}
}

// In a bare layout the worktree's own top is the project root, as for rig
// config (0.5.0): its rig.json narrows pushPrefixes and selects the forge.
func TestPushPrefixesFromAWorktreeOfABareRepo(t *testing.T) {
	repo, remote := newRepo(t)
	bare := filepath.Join(filepath.Dir(repo), "bare.git")
	gitc(t, filepath.Dir(repo), "clone", "-q", "--bare", repo, bare)
	gitc(t, bare, "remote", "set-url", "origin", remote)
	gitc(t, bare, "branch", "record/x", "main")
	wt := filepath.Join(filepath.Dir(repo), "wt-bare")
	gitc(t, bare, "worktree", "add", "-q", wt, "task/07-foo")
	mustMkdir(t, filepath.Join(wt, ".claude"))
	mustWrite(t, filepath.Join(wt, ".claude", "rig.json"), `{"pushPrefixes":["task"],"forge":"gitlab"}`)
	before := gitc(t, remote, "for-each-ref")
	if r := rig(t, t.TempDir(), nil, nil, "push", "record/x", "--worktree", wt); r.code != 2 {
		t.Fatalf("the worktree's rig.json was not honoured: %+v", r)
	}
	if gitc(t, remote, "for-each-ref") != before {
		t.Fatal("record/x reached the remote")
	}
	if k, err := forgeKind(context.Background(), wt, func() (string, error) { return t.TempDir(), nil }); err != nil || k != forge.GitLab {
		t.Fatalf("forge = %q, %v; want gitlab", k, err)
	}
}

func TestPushPrefixesReadThroughTheLayers(t *testing.T) {
	repo, remote := newRepo(t)
	gitc(t, repo, "branch", "record/x")
	gitc(t, repo, "branch", "task/x")
	before := gitc(t, remote, "for-each-ref")

	// The global file narrows the prefixes to record.
	env := globalHome(t, `{"push":{"prefixes":["record"]}}`)
	ran := hook(t, repo)
	if r := rig(t, repo, nil, env, "push", "task/x"); r.code != 2 {
		t.Fatalf("global narrowing ignored: %+v", r)
	}
	if ran() || gitc(t, remote, "for-each-ref") != before {
		t.Fatal("task/x reached the remote")
	}
	if r := rig(t, repo, nil, env, "push", "record/x"); r.code != 0 {
		t.Fatalf("record/x: %+v", r)
	}

	// A per-repository section narrows it too. The origin is a URL with a repo
	// key; pushInsteadOf sends the push to the local bare remote.
	gitc(t, repo, "remote", "set-url", "origin", "https://example.com/o/r.git")
	gitc(t, repo, "config", "url."+remote+".pushInsteadOf", "https://example.com/o/r.git")
	env = globalHome(t, `{"repos":{"example.com/o/r":{"push":{"prefixes":["record"]}}}}`)
	if r := rig(t, repo, nil, env, "push", "task/x"); r.code != 2 {
		t.Fatalf("repos narrowing ignored: %+v", r)
	}
	if got := gitc(t, remote, "for-each-ref"); strings.Contains(got, "refs/heads/task/x") {
		t.Fatal("task/x reached the remote")
	}

	// rig.json still wins over the global file.
	os.MkdirAll(filepath.Join(repo, ".claude"), 0o755)
	os.WriteFile(filepath.Join(repo, ".claude", "rig.json"), []byte(`{"pushPrefixes":["task"]}`), 0o644)
	if r := rig(t, repo, nil, env, "push", "task/x"); r.code != 0 {
		t.Fatalf("rig.json should win: %+v", r)
	}
}

func TestPushPrefixesNeverFromTheLocalFileAndDefaultStays(t *testing.T) {
	repo, _ := newRepo(t)
	gitc(t, repo, "branch", "record/x")
	gitc(t, repo, "branch", "task/x")
	env := globalHome(t, `{}`)
	os.MkdirAll(filepath.Join(repo, ".rig"), 0o755)
	os.WriteFile(filepath.Join(repo, ".rig", "config.json"), []byte(`{"push":{"prefixes":["codex"]}}`), 0o644)
	for _, b := range []string{"task/x", "record/x"} {
		if r := rig(t, repo, nil, env, "push", b); r.code != 0 {
			t.Fatalf("%s: %+v", b, r)
		}
	}
}

// With no home directory rig push fails closed: its policy may live in the
// global file, which cannot be read.
func TestPushWithoutHomeFailsClosed(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	repo, remote := newRepo(t)
	before := gitc(t, remote, "for-each-ref")
	var out, errOut bytes.Buffer
	a := &app{stdout: &out, stderr: &errOut, cwd: repo, getenv: func(string) string { return "" }}
	if code := a.run(ctx, []string{"push", "task/07-foo"}); code != 2 {
		t.Fatalf("exit %d: %s%s", code, out.String(), errOut.String())
	}
	if gitc(t, remote, "for-each-ref") != before {
		t.Fatal("a push reached the remote")
	}
}

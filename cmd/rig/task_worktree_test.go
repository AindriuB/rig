package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/git"
)

// isolate gives the process a git identity, no user git config and an empty
// home directory, so config and merges behave the same everywhere.
func isolate(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	for k, v := range map[string]string{
		"HOME": home, "USERPROFILE": home,
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
		"GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com",
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_SYSTEM": os.DevNull,
	} {
		t.Setenv(k, v)
	}
	return home
}

func writeGlobalConfig(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".rig")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func taskPath(repo, slug string) string {
	return filepath.Join(repo, "docs", "plan", "tasks", slug+".md")
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// AC1: worktree, branch, base and marker, and the four output lines.
func TestTaskStart(t *testing.T) {
	isolate(t)
	repo, _ := newRepo(t)
	writeTask(t, repo, "08-bar.md")
	main := gitc(t, repo, "rev-parse", "main")

	r := rig(t, repo, nil, nil, "task", "start", "08-bar")
	wt := filepath.Join(repo, ".worktrees", "repo", "08-bar")
	tf := filepath.ToSlash(taskPath(repo, "08-bar"))
	want := "WORKTREE: " + wt + "\nBRANCH:   task/08-bar\nBASE:     main\nOWNS:     enforced from " + tf + "\n"
	if r.code != 0 || r.out != want || r.errOut != "" {
		t.Fatalf("start: %+v\nwant out %q", r, want)
	}
	if got := gitc(t, wt, "rev-parse", "--abbrev-ref", "HEAD"); got != "task/08-bar" {
		t.Fatalf("worktree branch = %q", got)
	}
	if got := gitc(t, wt, "rev-parse", "HEAD"); got != main {
		t.Fatalf("worktree starts at %s, want main %s", got, main)
	}
	gitDir := gitc(t, wt, "rev-parse", "--absolute-git-dir")
	got, err := os.ReadFile(filepath.Join(gitDir, "kit-task"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != tf+"\n" {
		t.Fatalf("marker = %q, want %q", got, tf+"\n")
	}
	if strings.Contains(string(got), `\`) {
		t.Fatalf("marker has a backslash: %q", got)
	}
	if out := gitc(t, wt, "status", "--porcelain"); out != "" {
		t.Fatalf("the marker shows in status: %q", out)
	}
}

func TestTaskStartJSONAndRepoFlag(t *testing.T) {
	isolate(t)
	repo, _ := newRepo(t)
	writeTask(t, repo, "08-bar.md")
	r := rig(t, t.TempDir(), nil, nil, "task", "start", "08-bar", "--json", "--repo", repo)
	if r.code != 0 {
		t.Fatalf("start: %+v", r)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(r.out), &got); err != nil {
		t.Fatalf("%v: %q", err, r.out)
	}
	want := map[string]string{
		"worktree": filepath.Join(repo, ".worktrees", "repo", "08-bar"),
		"branch":   "task/08-bar", "base": "main",
		"taskFile": filepath.ToSlash(taskPath(repo, "08-bar")),
	}
	if len(got) != len(want) {
		t.Fatalf("json = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("json[%s] = %q, want %q", k, got[k], v)
		}
	}
}

// AC2: the task file lookup and the refusals.
func TestTaskStartFindsTheWorkspaceTaskFile(t *testing.T) {
	isolate(t)
	ws, child, _ := newWorkspace(t)
	writeTask(t, ws, "09-ws.md")
	r := rig(t, child, nil, nil, "task", "start", "09-ws")
	wt := filepath.Join(child, ".worktrees", "code", "09-ws")
	tf := filepath.ToSlash(taskPath(ws, "09-ws"))
	if r.code != 0 || !strings.Contains(r.out, "WORKTREE: "+wt+"\n") || !strings.Contains(r.out, "OWNS:     enforced from "+tf+"\n") {
		t.Fatalf("start: %+v", r)
	}
	gitDir := gitc(t, wt, "rev-parse", "--absolute-git-dir")
	if b, _ := os.ReadFile(filepath.Join(gitDir, "kit-task")); string(b) != tf+"\n" {
		t.Fatalf("marker = %q", b)
	}
}

func TestTaskStartPrefersTheRepoTaskFile(t *testing.T) {
	isolate(t)
	ws, child, _ := newWorkspace(t)
	writeTask(t, ws, "09-ws.md")
	writeTask(t, child, "09-ws.md")
	r := rig(t, child, nil, nil, "task", "start", "09-ws")
	if tf := filepath.ToSlash(taskPath(child, "09-ws")); r.code != 0 || !strings.Contains(r.out, "enforced from "+tf+"\n") {
		t.Fatalf("start: %+v", r)
	}
}

func TestTaskStartTaskFileOverride(t *testing.T) {
	isolate(t)
	repo, _ := newRepo(t)
	other := filepath.Join(t.TempDir(), "x.md")
	if err := os.WriteFile(other, []byte("# x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := rig(t, repo, nil, nil, "task", "start", "zz-1", "--task-file", other)
	if r.code != 0 || !strings.Contains(r.out, "enforced from "+filepath.ToSlash(other)+"\n") {
		t.Fatalf("start: %+v", r)
	}
	wt := filepath.Join(repo, ".worktrees", "repo", "zz-1")
	gitDir := gitc(t, wt, "rev-parse", "--absolute-git-dir")
	if b, _ := os.ReadFile(filepath.Join(gitDir, "kit-task")); string(b) != filepath.ToSlash(other)+"\n" {
		t.Fatalf("marker = %q", b)
	}
	// An override that is not a file is refused, and nothing is made.
	r = rig(t, repo, nil, nil, "task", "start", "zz-2", "--task-file", filepath.Join(t.TempDir(), "missing.md"))
	if r.code != 1 || exists(filepath.Join(repo, ".worktrees", "repo", "zz-2")) {
		t.Fatalf("missing override: %+v", r)
	}
	if ok := gitc(t, repo, "branch", "--list", "task/zz-2"); ok != "" {
		t.Fatalf("branch made: %q", ok)
	}
}

func TestTaskStartRefusals(t *testing.T) {
	isolate(t)
	repo, _ := newRepo(t)
	writeTask(t, repo, "08-bar.md")
	writeTask(t, repo, "08-baz.md")

	// No task file: exit 1, nothing created.
	r := rig(t, repo, nil, nil, "task", "start", "nope-1")
	if r.code != 1 || r.out != "" || !strings.Contains(r.errOut, "nope-1.md") {
		t.Fatalf("no task file: %+v", r)
	}
	if exists(filepath.Join(repo, ".worktrees")) || gitc(t, repo, "branch", "--list", "task/nope-1") != "" {
		t.Fatal("no task file: something was created")
	}
	// An exact name is needed: 08 is not 08-bar.
	if r := rig(t, repo, nil, nil, "task", "start", "08"); r.code != 1 {
		t.Fatalf("id only: %+v", r)
	}

	// The path exists.
	wt := filepath.Join(repo, ".worktrees", "repo", "08-bar")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	r = rig(t, repo, nil, nil, "task", "start", "08-bar")
	if r.code != 1 || !strings.Contains(r.errOut, "already exists") {
		t.Fatalf("existing path: %+v", r)
	}
	if gitc(t, repo, "branch", "--list", "task/08-bar") != "" {
		t.Fatal("existing path: a branch was made")
	}

	// The branch exists.
	gitc(t, repo, "branch", "task/08-baz")
	r = rig(t, repo, nil, nil, "task", "start", "08-baz")
	if r.code != 1 || !strings.Contains(r.errOut, "task/08-baz") {
		t.Fatalf("existing branch: %+v", r)
	}
	if exists(filepath.Join(repo, ".worktrees", "repo", "08-baz")) {
		t.Fatal("existing branch: a worktree was made")
	}
}

func TestTaskStartBadSlugNeverRunsGit(t *testing.T) {
	isolate(t)
	notRepo := t.TempDir() // a git call here would fail with "not a git repository"
	for _, slug := range []string{"../x", "a/b", `a\b`, "a:b", "..", "a..b", ""} {
		r := rig(t, notRepo, nil, nil, "task", "start", slug)
		if r.code != 2 || !strings.Contains(r.errOut, "not a task id") {
			t.Errorf("slug %q: %+v", slug, r)
		}
	}
}

// AC1: a marker that cannot be written leaves no worktree and no branch.
func TestTaskStartRemovesWhatItMadeWhenTheMarkerFails(t *testing.T) {
	isolate(t)
	repo, _ := newRepo(t)
	writeTask(t, repo, "08-bar.md")
	old := writeMarker
	writeMarker = func(string, []byte, os.FileMode) error { return os.ErrPermission }
	defer func() { writeMarker = old }()

	r := rig(t, repo, nil, nil, "task", "start", "08-bar")
	if r.code != 2 || r.out != "" || !strings.Contains(r.errOut, "marker") {
		t.Fatalf("start: %+v", r)
	}
	if exists(filepath.Join(repo, ".worktrees", "repo", "08-bar")) {
		t.Fatal("the worktree was left behind")
	}
	if gitc(t, repo, "branch", "--list", "task/08-bar") != "" {
		t.Fatal("the branch was left behind")
	}
	if got := gitc(t, repo, "worktree", "list"); strings.Count(got, "\n") != 0 {
		t.Fatalf("worktrees: %s", got)
	}
}

// AC3: worktrees.root.
func TestTaskStartWorktreesRoot(t *testing.T) {
	home := isolate(t)
	repo, _ := newRepo(t)
	writeTask(t, repo, "08-bar.md")
	writeTask(t, repo, "08-baz.md")
	writeTask(t, repo, "08-qux.md")

	// Default: .worktrees in the repo (see TestTaskStart). Relative:
	writeGlobalConfig(t, home, `{"worktrees":{"root":"wt"}}`)
	r := rig(t, repo, nil, nil, "task", "start", "08-bar")
	if want := filepath.Join(repo, "wt", "repo", "08-bar"); r.code != 0 || !strings.HasPrefix(r.out, "WORKTREE: "+want+"\n") {
		t.Fatalf("relative root: %+v, want %s", r, want)
	}
	// Absolute, as given.
	abs := t.TempDir()
	writeGlobalConfig(t, home, `{"worktrees":{"root":`+jsonString(abs)+`}}`)
	r = rig(t, repo, nil, nil, "task", "start", "08-baz")
	if want := filepath.Join(abs, "repo", "08-baz"); r.code != 0 || !strings.HasPrefix(r.out, "WORKTREE: "+want+"\n") {
		t.Fatalf("absolute root: %+v, want %s", r, want)
	}
	if !exists(filepath.Join(abs, "repo", "08-baz", ".git")) {
		t.Fatal("no worktree at the absolute root")
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// started makes a task worktree through rig and returns its path.
func started(t *testing.T, repo, slug string) string {
	t.Helper()
	writeTask(t, repo, slug+".md")
	if r := rig(t, repo, nil, nil, "task", "start", slug); r.code != 0 {
		t.Fatalf("start %s: %+v", slug, r)
	}
	return filepath.Join(repo, ".worktrees", filepath.Base(repo), slug)
}

// commitIn commits one new file in wt and returns the new head.
func commitIn(t *testing.T, wt, name, body string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wt, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc(t, wt, "add", name)
	gitc(t, wt, "commit", "-q", "-m", "work on "+name)
	return gitc(t, wt, "rev-parse", "HEAD")
}

// AC4: the happy path.
func TestTaskFinish(t *testing.T) {
	isolate(t)
	repo, _ := newRepo(t)
	wt := started(t, repo, "08-bar")
	sha := commitIn(t, wt, "a.txt", "a\n")
	before := gitc(t, repo, "rev-parse", "main")

	r := rig(t, repo, nil, nil, "task", "finish", "08-bar", "--sha", sha)
	want := "MERGED: task/08-bar @ " + sha[:12] + " -> main\nREMOVED: " + wt + "\n"
	if r.code != 0 || r.out != want || r.errOut != "" {
		t.Fatalf("finish: %+v\nwant out %q", r, want)
	}
	if got := gitc(t, repo, "rev-parse", "main^1"); got != before {
		t.Fatalf("first parent = %s, want %s", got, before)
	}
	if got := gitc(t, repo, "rev-parse", "main^2"); got != sha {
		t.Fatalf("second parent = %s, want %s", got, sha)
	}
	if got, want := gitc(t, repo, "log", "-1", "--format=%s", "main"), "merge 08-bar: task/08-bar @ "+sha[:12]; got != want {
		t.Fatalf("subject = %q, want %q", got, want)
	}
	if exists(wt) {
		t.Fatal("worktree still there")
	}
	if gitc(t, repo, "branch", "--list", "task/08-bar") != "" {
		t.Fatal("branch still there")
	}
	if got := gitc(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Fatalf("main checkout is on %q", got)
	}
}

func TestTaskFinishNameForms(t *testing.T) {
	isolate(t)
	repo, _ := newRepo(t)
	for _, tc := range []struct{ slug, arg string }{
		{"08-bar", "task/08-bar"},
		{"09-baz", "09"}, // a unique <id>- prefix
	} {
		wt := started(t, repo, tc.slug)
		sha := commitIn(t, wt, tc.slug+".txt", "x\n")
		r := rig(t, repo, nil, nil, "task", "finish", tc.arg, "--sha", sha)
		if r.code != 0 || !strings.Contains(r.out, "MERGED: task/"+tc.slug+" @ ") {
			t.Fatalf("finish %s: %+v", tc.arg, r)
		}
		wantMsg := "merge " + strings.TrimPrefix(tc.arg, "task/") + ": task/" + tc.slug + " @ " + sha[:12]
		if got := gitc(t, repo, "log", "-1", "--format=%s", "main"); got != wantMsg {
			t.Errorf("subject = %q, want %q", got, wantMsg)
		}
	}
}

func TestTaskFinishJSON(t *testing.T) {
	isolate(t)
	repo, _ := newRepo(t)
	wt := started(t, repo, "08-bar")
	sha := commitIn(t, wt, "a.txt", "a\n")
	r := rig(t, repo, nil, nil, "task", "finish", "08-bar", "--sha", sha, "--json")
	var got struct {
		Merged  struct{ Branch, Sha, Base string }
		Removed string
		Kept    *struct{ Worktree, Reason string }
	}
	if err := json.Unmarshal([]byte(r.out), &got); err != nil || r.code != 0 {
		t.Fatalf("%v: %+v", err, r)
	}
	if got.Merged.Branch != "task/08-bar" || got.Merged.Sha != sha || got.Merged.Base != "main" || got.Removed != wt || got.Kept != nil {
		t.Fatalf("json = %+v", got)
	}
}

// AC5: every refusal is exit 1 and changes nothing.
func TestTaskFinishRefusals(t *testing.T) {
	isolate(t)
	repo, _ := newRepo(t)
	wt := started(t, repo, "08-bar")
	sha := commitIn(t, wt, "a.txt", "a\n")
	mainHead := gitc(t, repo, "rev-parse", "main")

	unchanged := func(t *testing.T, what string) {
		t.Helper()
		if got := gitc(t, repo, "rev-parse", "main"); got != mainHead {
			t.Fatalf("%s: main moved", what)
		}
		if !exists(wt) || gitc(t, repo, "branch", "--list", "task/08-bar") == "" {
			t.Fatalf("%s: the worktree or branch is gone", what)
		}
	}
	finish := func(args ...string) result {
		return rig(t, repo, nil, nil, append([]string{"task", "finish"}, args...)...)
	}

	// Dirty worktree.
	if err := os.WriteFile(filepath.Join(wt, "dirty"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := finish("08-bar", "--sha", sha); r.code != 1 || !strings.Contains(r.errOut, "uncommitted") {
		t.Fatalf("dirty: %+v", r)
	}
	unchanged(t, "dirty")
	os.Remove(filepath.Join(wt, "dirty"))

	// A sha that is not a commit.
	for _, bad := range []string{"deadbeefdeadbeef", "--all", ""} {
		if r := finish("08-bar", "--sha", bad); r.code != 1 && r.code != 2 {
			t.Fatalf("sha %q: %+v", bad, r)
		}
	}
	if r := finish("08-bar", "--sha", "deadbeefdeadbeef"); r.code != 1 || !strings.Contains(r.errOut, "not a commit") {
		t.Fatalf("unknown sha: %+v", r)
	}
	unchanged(t, "unknown sha")

	// The branch head is not the sha.
	if r := finish("08-bar", "--sha", mainHead); r.code != 1 || !strings.Contains(r.errOut, "untested") {
		t.Fatalf("head != sha: %+v", r)
	}
	unchanged(t, "head != sha")

	// No such worktree.
	if r := finish("99-none", "--sha", sha); r.code != 1 {
		t.Fatalf("no worktree: %+v", r)
	}
	unchanged(t, "no worktree")
}

func TestTaskFinishAmbiguousPrefix(t *testing.T) {
	isolate(t)
	repo, _ := newRepo(t)
	a := started(t, repo, "08-a")
	b := started(t, repo, "08-b")
	sha := commitIn(t, a, "a.txt", "a\n")
	mainHead := gitc(t, repo, "rev-parse", "main")
	r := rig(t, repo, nil, nil, "task", "finish", "08", "--sha", sha)
	if r.code != 1 || !strings.Contains(r.errOut, a) || !strings.Contains(r.errOut, b) {
		t.Fatalf("ambiguous: %+v", r)
	}
	if gitc(t, repo, "rev-parse", "main") != mainHead || !exists(a) || !exists(b) {
		t.Fatal("ambiguous prefix changed something")
	}
	// An exact name wins over a prefix match.
	if r := rig(t, repo, nil, nil, "task", "finish", "08-a", "--sha", sha); r.code != 0 {
		t.Fatalf("exact: %+v", r)
	}
}

func TestTaskFinishConflictIsAborted(t *testing.T) {
	isolate(t)
	repo, _ := newRepo(t)
	wt := started(t, repo, "08-bar")
	sha := commitIn(t, wt, "f.txt", "branch\n")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc(t, repo, "add", "f.txt")
	gitc(t, repo, "commit", "-q", "-m", "main change")
	mainHead := gitc(t, repo, "rev-parse", "main")

	r := rig(t, repo, nil, nil, "task", "finish", "08-bar", "--sha", sha)
	if r.code != 1 || !strings.Contains(r.errOut, "conflict") || r.out != "" {
		t.Fatalf("conflict: %+v", r)
	}
	if gitc(t, repo, "rev-parse", "main") != mainHead {
		t.Fatal("main moved")
	}
	if out := gitc(t, repo, "status", "--porcelain", "--untracked-files=no"); out != "" {
		t.Fatalf("main checkout left dirty: %q", out)
	}
	if !exists(wt) || gitc(t, repo, "branch", "--list", "task/08-bar") == "" {
		t.Fatal("worktree or branch removed")
	}
}

// AC6: a worktree that will not go is kept, after a merge that happened.
func TestTaskFinishKeepsALockedWorktree(t *testing.T) {
	isolate(t)
	repo, _ := newRepo(t)
	wt := started(t, repo, "08-bar")
	sha := commitIn(t, wt, "a.txt", "a\n")
	gitc(t, repo, "worktree", "lock", wt)

	r := rig(t, repo, nil, nil, "task", "finish", "08-bar", "--sha", sha)
	head := "MERGED: task/08-bar @ " + sha[:12] + " -> main\nKEPT:   " + wt + " (worktree remove failed: "
	tail := "; close programs using it, then git worktree remove " + wt + ")\n"
	if r.code != 0 || !strings.HasPrefix(r.out, head) || !strings.HasSuffix(r.out, tail) || !strings.Contains(r.out, "locked") {
		t.Fatalf("finish: %+v", r)
	}
	if strings.Count(r.out, "\n") != 2 {
		t.Fatalf("reason spans lines: %q", r.out)
	}
	if got := gitc(t, repo, "rev-parse", "main^2"); got != sha {
		t.Fatal("not merged")
	}
	if !exists(wt) || gitc(t, repo, "branch", "--list", "task/08-bar") == "" {
		t.Fatal("worktree or branch removed")
	}
}

func TestTaskFinishKeptJSON(t *testing.T) {
	isolate(t)
	repo, _ := newRepo(t)
	wt := started(t, repo, "08-bar")
	sha := commitIn(t, wt, "a.txt", "a\n")
	gitc(t, repo, "worktree", "lock", wt)
	r := rig(t, repo, nil, nil, "task", "finish", "08-bar", "--sha", sha, "--json")
	var got struct {
		Merged  struct{ Branch string }
		Removed string
		Kept    *struct{ Worktree, Reason string }
	}
	if err := json.Unmarshal([]byte(r.out), &got); err != nil || r.code != 0 {
		t.Fatalf("%v: %+v", err, r)
	}
	if got.Merged.Branch != "task/08-bar" || got.Removed != "" || got.Kept == nil || got.Kept.Worktree != wt || !strings.Contains(got.Kept.Reason, "locked") {
		t.Fatalf("json = %+v", got)
	}
}

// The branch moves after the merge starts: the merge is reported, the extra
// commit is not merged, and both worktree and branch stay.
func TestTaskFinishKeepsABranchThatMoved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the post-merge hook is a /bin/sh script")
	}
	isolate(t)
	repo, _ := newRepo(t)
	wt := started(t, repo, "08-bar")
	sha := commitIn(t, wt, "a.txt", "a\n")
	hook := "#!/bin/sh\nunset GIT_DIR GIT_INDEX_FILE GIT_WORK_TREE\ngit -C '" + wt + "' commit -q --allow-empty -m moved\n"
	hookPath := filepath.Join(repo, ".git", "hooks", "post-merge")
	if err := os.WriteFile(hookPath, []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	r := rig(t, repo, nil, nil, "task", "finish", "08-bar", "--sha", sha)
	want := "MERGED: task/08-bar @ " + sha[:12] + " -> main\nKEPT:   " + wt + " (branch moved during the merge; its new commits are not merged)\n"
	if r.code != 0 || r.out != want {
		t.Fatalf("finish: %+v\nwant %q", r, want)
	}
	if !exists(wt) || gitc(t, repo, "rev-parse", "task/08-bar") == sha {
		t.Fatal("the worktree is gone or the branch did not move")
	}
}

// F1: a worktree add that git half-completes (the post-checkout hook fails
// after the branch and worktree exist) is rolled back, never left unguarded.
func TestTaskStartRollsBackWhenWorktreeAddFailsAfterGitMadeIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a /bin/sh hook")
	}
	isolate(t)
	repo, _ := newRepo(t)
	writeTask(t, repo, "08-bar.md")
	hook := filepath.Join(repo, ".git", "hooks", "post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := rig(t, repo, nil, nil, "task", "start", "08-bar")
	if r.code != 2 || r.out != "" {
		t.Fatalf("start: %+v", r)
	}
	if exists(filepath.Join(repo, ".worktrees", "repo", "08-bar")) {
		t.Fatal("the worktree was left behind")
	}
	if gitc(t, repo, "branch", "--list", "task/08-bar") != "" {
		t.Fatal("the branch was left behind")
	}
	if got := gitc(t, repo, "worktree", "list"); strings.Count(got, "\n") != 0 {
		t.Fatalf("worktrees: %s", got)
	}
}

// F3: a project file cannot send worktrees outside the checkout.
func TestTaskStartRefusesAProjectRootOutsideTheCheckout(t *testing.T) {
	home := isolate(t)
	repo, _ := newRepo(t)
	writeTask(t, repo, "08-bar.md")
	outside := filepath.Join(t.TempDir(), "x")
	for _, v := range []string{"../../x", outside} {
		if err := os.MkdirAll(filepath.Join(repo, ".rig"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, ".rig", "config.json"), []byte(`{"worktrees":{"root":`+jsonString(v)+`}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		r := rig(t, repo, nil, nil, "task", "start", "08-bar")
		if r.code != 2 || r.out != "" || !strings.Contains(r.errOut, filepath.Join(repo, ".rig", "config.json")) {
			t.Fatalf("root %q: %+v", v, r)
		}
		if gitc(t, repo, "branch", "--list", "task/08-bar") != "" || exists(outside) || exists(filepath.Join(repo, "..", "..", "x")) {
			t.Fatalf("root %q: something was created", v)
		}
	}
	// The same absolute path from the user's own global file is honoured.
	os.Remove(filepath.Join(repo, ".rig", "config.json"))
	writeGlobalConfig(t, home, `{"worktrees":{"root":`+jsonString(outside)+`}}`)
	if r := rig(t, repo, nil, nil, "task", "start", "08-bar"); r.code != 0 {
		t.Fatalf("global absolute root: %+v", r)
	}
}

// G1: a start that loses the race for the same slug must not undo the winner.
func TestTaskStartLosingARaceLeavesTheWinnersWorktreeAndBranch(t *testing.T) {
	isolate(t)
	repo, _ := newRepo(t)
	writeTask(t, repo, "08-bar.md")
	wt := filepath.Join(repo, ".worktrees", "repo", "08-bar")
	beforeWorktreeAdd = func() {
		beforeWorktreeAdd = func() {}
		gitc(t, repo, "worktree", "add", "-q", "-b", "task/08-bar", wt, "main")
	}
	t.Cleanup(func() { beforeWorktreeAdd = func() {} })

	r := rig(t, repo, nil, nil, "task", "start", "08-bar")
	if r.code != 2 {
		t.Fatalf("start: %+v", r)
	}
	if !exists(wt) || gitc(t, repo, "branch", "--list", "task/08-bar") == "" {
		t.Fatalf("the winner's worktree or branch was removed: %+v", r)
	}
	if strings.Contains(r.errOut, "removed again") {
		t.Fatalf("message claims a removal: %s", r.errOut)
	}
}

// G2: a project root that reaches outside through a symlink is refused.
func TestTaskStartRefusesAProjectRootThroughASymlink(t *testing.T) {
	isolate(t)
	repo, _ := newRepo(t)
	writeTask(t, repo, "08-bar.md")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, "wt")); err != nil {
		t.Skipf("cannot make symlinks: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".rig"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".rig", "config.json"), []byte(`{"worktrees":{"root":"wt"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r := rig(t, repo, nil, nil, "task", "start", "08-bar")
	if r.code != 2 || r.out != "" {
		t.Fatalf("start: %+v", r)
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("something was created outside: %v", ents)
	}
	if gitc(t, repo, "branch", "--list", "task/08-bar") != "" {
		t.Fatal("the branch was created")
	}
}

// G3: the rollback message states only what it removed.
func TestRollBackMessageStatesOnlyWhatItRemoved(t *testing.T) {
	isolate(t)
	repo, _ := newRepo(t)
	g := git.Repo{Dir: repo}
	cause := errors.New("boom")
	if got := rollBack(context.Background(), g, filepath.Join(repo, "nope"), "task/none", "main", false, false, cause); strings.Contains(got, "removed again") {
		t.Fatalf("nothing was removed, but: %s", got)
	}
}

// finishDirtyEnv starts a task with one commit and returns what finish needs.
func finishDirtyEnv(t *testing.T) (repo, wt, sha string) {
	t.Helper()
	isolate(t)
	repo, _ = newRepo(t)
	wt = started(t, repo, "08-bar")
	sha = commitIn(t, wt, "a.txt", "a\n")
	return repo, wt, sha
}

// A tracked edit in the main checkout refuses finish before any checkout or merge.
func TestTaskFinishRefusesATrackedEditInTheMainCheckout(t *testing.T) {
	repo, wt, sha := finishDirtyEnv(t)
	files := strings.Fields(gitc(t, repo, "ls-files"))
	if len(files) == 0 {
		t.Fatal("no tracked file")
	}
	edited := filepath.Join(repo, files[0])
	if err := os.WriteFile(edited, []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	head := gitc(t, repo, "rev-parse", "HEAD")
	r := rig(t, repo, nil, nil, "task", "finish", "08-bar", "--sha", sha)
	if r.code != 1 || !strings.Contains(r.errOut, "uncommitted") {
		t.Fatalf("finish: %+v", r)
	}
	if got := gitc(t, repo, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD moved: %s -> %s", head, got)
	}
	if b, err := os.ReadFile(edited); err != nil || string(b) != "edited\n" {
		t.Fatalf("edit lost: %q %v", b, err)
	}
	if !exists(wt) || gitc(t, repo, "branch", "--list", "task/08-bar") == "" {
		t.Fatal("worktree or branch removed")
	}
}

// A merge in progress in the main checkout refuses finish; an untracked file does not.
func TestTaskFinishRefusesAnOperationInProgress(t *testing.T) {
	repo, wt, sha := finishDirtyEnv(t)
	mh := filepath.Join(repo, ".git", "MERGE_HEAD")
	if err := os.WriteFile(mh, []byte(gitc(t, repo, "rev-parse", "HEAD")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := rig(t, repo, nil, nil, "task", "finish", "08-bar", "--sha", sha)
	if r.code != 1 || !strings.Contains(r.errOut, "in progress") {
		t.Fatalf("finish: %+v", r)
	}
	if !exists(mh) || !exists(wt) {
		t.Fatal("state changed")
	}
	if err := os.Remove(mh); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("u\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := rig(t, repo, nil, nil, "task", "finish", "08-bar", "--sha", sha); r.code != 0 {
		t.Fatalf("untracked file blocked finish: %+v", r)
	}
}

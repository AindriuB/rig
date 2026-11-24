package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/AindriuB/rig/internal/forge"
	"github.com/AindriuB/rig/internal/forge/fake"
	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/tracker"
)

func TestStatus(t *testing.T) {
	repo, _ := newRepo(t)
	wt := filepath.Join(filepath.Dir(repo), "wt")
	gitc(t, repo, "worktree", "add", "-q", wt, "task/07-foo")
	gitc(t, wt, "commit", "-q", "--allow-empty", "-m", "work")
	os.WriteFile(filepath.Join(wt, "scratch.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(repo, ".git", handoffFile), []byte("Next: verify 07\nThen record\n"), 0o644)

	f := prs()
	// From inside the worktree: the task files and handoff still come from
	// the main checkout and the common git dir.
	r := rig(t, wt, f, nil, "status")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	for _, want := range []string{
		"HANDOFF from the last session (",
		"  Next: verify 07\n  Then record\n",
		"OPEN TASKS (1):\n  07-foo\n",
		// git prints worktree paths with forward slashes, on Windows too.
		"WORKTREES (1):\n  task/07-foo  ahead 1  dirty 1  " + filepath.ToSlash(wt) + "\n",
		"OPEN PRS (2):\n  #12  red  task/07-foo  Add parser  conflict  draft\n",
	} {
		if !strings.Contains(r.out, want) {
			t.Errorf("missing %q in:\n%s", want, r.out)
		}
	}
	if f.Calls[0] != "ListPRs {State: Head: Mine:true Limit:15}" {
		t.Fatalf("calls = %q", f.Calls)
	}

	var got statusOut
	r = rig(t, repo, f, nil, "status", "--json")
	if err := json.Unmarshal([]byte(r.out), &got); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	if got.Handoff == nil || len(got.Handoff.Lines) != 2 || len(got.Tasks) != 1 || len(got.Worktrees) != 1 ||
		got.Worktrees[0].Ahead != 1 || got.Worktrees[0].Dirty != 1 || len(got.PRs) != 2 || got.PRs[0].CI != forge.CIRed {
		t.Fatalf("json = %+v", got)
	}
}

func TestStatusSurvivesAnUnreachableForgeAndAStaleHandoff(t *testing.T) {
	repo, _ := newRepo(t)
	handoff := filepath.Join(repo, ".git", handoffFile)
	os.WriteFile(handoff, []byte("old news\n"), 0o644)
	old := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(handoff, old, old)

	r := rig(t, repo, &fake.Forge{Err: errors.New("gh not found on PATH")}, nil, "status")
	if r.code != 0 || !strings.Contains(r.out, "OPEN PRS: unavailable (gh not found on PATH)") {
		t.Fatalf("%+v", r)
	}
	if strings.Contains(r.out, "HANDOFF") {
		t.Fatalf("an 8-day-old handoff was shown:\n%s", r.out)
	}
	if r := rig(t, repo, prs(), nil, "status", "--verbose"); r.code != 2 {
		t.Fatalf("unknown flag: %+v", r)
	}
	if r := rig(t, t.TempDir(), &fake.Forge{}, nil, "status"); r.code != 0 || !strings.Contains(r.out, "Not in a git repository.") {
		t.Fatalf("outside a repo: %+v", r)
	}
}

func TestForgeKind(t *testing.T) {
	repo, _ := newRepo(t)
	set := func(url string) { gitc(t, repo, "remote", "set-url", "origin", url) }
	cfg := filepath.Join(repo, ".claude", "rig.json")
	os.MkdirAll(filepath.Dir(cfg), 0o755)

	if k, err := forgeKind(ctx, t.TempDir()); err != nil || k != forge.GitHub {
		t.Fatalf("outside a repo: %q, %v", k, err)
	}
	if k, err := forgeKind(ctx, repo); err != nil || k != forge.GitHub {
		t.Fatalf("local-path origin: %q, %v", k, err)
	}
	set("git@github.com:o/r.git")
	if k, err := forgeKind(ctx, repo); err != nil || k != forge.GitHub {
		t.Fatalf("github origin: %q, %v", k, err)
	}
	set("https://gitlab.com/o/r.git")
	if k, err := forgeKind(ctx, repo); err != nil || k != forge.GitLab {
		t.Fatalf("gitlab origin: %q, %v", k, err)
	}
	set("git@git.example.com:o/r.git")
	// A host that names no forge, such as an SSH config alias, is GitHub.
	for _, url := range []string{"git@git.example.com:o/r.git", "work:o/r.git"} {
		set(url)
		if k, err := forgeKind(ctx, repo); err != nil || k != forge.GitHub {
			t.Fatalf("%s: %q, %v", url, k, err)
		}
	}
	os.WriteFile(cfg, []byte(`not json`), 0o644)
	if _, err := forgeKind(ctx, repo); err == nil {
		t.Fatal("a broken rig.json was ignored")
	}
	os.WriteFile(cfg, []byte(`{"host":"gitlab.example.com"}`), 0o644)
	if k, err := forgeKind(ctx, repo); err != nil || k != forge.GitLab {
		t.Fatalf("host override: %q, %v", k, err)
	}
	os.WriteFile(cfg, []byte(`{"forge":"github"}`), 0o644)
	if k, err := forgeKind(ctx, repo); err != nil || k != forge.GitHub {
		t.Fatalf("forge override: %q, %v", k, err)
	}
}

// statusRun runs `rig status` on the env's memstore with fake as the forge,
// a PATH that holds no gh or glab, and a tracker seam that fails the test.
func (e *taskEnv) statusRun(f *fake.Forge, dir string, args ...string) result {
	e.t.Helper()
	var out, errOut bytes.Buffer
	env := map[string]string{"RIG_WORKFLOW": e.env["RIG_WORKFLOW"], "PATH": e.t.TempDir()}
	a := &app{
		stdout: &out, stderr: &errOut, cwd: dir,
		getenv: func(k string) string { return env[k] },
		store:  func(context.Context, string) (store.Store, error) { return e.st, nil },
		forge:  func(context.Context, string) (forge.Forge, error) { return f, nil },
		tracker: func(context.Context, trackerSpec) (tracker.Tracker, error) {
			e.t.Error("status reached for a tracker")
			return nil, errors.New("no tracker")
		},
	}
	code := a.run(ctx, append([]string{"status"}, args...))
	return result{code, out.String(), errOut.String()}
}

// bareRepo is a git repository with no commits, no handoff and no docs/plan.
func bareRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitc(t, dir, "init", "-q", "-b", "main")
	return dir
}

func TestStatusStoreOnlyAndEmpty(t *testing.T) {
	e := newTaskEnv(t)
	dir := bareRepo(t)
	f := &fake.Forge{}

	r := e.statusRun(f, dir)
	if r.code != 0 || !strings.Contains(r.out, "STORE: no tasks\n") || strings.Contains(r.out, "HANDOFF") {
		t.Fatalf("empty store: %+v", r)
	}
	r = e.statusRun(f, dir, "--json")
	var empty struct {
		Store map[string]json.RawMessage `json:"store"`
	}
	if err := json.Unmarshal([]byte(r.out), &empty); err != nil || r.code != 0 || string(empty.Store["tasks"]) != "[]" {
		t.Fatalf("empty store json: %+v", r)
	}

	e.newTask("alpha")
	r = e.statusRun(f, dir)
	if r.code != 0 || !strings.Contains(r.out, "STORE TASKS (1):\n") || !strings.Contains(r.out, "alpha  Title alpha\n") {
		t.Fatalf("store only: %+v", r)
	}
	if strings.Contains(r.out, "HANDOFF") || strings.Contains(r.out, "OPEN TASKS (") {
		t.Fatalf("phase-1 output without its sources: %s", r.out)
	}
	if len(f.Calls) != 3 { // phase-1 ListPRs only, once per run
		t.Fatalf("forge calls = %q", f.Calls)
	}
}

// The store view makes no network call: the only forge call is phase-1's
// ListPRs, the tracker seam is never built, PATH holds no gh or glab, and an
// unreachable forge costs the PR line only.
func TestStatusStoreViewNeedsNoNetwork(t *testing.T) {
	e := newTaskEnv(t)
	e.newTask("alpha")
	for _, args := range [][]string{nil, {"--json"}} {
		f := &fake.Forge{Err: errors.New("network is down")}
		r := e.statusRun(f, e.repo, args...)
		if r.code != 0 || !strings.Contains(r.out, "alpha") {
			t.Fatalf("%v: %+v", args, r)
		}
		if len(f.Calls) != 1 || !strings.HasPrefix(f.Calls[0], "ListPRs ") {
			t.Fatalf("%v: forge calls = %q", args, f.Calls)
		}
	}
}

// With a handoff, task files and a store, the phase-1 lines come out exactly
// as before and the store section follows them.
func TestStatusGoldenPhase1ThenStore(t *testing.T) {
	e := newTaskEnv(t)
	task := e.newTask("alpha")
	note := filepath.Join(e.repo, ".git", handoffFile)
	if err := os.WriteFile(note, []byte("Next: verify 07\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(note)
	if err != nil {
		t.Fatal(err)
	}
	r := e.statusRun(&fake.Forge{}, e.repo)
	want := "HANDOFF from the last session (" + fi.ModTime().Format("2006-01-02 15:04") + "):\n" +
		"  Next: verify 07\n" +
		"OPEN TASKS (1):\n  07-foo\n" +
		"OPEN PRS: none\n" +
		"STORE TASKS (1):\n  " + task["short"].(string) + "  open        alpha  Title alpha\n"
	if r.code != 0 || r.out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", r.out, want)
	}
}

func TestStatusOpenTasksStatesAndFindings(t *testing.T) {
	e := newTaskEnv(t)
	run := func(args ...string) {
		t.Helper()
		if r := e.rig(args...); r.code != 0 {
			t.Fatalf("%v: %+v", args, r)
		}
	}
	a := e.newTask("aa")
	b := e.newTask("bb")
	c := e.newTask("cc", "--field", "owner=x")
	d := e.newTask("dd")
	as, bs, cs, ds := a["short"].(string), b["short"].(string), c["short"].(string), d["short"].(string)
	run("task", "move", cs, "done")
	run("task", "move", ds, "stopped", "--reason", "why")
	run("task", "parent", bs, as)

	// bb goes sha-bound, then a commit moves its branch: effective "back".
	branch := "task/" + bs + "-bb"
	gitc(t, e.repo, "branch", branch)
	run("task", "move", bs, "bound", "--sha", gitc(t, e.repo, "rev-parse", branch))
	gitc(t, e.repo, "checkout", "-q", branch)
	gitc(t, e.repo, "commit", "-q", "--allow-empty", "-m", "more")
	gitc(t, e.repo, "checkout", "-q", "main")

	run("finding", "add", bs, "--severity", "P1", "--summary", "one")
	run("finding", "add", bs, "--severity", "P1", "--summary", "two")
	run("finding", "add", as, "--severity", "P1", "--summary", "three")
	run("finding", "add", cs, "--severity", "P1", "--summary", "on a done task")

	f := &fake.Forge{}
	r := e.statusRun(f, e.repo, "--json")
	var got struct {
		Store storeView `json:"store"`
	}
	if err := json.Unmarshal([]byte(r.out), &got); err != nil || r.code != 0 {
		t.Fatalf("%v: %+v", err, r)
	}
	var order []string
	states := map[string]string{}
	for _, tk := range got.Store.Tasks {
		order = append(order, tk.Short)
		states[tk.Short] = tk.State
	}
	// By state order (distance from the initial state: open, stopped, back), newest first within one.
	if want := []string{as, ds, bs}; strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v\n%s", order, want, r.out)
	}
	if states[as] != "open" || states[bs] != "back" || states[ds] != "stopped" {
		t.Fatalf("states = %v", states)
	}
	if got.Store.Tasks[2].Parent != as {
		t.Fatalf("parent = %q, want %q", got.Store.Tasks[1].Parent, as)
	}
	if fi := got.Store.Findings; fi.Open != 3 || fi.ByTask[bs] != 2 || fi.ByTask[as] != 1 || len(fi.ByTask) != 2 {
		t.Fatalf("findings = %+v", fi)
	}
	r = e.statusRun(f, e.repo)
	parts := []string{as + ": 1", bs + ": 2"}
	sort.Strings(parts) // by short id
	if !strings.Contains(r.out, "STORE TASKS (3):\n") || !strings.Contains(r.out, "STORE FINDINGS: 3 open ("+strings.Join(parts, ", ")+")\n") ||
		strings.Contains(r.out, cs+"  ") {
		t.Fatalf("text:\n%s", r.out)
	}
}

func TestStatusStoreTextCapAndEscapes(t *testing.T) {
	e := newTaskEnv(t)
	for i := range 52 {
		title := fmt.Sprintf("T%02d", i)
		if i == 51 {
			title = "evil\x1b[31m\rline"
		}
		if r := e.rig("task", "new", "--slug", fmt.Sprintf("s%02d", i), "--title", title); r.code != 0 {
			t.Fatalf("%+v", r)
		}
	}
	r := e.statusRun(&fake.Forge{}, e.repo)
	if !strings.Contains(r.out, "STORE TASKS (52):\n") || !strings.Contains(r.out, "  … and 2 more\n") ||
		strings.Count(r.out, "  t-") != 50 {
		t.Fatalf("cap:\n%s", r.out)
	}
	if strings.ContainsAny(r.out, "\x1b\r") || !strings.Contains(r.out, `evil\x1b[31m\x0dline`) {
		t.Fatalf("control bytes in text output:\n%q", r.out)
	}
	var got struct {
		Store storeView `json:"store"`
	}
	r = e.statusRun(&fake.Forge{}, e.repo, "--json")
	if err := json.Unmarshal([]byte(r.out), &got); err != nil || len(got.Store.Tasks) != 52 {
		t.Fatalf("json is uncapped: %v, %d", err, len(got.Store.Tasks))
	}
}

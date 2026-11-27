package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/tracker"
	"github.com/AindriuB/rig/internal/tracker/fake"
)

// onceEnv is a real repository under the test workflow, driven through the
// real git-ref store.
type onceEnv struct {
	t    *testing.T
	repo string
	env  map[string]string
}

func newOnceEnv(t *testing.T) *onceEnv {
	t.Helper()
	repo, _ := newRepo(t)
	return &onceEnv{t: t, repo: repo, env: map[string]string{"RIG_WORKFLOW": testWorkflow(t), "RIG_ACTOR": "tester"}}
}

func (e *onceEnv) rig(args ...string) result {
	e.t.Helper()
	var out, errOut bytes.Buffer
	a := &app{stdout: &out, stderr: &errOut, cwd: e.repo, now: tick(),
		getenv: func(k string) string { return e.env[k] }}
	a.store = a.openStore
	code := a.run(context.Background(), append(args, "--repo", e.repo))
	return result{code, out.String(), errOut.String()}
}

func (e *onceEnv) must(args ...string) result {
	e.t.Helper()
	r := e.rig(args...)
	if r.code != 0 {
		e.t.Fatalf("rig %v: %+v", args, r)
	}
	return r
}

func (e *onceEnv) newTask(slug string) {
	e.t.Helper()
	e.must("task", "new", "--slug", slug, "--title", "T "+slug)
}

func (e *onceEnv) events(ref string) []store.Event {
	e.t.Helper()
	a := &app{now: tick(), getenv: func(k string) string { return e.env[k] }}
	st, err := a.openStore(context.Background(), e.repo)
	if err != nil {
		e.t.Fatal(err)
	}
	evs, err := st.Events(context.Background(), ref)
	if err != nil {
		e.t.Fatal(err)
	}
	return evs
}

func onceKindCount(evs []store.Event, kind string) int {
	n := 0
	for _, e := range evs {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

const repeatLine = "repeat: --once k already recorded\n"

// AC3: a repeated move is a no-op that exits 0, though "reviewed" to
// "reviewed" has no transition.
func TestOnceMoveRepeat(t *testing.T) {
	e := newOnceEnv(t)
	e.newTask("a")
	e.must("task", "move", "a", "reviewed", "--once", "k")
	r := e.rig("task", "move", "a", "reviewed", "--once", "k")
	if r.code != 0 || r.out != repeatLine {
		t.Fatalf("repeat: %+v", r)
	}
	if n := onceKindCount(e.events("a"), store.KindTransition); n != 1 {
		t.Fatalf("%d transitions, want 1", n)
	}
	if r := e.rig("task", "move", "a", "reviewed", "--once", "other"); r.code != 1 {
		t.Fatalf("another key is not a repeat, and the move has no transition: %+v", r)
	}
}

// AC4: the repeat is found before the command gate runs.
func TestOnceMoveRepeatRunsNoGate(t *testing.T) {
	e := newOnceEnv(t)
	out := filepath.Join(t.TempDir(), "gate.log")
	t.Setenv("RIG_CMD_HELPER", out)
	t.Setenv("RIG_CMD_EXIT", "0")
	e.newTask("a")
	e.must("task", "move", "a", "checked", "--once", "k")
	b, _ := os.ReadFile(out)
	if n := strings.Count(string(b), "\n"); n != 1 {
		t.Fatalf("the gate ran %d times on the first move", n)
	}
	if r := e.rig("task", "move", "a", "checked", "--once", "k"); r.code != 0 || r.out != repeatLine {
		t.Fatalf("repeat: %+v", r)
	}
	b, _ = os.ReadFile(out)
	if n := strings.Count(string(b), "\n"); n != 1 {
		t.Fatalf("the gate ran %d times after a repeat, want 1", n)
	}
}

// AC5: notes and created events are searched.
func TestOnceNoteAndNewRepeat(t *testing.T) {
	e := newOnceEnv(t)
	e.must("note", "add", "--kind", "k", "--text", "x", "--once", "n")
	r := e.rig("note", "add", "--kind", "k", "--text", "x", "--once", "n")
	if r.code != 0 || r.out != "repeat: --once n already recorded\n" {
		t.Fatalf("note repeat: %+v", r)
	}
	var notes []string
	for _, l := range strings.Split(strings.TrimSpace(e.must("note", "list").out), "\n") {
		if l != "" {
			notes = append(notes, l)
		}
	}
	if len(notes) != 1 {
		t.Fatalf("notes: %q", notes)
	}

	e.must("task", "new", "--slug", "s", "--title", "t", "--once", "c")
	r = e.rig("task", "new", "--slug", "s2", "--title", "t", "--once", "c")
	if r.code != 0 || r.out != "repeat: --once c already recorded\n" {
		t.Fatalf("task new repeat: %+v", r)
	}
	if got := e.rig("task", "list"); strings.Contains(got.out, "s2") || !strings.Contains(got.out, "s") {
		t.Fatalf("tasks: %q", got.out)
	}
}

// AC6: a key is per task.
func TestOnceKeyIsPerTask(t *testing.T) {
	e := newOnceEnv(t)
	e.newTask("a")
	e.newTask("b")
	e.must("task", "move", "a", "reviewed", "--once", "k")
	if r := e.must("task", "move", "b", "reviewed", "--once", "k"); r.out == repeatLine {
		t.Fatalf("the same key on another task was a repeat")
	}
	if n := onceKindCount(e.events("b"), store.KindTransition); n != 1 {
		t.Fatalf("b has %d transitions", n)
	}
}

// AC7: inside a hook the key defaults from the hook and the command line.
func TestOnceHookDefault(t *testing.T) {
	e := newOnceEnv(t)
	e.env["RIG_HOOK_NAME"], e.env["RIG_HOOK_EVENT"] = "h", "EV1"
	e.newTask("a")
	// task new above ran as the hook too; its default key is its own.
	add := func(text string) result {
		return e.must("note", "add", "a", "--kind", "k", "--text", text)
	}
	add("a")
	if r := add("a"); !strings.HasPrefix(r.out, "repeat: --once hook-") {
		t.Fatalf("identical command: %+v", r)
	}
	if r := add("b"); strings.HasPrefix(r.out, "repeat:") {
		t.Fatalf("different text was a repeat: %+v", r)
	}
	if got := e.must("note", "list", "a").out; strings.Count(got, "\n") != 2 {
		t.Fatalf("notes: %q", got)
	}
	// An explicit --once wins over the default.
	e.must("note", "add", "a", "--kind", "k", "--text", "a", "--once", "mine")
	if r := e.must("note", "add", "a", "--kind", "k", "--text", "zzz", "--once", "mine"); !strings.Contains(r.out, "--once mine") {
		t.Fatalf("explicit key: %+v", r)
	}
	// Outside a hook there is no default.
	delete(e.env, "RIG_HOOK_EVENT")
	e.must("note", "add", "a", "--kind", "k", "--text", "a")
	e.must("note", "add", "a", "--kind", "k", "--text", "a")
}

// AC8: the JSON shape of a repeat.
func TestOnceJSONRepeat(t *testing.T) {
	e := newOnceEnv(t)
	e.newTask("a")
	e.must("task", "set", "a", "--field", "owner=me")
	first := e.must("task", "move", "a", "done", "--json", "--once", "k")
	if strings.Contains(first.out, `"repeat"`) {
		t.Fatalf("first move: %q", first.out)
	}
	r := e.rig("task", "move", "a", "done", "--json", "--once", "k")
	if r.code != 0 || r.out != "{\"repeat\":true,\"once\":\"k\"}\n" {
		t.Fatalf("repeat: %+v", r)
	}
}

// AC9: an empty key, and the tracker bulk commands.
func TestOnceRefused(t *testing.T) {
	e := newOnceEnv(t)
	e.newTask("a")
	for _, args := range [][]string{
		{"task", "move", "a", "reviewed", "--once", ""},
		{"task", "move", "a", "reviewed", "--once", "  "},
		{"note", "add", "--kind", "k", "--text", "x", "--once="},
		{"tracker", "push", "a", "--once", "k"},
		{"tracker", "pull", "X-1", "--once", "k"},
		{"tracker", "refresh", "--once", "k"},
	} {
		if r := e.rig(args...); r.code != 2 {
			t.Errorf("rig %q: %+v", args, r)
		}
	}
	if n := onceKindCount(e.events("a"), store.KindTransition); n != 0 {
		t.Fatalf("a refused write wrote %d transitions", n)
	}
}

// A finding write repeats too.
func TestOnceFindingRepeat(t *testing.T) {
	e := newOnceEnv(t)
	e.newTask("a")
	e.must("finding", "add", "a", "--severity", "P1", "--summary", "s", "--once", "k")
	if r := e.must("finding", "add", "a", "--severity", "P2", "--summary", "other", "--once", "k"); r.out != repeatLine {
		t.Fatalf("repeat: %+v", r)
	}
	if n := onceKindCount(e.events("a"), store.KindFinding); n != 1 {
		t.Fatalf("%d findings", n)
	}
}

// AC11: task link --once succeeds on its first run and writes the
// tracker-sync snapshot; a repeat reports repeat and writes nothing.
func TestOnceTaskLink(t *testing.T) {
	e := newTrkEnv(t, ghCfg())
	e.tr = fake.New(allCaps, []tracker.Issue{issue("12", "Remote Title")}, nil)
	x := e.mkTask("x")
	if code := e.run("task", "link", x.Short, "github:12", "--once", "k"); code != 0 {
		t.Fatalf("first run: exit %d, stderr %q", code, e.err.String())
	}
	if onceKindCount(e.eventsByTask()[x.ID], store.KindTrackerSync) != 1 {
		t.Fatal("no tracker-sync snapshot after the first run")
	}
	n := e.eventCount(x.ID)
	e.out.Reset()
	if code := e.run("task", "link", x.Short, "github:12", "--once", "k"); code != 0 || !strings.Contains(e.out.String(), "repeat") {
		t.Fatalf("repeat: exit %d, out %q, err %q", code, e.out.String(), e.err.String())
	}
	if e.eventCount(x.ID) != n {
		t.Fatal("the repeat wrote events")
	}
}

package main

import (
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/tracker"
	"github.com/AindriuB/rig/internal/tracker/fake"
	"github.com/AindriuB/rig/internal/workflow"
)

// mkTask makes an unlinked task and returns its view.
func (e *trkEnv) mkTask(slug string, extra ...string) workflow.TaskView {
	e.t.Helper()
	var v workflow.TaskView
	args := append([]string{"task", "new", "--slug", slug, "--title", "Title " + slug}, extra...)
	if code := e.json(&v, args...); code != 0 {
		e.t.Fatalf("task new %s: exit %d: %s", slug, code, e.err.String())
	}
	return v
}

// AC1: task new --from creates the linked task through intake; a repeat
// returns it; a wrong kind is exit 2; a bad --state is exit 1 and makes no
// tracker call.
func TestTaskNewFrom(t *testing.T) {
	e := newTrkEnv(t, ghCfg())
	c := &counting{Tracker: fake.New(allCaps, []tracker.Issue{issue("12", "Fix the Login"), issue("13", "Other")}, nil)}
	e.tr = c

	var v workflow.TaskView
	if code := e.json(&v, "task", "new", "--from", "12"); code != 0 {
		t.Fatalf("exit %d: %s", code, e.err.String())
	}
	x := v.External
	if x == nil || x.Key != "12" || x.Kind != "github" || v.Title != "Fix the Login" || v.Slug != "12-fix-the-login" || v.Body != "about Fix the Login" {
		t.Fatalf("%+v", v)
	}
	if ts := e.snapshotOf(v.Short); ts["title"] != "Fix the Login" {
		t.Fatalf("snapshot %v", ts)
	}

	// The kind prefix is accepted, and a repeat is the existing task.
	var again workflow.TaskView
	if code := e.json(&again, "task", "new", "--from", "github:12"); code != 0 || again.ID != v.ID {
		t.Fatalf("repeat: exit %d %+v", code, again)
	}
	if !strings.Contains(e.err.String(), "already task") {
		t.Fatalf("stderr %q", e.err.String())
	}
	if all, _ := e.st().Tasks(ctx, store.TaskFilter{}); len(all) != 1 {
		t.Fatalf("%d tasks", len(all))
	}

	// A kind that is not the configured tracker's is a usage error.
	n := c.n
	if code := e.run("task", "new", "--from", "jira:13"); code != 2 || !strings.Contains(e.err.String(), "github") {
		t.Fatalf("wrong kind: exit %d, stderr %q", code, e.err.String())
	}
	if c.n != n {
		t.Fatal("a wrong kind reached the tracker")
	}

	// --slug is honoured.
	var w workflow.TaskView
	if code := e.json(&w, "task", "new", "--from", "13", "--slug", "mine"); code != 0 || w.Slug != "mine" {
		t.Fatalf("slug: exit %d %+v (%s)", code, w, e.err.String())
	}
}

func TestTaskNewFromBadState(t *testing.T) {
	e := newTrkEnv(t, ghCfg())
	c := &counting{Tracker: fake.New(allCaps, []tracker.Issue{issue("12", "T")}, nil)}
	e.tr = c
	if code := e.run("task", "new", "--from", "12", "--state", "nowhere"); code != 1 || !strings.Contains(e.err.String(), "intake state") {
		t.Fatalf("exit %d, stderr %q", code, e.err.String())
	}
	if c.n != 0 {
		t.Fatalf("%d tracker calls", c.n)
	}
	if all, _ := e.st().Tasks(ctx, store.TaskFilter{}); len(all) != 0 {
		t.Fatal("a task was created")
	}
	// Without --from the old command is unchanged: --state is not one of its flags.
	if code := e.run("task", "new", "--slug", "a", "--title", "A", "--state", "open"); code != 2 {
		t.Fatalf("--state without --from: exit %d", code)
	}
	if code := e.run("task", "new", "--from", "12", "--title", "A"); code != 2 {
		t.Fatalf("--title with --from: exit %d", code)
	}
}

// parentOf is the stored parent id of a task.
func (e *trkEnv) parentOf(ref string) string {
	e.t.Helper()
	task, err := e.st().Task(ctx, ref)
	if err != nil {
		e.t.Fatal(err)
	}
	return task.Parent
}

// AC2: task parent sets and clears; a cycle and a missing parent are exit 1
// and write nothing.
func TestTaskParent(t *testing.T) {
	e := newTrkEnv(t, ghCfg())
	a, b, c := e.mkTask("a"), e.mkTask("b"), e.mkTask("c")

	var v workflow.TaskView
	if code := e.json(&v, "task", "parent", b.Short, a.Short); code != 0 || v.Parent != a.ID || v.ID != b.ID {
		t.Fatalf("set: exit %d %+v (%s)", code, v, e.err.String())
	}
	if e.parentOf(b.ID) != a.ID {
		t.Fatal("parent not stored")
	}
	if code := e.run("task", "parent", c.Short, b.Slug); code != 0 || e.parentOf(c.ID) != b.ID {
		t.Fatalf("slug ref: exit %d (%s)", code, e.err.String())
	}

	// A cycle, the task itself and a missing parent are refused, nothing written.
	before := e.eventCount(a.ID)
	for _, args := range [][]string{
		{"task", "parent", a.Short, c.Short}, // a -> c -> b -> a
		{"task", "parent", a.Short, a.Short},
		{"task", "parent", a.Short, "nosuchtask"},
	} {
		if code := e.run(args...); code != 1 {
			t.Fatalf("%v: exit %d, stderr %q", args, code, e.err.String())
		}
	}
	if e.eventCount(a.ID) != before || e.parentOf(a.ID) != "" {
		t.Fatal("a refused parent change wrote an event")
	}

	// none clears; clearing an already clear parent is not an error.
	var cleared workflow.TaskView
	if code := e.json(&cleared, "task", "parent", b.Short, "none"); code != 0 || cleared.Parent != "" || cleared.ID != b.ID || e.parentOf(b.ID) != "" {
		t.Fatalf("clear: exit %d %+v", code, cleared)
	}
	n := e.eventCount(b.ID)
	if code := e.run("task", "parent", b.Short, "none"); code != 0 || e.eventCount(b.ID) != n {
		t.Fatalf("clear again: exit %d", code)
	}
	// A missing task is exit 2 like every other task command.
	if code := e.run("task", "parent", "nosuchtask", a.Short); code != 2 {
		t.Fatalf("missing task: exit %d", code)
	}
	if code := e.run("task", "parent", a.Short); code != 2 {
		t.Fatalf("usage: exit %d", code)
	}
}

// END-OF-TESTS

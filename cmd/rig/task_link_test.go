package main

import (
	"context"
	"errors"
	"os"
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

// AC3: task link writes the link and does one refresh; an issue linked to
// another task is exit 1; a malformed ref never reaches the store or the
// tracker; a failed refresh says the link stands.
func TestTaskLink(t *testing.T) {
	e := newTrkEnv(t, ghCfg())
	c := &counting{Tracker: fake.New(allCaps, []tracker.Issue{issue("12", "Remote Title")}, nil)}
	e.tr = c
	x, y := e.mkTask("x"), e.mkTask("y")

	// A malformed ref, or one of another kind, stops before the store and the tracker.
	before := e.eventCount(x.ID)
	for _, ref := range []string{"a/b", "12#3", "a..b", "jira:12", "github:", ":12", "ba d"} {
		if code := e.run("task", "link", x.Short, ref); code != 2 {
			t.Fatalf("%q: exit %d, stderr %q", ref, code, e.err.String())
		}
	}
	if c.n != 0 || e.eventCount(x.ID) != before {
		t.Fatalf("a malformed ref reached the tracker (%d calls) or the store", c.n)
	}

	var v workflow.TaskView
	if code := e.json(&v, "task", "link", x.Short, "github:12"); code != 0 {
		t.Fatalf("exit %d: %s", code, e.err.String())
	}
	if v.ID != x.ID || v.External == nil || v.External.Key != "12" || v.External.Kind != "github" || v.External.Host != "github.com" ||
		strings.Join(v.External.Owned, ",") != "body,title" || v.Title != "Remote Title" || v.Body != "about Remote Title" ||
		len(v.External.Drift) != 0 || v.External.SyncedAt.IsZero() {
		t.Fatalf("%+v", v)
	}
	if c.n != 2 {
		t.Fatalf("%d tracker calls, want the fetch and one refresh", c.n)
	}
	if v.External.URL != "https://t.example/12" || v.External.Type != "Task" {
		t.Fatalf("link URL/Type: %+v", v.External)
	}
	if ts := e.snapshotOf(x.Short); ts["title"] != "Remote Title" || ts["body"] != "about Remote Title" {
		t.Fatalf("snapshot %v", ts)
	}

	// The same issue from another task is refused; the same task may repeat.
	before = e.eventCount(y.ID)
	if code := e.run("task", "link", y.Short, "12"); code != 1 || !strings.Contains(e.err.String(), x.Short) {
		t.Fatalf("exit %d, stderr %q", code, e.err.String())
	}
	if e.eventCount(y.ID) != before {
		t.Fatal("a refused link wrote an event")
	}
	if code := e.run("task", "link", x.Short, "12"); code != 0 {
		t.Fatalf("relink: exit %d: %s", code, e.err.String())
	}

	// An issue the tracker does not have is exit 2 and writes no link.
	before = e.eventCount(y.ID)
	if code := e.run("task", "link", y.Short, "99"); code != 2 || !strings.Contains(e.err.String(), "not found") {
		t.Fatalf("exit %d, stderr %q", code, e.err.String())
	}
	if task, _ := e.st().Task(ctx, y.ID); task.External != nil || e.eventCount(y.ID) != before {
		t.Fatalf("a link to a missing issue was written: %+v", task.External)
	}
	// A repeat of the task's own link writes no link event, only the refresh.
	before = e.eventCount(x.ID)
	if code := e.run("task", "link", x.Short, "12"); code != 0 {
		t.Fatalf("relink: exit %d: %s", code, e.err.String())
	}
	if n := e.eventCount(x.ID); n != before {
		t.Fatalf("a repeated link wrote %d events", n-before)
	}
	if code := e.run("task", "link", y.Short); code != 2 {
		t.Fatalf("usage: exit %d", code)
	}
}

// AC4: task unlink removes the link, the fields keep their values and are the
// task's own; an unlinked task is exit 1.
func TestTaskUnlink(t *testing.T) {
	e := newTrkEnv(t, ghCfg())
	e.tr = fake.New(allCaps, []tracker.Issue{issue("12", "Remote Title")}, nil)
	x := e.mkTask("x")
	if code := e.run("task", "unlink", x.Short); code != 1 {
		t.Fatalf("unlinked: exit %d", code)
	}
	if code := e.run("task", "link", x.Short, "12"); code != 0 {
		t.Fatalf("link: exit %d: %s", code, e.err.String())
	}
	var v workflow.TaskView
	if code := e.json(&v, "task", "unlink", x.Short); code != 0 || v.ID != x.ID || v.External != nil || v.Title != "Remote Title" || v.Body != "about Remote Title" {
		t.Fatalf("exit %d %+v (%s)", code, v, e.err.String())
	}
	if task, _ := e.st().Task(ctx, x.ID); task.External != nil {
		t.Fatalf("still linked: %+v", task.External)
	}
	if code := e.run("task", "set", x.Short, "--field", "title=Mine"); code != 0 {
		t.Fatalf("set after unlink: exit %d: %s", code, e.err.String())
	}
	if code := e.run("task", "unlink", x.Short); code != 1 {
		t.Fatalf("second unlink: exit %d", code)
	}
	// The issue is free again.
	z := e.mkTask("z")
	if code := e.run("task", "link", z.Short, "12"); code != 0 {
		t.Fatalf("relink elsewhere: exit %d: %s", code, e.err.String())
	}
}

// forest makes a -> b -> c and a lone d, and returns them.
func (e *trkEnv) forest() (a, b, c, d workflow.TaskView) {
	e.t.Helper()
	a, b, c, d = e.mkTask("a"), e.mkTask("b"), e.mkTask("c"), e.mkTask("d")
	for _, p := range [][2]string{{b.Short, a.Short}, {c.Short, b.Short}} {
		if code := e.run("task", "parent", p[0], p[1]); code != 0 {
			e.t.Fatalf("parent %v: exit %d: %s", p, code, e.err.String())
		}
	}
	return
}

func slugsOf(vs []workflow.TaskView) string {
	var s []string
	for _, v := range vs {
		s = append(s, v.Slug)
	}
	return strings.Join(s, ",")
}

// AC5: list --parent, --roots and --tree, text and JSON, with the empty cases;
// two of them at once is exit 2.
func TestTaskListHierarchy(t *testing.T) {
	e := newTrkEnv(t, ghCfg())
	a, b, _, d := e.forest()

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--parent", a.Short}, "b"},
		{[]string{"--parent", b.Slug}, "c"},
		{[]string{"--parent", d.Short}, ""},
		{[]string{"--roots"}, "d,a"},
		{[]string{"--tree"}, "d,a,b,c"},
	} {
		var out []workflow.TaskView
		args := append([]string{"task", "list"}, tc.args...)
		if code := e.json(&out, args...); code != 0 || slugsOf(out) != tc.want {
			t.Fatalf("%v: exit %d, got %q want %q (%s)", tc.args, code, slugsOf(out), tc.want, e.err.String())
		}
		if tc.want == "" && strings.TrimSpace(e.out.String()) != "[]" {
			t.Fatalf("%v: empty JSON is %q", tc.args, e.out.String())
		}
	}

	// Text: --tree indents each child under its parent.
	if code := e.run("task", "list", "--tree"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	lines := strings.Split(strings.TrimRight(e.out.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("tree text:\n%s", e.out.String())
	}
	for i, depth := range []int{0, 0, 1, 2} {
		if got := len(lines[i]) - len(strings.TrimLeft(lines[i], " ")); got != 2*depth {
			t.Fatalf("line %d indent %d, want %d:\n%s", i, got, 2*depth, e.out.String())
		}
	}
	if code := e.run("task", "list", "--parent", d.Short); code != 0 || e.out.Len() != 0 {
		t.Fatalf("empty text: exit %d %q", code, e.out.String())
	}
	if code := e.run("task", "list", "--parent", a.Short); code != 0 || !strings.Contains(e.out.String(), "Title b") || strings.Contains(e.out.String(), "Title c") {
		t.Fatalf("parent text: exit %d %q", code, e.out.String())
	}

	// Markdown.
	if code := e.run("task", "list", "--tree", "--markdown"); code != 0 || !strings.HasPrefix(e.out.String(), "# Tasks\n\n- ") || !strings.Contains(e.out.String(), "\n    - ") {
		t.Fatalf("tree markdown: exit %d\n%s", code, e.out.String())
	}
	if code := e.run("task", "list", "--parent", d.Short, "--markdown"); code != 0 || e.out.String() != "# Tasks\n\nnone\n" {
		t.Fatalf("empty markdown: exit %d %q", code, e.out.String())
	}

	// At most one of the three; a missing parent task is an error.
	for _, args := range [][]string{
		{"--roots", "--tree"}, {"--parent", a.Short, "--roots"}, {"--parent", a.Short, "--tree"}, {"--parent", "nosuchtask"}, {"--parent"},
	} {
		if code := e.run(append([]string{"task", "list"}, args...)...); code != 2 {
			t.Fatalf("%v: exit %d", args, code)
		}
	}
}

type withChildren struct {
	workflow.TaskView
	Children []workflow.TaskView `json:"children"`
}

// AC5: task show --children, text, Markdown and JSON, with and without any.
func TestTaskShowChildren(t *testing.T) {
	e := newTrkEnv(t, ghCfg())
	a, b, c, _ := e.forest()

	var out withChildren
	if code := e.json(&out, "task", "show", a.Short, "--children"); code != 0 || out.ID != a.ID || slugsOf(out.Children) != "b" {
		t.Fatalf("exit %d %+v (%s)", code, out, e.err.String())
	}
	var leaf withChildren
	if code := e.json(&leaf, "task", "show", c.Short, "--children"); code != 0 || leaf.Children == nil || len(leaf.Children) != 0 || !strings.Contains(e.out.String(), `"children": []`) && !strings.Contains(e.out.String(), `"children":[]`) {
		t.Fatalf("leaf: exit %d %s", code, e.out.String())
	}
	// Without the flag the JSON is the Task alone.
	if code := e.run("task", "show", a.Short, "--json"); code != 0 || strings.Contains(e.out.String(), `"children"`) {
		t.Fatalf("no flag: exit %d %s", code, e.out.String())
	}

	if code := e.run("task", "show", a.Short, "--children"); code != 0 || !strings.Contains(e.out.String(), "## Children\n\n- "+b.Short) {
		t.Fatalf("text: exit %d\n%s", code, e.out.String())
	}
	if code := e.run("task", "show", c.Short, "--children"); code != 0 || !strings.HasSuffix(e.out.String(), "## Children\n\nnone\n") {
		t.Fatalf("text leaf: exit %d\n%s", code, e.out.String())
	}
	if code := e.run("task", "show", a.Short, "--children", "--markdown"); code != 0 || !strings.HasSuffix(e.out.String(), "\n## Children\n\n- "+b.Short+" [open] Title b (b)\n") {
		t.Fatalf("markdown: exit %d\n%s", code, e.out.String())
	}
	if code := e.run("task", "show", c.Short, "--children", "--markdown"); code != 0 || !strings.HasSuffix(e.out.String(), "\n## Children\n\nnone\n") {
		t.Fatalf("markdown leaf: exit %d\n%s", code, e.out.String())
	}
}

// ownedEnv is a trkEnv whose workflow has non-owned fields (owner, tags, ...)
// and one linked task, x, whose issue is 12.
func ownedEnv(t *testing.T) (*trkEnv, workflow.TaskView) {
	t.Helper()
	e := newTrkEnv(t, ghCfg())
	e.envVars["RIG_WORKFLOW"] = taskWorkflow(t)
	e.tr = fake.New(allCaps, []tracker.Issue{{Key: "12", Title: "Title x"}}, nil)
	x := e.mkTask("x")
	if code := e.run("task", "link", x.Short, "12"); code != 0 {
		t.Fatalf("link: exit %d: %s", code, e.err.String())
	}
	return e, x
}

func (e *trkEnv) fieldsOf(ref string) (title, body, owner string) {
	e.t.Helper()
	task, err := e.st().Task(ctx, ref)
	if err != nil {
		e.t.Fatal(err)
	}
	owner, _ = task.Fields["owner"].(string)
	return task.Title, task.Body, owner
}

// AC6: task set refuses a change to an owned field of a linked task, names
// the tracker and `rig task unlink`, writes nothing; other fields work.
func TestTaskSetRefusesOwnedFields(t *testing.T) {
	e, x := ownedEnv(t)
	n := e.eventCount(x.ID)
	for _, f := range []string{"title=Mine", "body=Mine"} {
		if code := e.run("task", "set", x.Short, "--field", f); code != 1 ||
			!strings.Contains(e.err.String(), "rig task unlink") || !strings.Contains(e.err.String(), "github") {
			t.Fatalf("%s: exit %d, stderr %q", f, code, e.err.String())
		}
	}
	// One owned field among others refuses all of them.
	if code := e.run("task", "set", x.Short, "--field", "owner=ann", "--field", "title=Mine"); code != 1 {
		t.Fatalf("mixed: exit %d", code)
	}
	if e.eventCount(x.ID) != n {
		t.Fatal("a refused set wrote an event")
	}
	// A value the field already has is not a change.
	if code := e.run("task", "set", x.Short, "--field", "title=Title x"); code != 0 {
		t.Fatalf("same value: exit %d: %s", code, e.err.String())
	}
	if code := e.run("task", "set", x.Short, "--field", "owner=ann"); code != 0 {
		t.Fatalf("non-owned: exit %d: %s", code, e.err.String())
	}
	if title, _, owner := e.fieldsOf(x.ID); title != "Title x" || owner != "ann" {
		t.Fatalf("title %q owner %q", title, owner)
	}
	// An unlinked task is free to change them.
	y := e.mkTask("y")
	if code := e.run("task", "set", y.Short, "--field", "title=Mine"); code != 0 {
		t.Fatalf("unlinked: exit %d", code)
	}
}

// AC6: task edit refuses an owned field of a linked task too, and keeps the
// user's copy.
func TestTaskEditRefusesOwnedFields(t *testing.T) {
	e, x := ownedEnv(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RIG_TASK_EDIT_HELPER", "1")
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TEMP", tmp)
	t.Setenv("TMP", tmp)
	e.envVars["EDITOR"] = exe
	e.envVars["RIG_EDITOR_NO_TTY"] = "1"

	t.Setenv("RIG_EDIT_FROM", "# Title x")
	t.Setenv("RIG_EDIT_TO", "# Changed")
	n := e.eventCount(x.ID)
	if code := e.run("task", "edit", x.Short); code != 1 ||
		!strings.Contains(e.err.String(), "rig task unlink") || !strings.Contains(e.err.String(), "your copy is at") {
		t.Fatalf("exit %d, stderr %q", code, e.err.String())
	}
	if e.eventCount(x.ID) != n {
		t.Fatal("a refused edit wrote an event")
	}

	// A non-owned field edits as before.
	t.Setenv("RIG_EDIT_FROM", "## owner\n\n")
	t.Setenv("RIG_EDIT_TO", "## owner\n\nann")
	if code := e.run("task", "edit", x.Short); code != 0 {
		t.Fatalf("non-owned: exit %d: %s", code, e.err.String())
	}
	if _, _, owner := e.fieldsOf(x.ID); owner != "ann" {
		t.Fatalf("owner %q", owner)
	}
}

// AC6: the owned-field check is repeated inside the store's Update, so an
// edit that read the task before it was linked cannot slip through. This is
// run on the git-ref store.
func TestTaskUpdateRefusesOwnedFieldsAfterLinkRace(t *testing.T) {
	e := newTrkEnv(t, ghCfg())
	e.tr = fake.New(allCaps, []tracker.Issue{{Key: "12", Title: "Title x"}}, nil)
	x := e.mkTask("x")
	w, err := e.a.openWork(ctx, e.repo, true)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := w.st.Task(ctx, x.ID) // an edit reads the task here, unlinked
	if err != nil {
		t.Fatal(err)
	}
	if code := e.run("task", "link", x.Short, "12"); code != 0 { // a link lands before its write
		t.Fatalf("link: exit %d: %s", code, e.err.String())
	}
	n := e.eventCount(x.ID)
	_, err = w.updateFields(ctx, stale, map[string]any{"title": "Sneaky"})
	var r *refusal
	if !errors.As(err, &r) || !strings.Contains(r.reason, "rig task unlink") || strings.Contains(r.reason, "changed since") {
		t.Fatalf("err %v", err)
	}
	if e.eventCount(x.ID) != n {
		t.Fatal("the racing edit was written")
	}
	if title, _, _ := e.fieldsOf(x.ID); title != "Title x" {
		t.Fatalf("title %q", title)
	}
	// A non-owned field still goes through on the same stale read.
	if _, err := w.updateFields(ctx, stale, map[string]any{"owner": "ann"}); err != nil {
		t.Fatalf("non-owned: %v", err)
	}
}

// A link written with a narrower Owned list than the workflow's current one
// refuses only what that link owns: the task's own External.Owned decides.
func TestTaskSetUsesLinkOwnedFields(t *testing.T) {
	e := newTrkEnv(t, ghCfg())
	e.envVars["RIG_WORKFLOW"] = taskWorkflow(t)
	x := e.mkTask("x")
	ev, err := store.NewEvent(store.KindExternal, store.ExternalData{Kind: "github", Host: "github.com", Key: "12", Owned: []string{"title"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st().Update(ctx, x.ID, func(store.Task, []store.Finding) ([]store.Event, error) { return []store.Event{ev}, nil }); err != nil {
		t.Fatal(err)
	}
	if code := e.run("task", "set", x.Short, "--field", "title=Mine"); code != 1 || !strings.Contains(e.err.String(), "rig task unlink") {
		t.Fatalf("title: exit %d, stderr %q", code, e.err.String())
	}
	if code := e.run("task", "set", x.Short, "--field", "body=Mine"); code != 0 {
		t.Fatalf("body is not owned by this link: exit %d: %s", code, e.err.String())
	}
}

// failAfter lets n tracker reads through, then fails every later one.
type failAfter struct {
	tracker.Tracker
	n int
}

func (f *failAfter) Issue(ctx context.Context, k string) (tracker.Issue, error) {
	if f.n <= 0 {
		return tracker.Issue{}, errors.New("tracker down")
	}
	f.n--
	return f.Tracker.Issue(ctx, k)
}

// answersKey answers every read with another key, as a moved issue does.
type answersKey struct {
	tracker.Tracker
	key string
}

func (a answersKey) Issue(ctx context.Context, k string) (tracker.Issue, error) {
	is, err := a.Tracker.Issue(ctx, k)
	is.Key = a.key
	return is, err
}

// task link reads the issue before it writes: a moved or malformed answer is
// exit 2 with no link; a refresh failure after the link stands says so; the
// link carries the issue's URL, Type and parent key.
func TestTaskLinkReadsFirst(t *testing.T) {
	base := issue("12", "Remote")
	base.Parent = &tracker.IssueRef{Key: "7"}
	for name, tr := range map[string]tracker.Tracker{
		"moved": answersKey{fake.New(allCaps, []tracker.Issue{base}, nil), "13"},
		"bad":   answersKey{fake.New(allCaps, []tracker.Issue{base}, nil), "a b"},
	} {
		e := newTrkEnv(t, ghCfg())
		e.tr = tr
		x := e.mkTask("x")
		n := e.eventCount(x.ID)
		if code := e.run("task", "link", x.Short, "12"); code != 2 {
			t.Fatalf("%s: exit %d, stderr %q", name, code, e.err.String())
		}
		if got, _ := e.st().Task(ctx, x.ID); got.External != nil || e.eventCount(x.ID) != n {
			t.Fatalf("%s: a link was written: %+v", name, got.External)
		}
	}

	e := newTrkEnv(t, ghCfg())
	e.tr = &failAfter{Tracker: fake.New(allCaps, []tracker.Issue{base}, nil), n: 1}
	x := e.mkTask("x")
	if code := e.run("task", "link", x.Short, "12"); code != 2 || !strings.Contains(e.err.String(), "link stands") {
		t.Fatalf("exit %d, stderr %q", code, e.err.String())
	}
	got, _ := e.st().Task(ctx, x.ID)
	if got.External == nil || got.External.Key != "12" || got.External.URL != "https://t.example/12" ||
		got.External.Type != "Task" || got.External.ParentKey != "7" || strings.Join(got.External.Owned, ",") != "body,title" {
		t.Fatalf("link: %+v", got.External)
	}
}

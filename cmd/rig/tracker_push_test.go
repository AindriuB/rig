package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/tracker"
	"github.com/AindriuB/rig/internal/tracker/fake"
)

func pushWorkflow(t *testing.T, levels string) string {
	t.Helper()
	push := `,
  "tracker": {"push": {"levels": ` + levels + `, "types": ["Bug"]}}`
	if levels == "" {
		push = ""
	}
	wf := `{
  "name": "pw", "version": 1, "initial": "todo",
  "states": {"todo": {"action": "start"}, "done": {"final": true}},
  "transitions": [{"from": "todo", "to": "done"}],
  "fields": {"type": {"type": "string"}}` + push + `
}`
	path := filepath.Join(t.TempDir(), "pw.json")
	if err := os.WriteFile(path, []byte(wf), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func lvl(n int) *int { return &n }

var pushTypes = []tracker.IssueType{
	{Name: "Epic", Level: lvl(3)}, {Name: "Story", Level: lvl(2)}, {Name: "Subtask", Level: lvl(1)}, {Name: "Bug"},
}

var pushCaps = tracker.Caps{Parent: true, Children: true, Types: true, Search: true, Create: true}

type pushEnv struct {
	*trkEnv
	fk *fake.Tracker
}

func newPushEnv(t *testing.T, caps tracker.Caps) *pushEnv {
	t.Helper()
	e := &pushEnv{trkEnv: newTrkEnv(t, ghCfg())}
	e.envVars["RIG_WORKFLOW"] = pushWorkflow(t, `["Epic","Story","Subtask"]`)
	e.fk = fake.New(caps, nil, nil)
	e.fk.Types = pushTypes
	e.tr = e.fk
	return e
}

// task makes a task, under parent when given, and returns its short id.
func (e *pushEnv) task(slug, parent string, fields ...string) string {
	e.t.Helper()
	args := []string{"task", "new", "--slug", slug, "--title", "Title " + slug, "--body", "Body " + slug}
	for _, f := range fields {
		args = append(args, "--field", f)
	}
	var v struct{ Short string }
	if code := e.json(&v, args...); code != 0 {
		e.t.Fatalf("task new %s: %d %s", slug, code, e.err.String())
	}
	if parent != "" {
		if code := e.run("task", "parent", v.Short, parent); code != 0 {
			e.t.Fatalf("task parent: %d %s", code, e.err.String())
		}
	}
	return v.Short
}

type pushJSON struct {
	DryRun                     bool
	Pushed, Skipped, NotPushed []struct {
		Task, Key, Status, Error, Parent, Type string
		Payload                                map[string]any
	}
}

// tree makes epic > story > subtask and returns their short ids.
func (e *pushEnv) tree() (epic, story, sub string) {
	epic = e.task("epic", "")
	story = e.task("story", epic)
	sub = e.task("sub", story)
	return
}

func (e *pushEnv) linkOf(short string) *store.External {
	e.t.Helper()
	tk, err := e.st().Task(ctx, short)
	if err != nil {
		e.t.Fatal(err)
	}
	return tk.External
}

func (e *pushEnv) intents(short string) int {
	e.t.Helper()
	tk, _ := e.st().Task(ctx, short)
	evs, _ := e.st().Events(ctx, tk.ID)
	n := 0
	for _, ev := range evs {
		if ev.Kind == store.KindTrackerPush {
			n++
		}
	}
	return n
}

// AC1, AC2, AC6: a tree is created parent first, linked with a snapshot that
// shows no drift; a re-run creates nothing, and a new child is created alone.
func TestTrackerPushTreeIsParentFirstAndIdempotent(t *testing.T) {
	e := newPushEnv(t, pushCaps)
	epic, story, sub := e.tree()
	if code := e.run("tracker", "push", epic, "--tree"); code != 0 {
		t.Fatalf("exit %d: %s", code, e.err.String())
	}
	c := e.fk.Created
	if len(c) != 3 || c[0].Type != "Epic" || c[1].Type != "Story" || c[2].Type != "Subtask" {
		t.Fatalf("created %+v", c)
	}
	if c[0].ParentKey != "" || c[1].ParentKey != e.linkOf(epic).Key || c[2].ParentKey != e.linkOf(story).Key {
		t.Fatalf("parents %+v", c)
	}
	for i, short := range []string{epic, story, sub} {
		x := e.linkOf(short)
		if x == nil || x.Kind != "github" || !slices.Equal(x.Owned, []string{"body", "title"}) || x.Rev == "" {
			t.Fatalf("link %d: %+v", i, x)
		}
		want := tracker.NewIssue{Type: c[i].Type, Title: "Title " + []string{"epic", "story", "sub"}[i], Body: "Body " + []string{"epic", "story", "sub"}[i],
			Labels: []string{"rig-" + short}, ParentKey: c[i].ParentKey}
		if c[i].Title != want.Title || c[i].Body != want.Body || !slices.Equal(c[i].Labels, want.Labels) || len(c[i].Acceptance) != 0 {
			t.Fatalf("payload %d: %+v", i, c[i])
		}
	}
	var refresh refreshOut
	if code := e.json(&refresh, "tracker", "refresh", "--all", "--dry-run"); code != 0 {
		t.Fatalf("refresh: %d %s", code, e.err.String())
	}
	for _, r := range refresh.Tasks {
		if r.Status != "unchanged" {
			t.Fatalf("drift after push: %+v", r)
		}
	}

	if code := e.run("tracker", "push", epic, "--tree"); code != 0 || len(e.fk.Created) != 3 {
		t.Fatalf("re-run: exit %d, %d creates", code, len(e.fk.Created))
	}
	sib := e.task("sib", story)
	if code := e.run("tracker", "push", epic, "--tree"); code != 0 {
		t.Fatalf("new child: %d %s", code, e.err.String())
	}
	if len(e.fk.Created) != 4 || e.fk.Created[3].ParentKey != e.linkOf(story).Key || e.linkOf(sib) == nil {
		t.Fatalf("created %+v", e.fk.Created)
	}
}

// AC3: every refusal exits 1, names the task and creates nothing, even when
// only the last task is invalid.
func TestTrackerPushRefusals(t *testing.T) {
	t.Run("type not allowed", func(t *testing.T) {
		e := newPushEnv(t, pushCaps)
		epic := e.task("epic", "")
		bad := e.task("bad", epic, "type=Nope")
		if code := e.run("tracker", "push", epic, "--tree"); code != 1 || !strings.Contains(e.err.String(), bad) || len(e.fk.Created) != 0 {
			t.Fatalf("exit %d, creates %d, %s", code, len(e.fk.Created), e.err.String())
		}
	})
	t.Run("depth", func(t *testing.T) {
		e := newPushEnv(t, pushCaps)
		_, _, sub := e.tree()
		deep := e.task("deep", sub)
		if code := e.run("tracker", "push", e.rootOf(), "--tree"); code != 1 || !strings.Contains(e.err.String(), deep) || len(e.fk.Created) != 0 {
			t.Fatalf("exit %d, creates %d, %s", code, len(e.fk.Created), e.err.String())
		}
	})
	t.Run("no levels", func(t *testing.T) {
		e := newPushEnv(t, pushCaps)
		e.envVars["RIG_WORKFLOW"] = pushWorkflow(t, "")
		epic := e.task("epic", "")
		if code := e.run("tracker", "push", epic); code != 1 || !strings.Contains(e.err.String(), epic) || len(e.fk.Created) != 0 {
			t.Fatalf("exit %d, creates %d, %s", code, len(e.fk.Created), e.err.String())
		}
	})
	t.Run("unknown tracker type", func(t *testing.T) {
		e := newPushEnv(t, pushCaps)
		e.fk.Types = pushTypes[:2]
		epic, _, sub := e.tree()
		if code := e.run("tracker", "push", epic, "--tree"); code != 1 || !strings.Contains(e.err.String(), sub) || len(e.fk.Created) != 0 {
			t.Fatalf("exit %d, creates %d, %s", code, len(e.fk.Created), e.err.String())
		}
	})
	t.Run("child level not below parent", func(t *testing.T) {
		e := newPushEnv(t, pushCaps)
		e.fk.Types = []tracker.IssueType{{Name: "Epic", Level: lvl(1)}, {Name: "Story", Level: lvl(2)}, {Name: "Subtask", Level: lvl(1)}}
		epic, story, _ := e.tree()
		if code := e.run("tracker", "push", epic, "--tree"); code != 1 || !strings.Contains(e.err.String(), story) || len(e.fk.Created) != 0 {
			t.Fatalf("exit %d, creates %d, %s", code, len(e.fk.Created), e.err.String())
		}
	})
	t.Run("types unsupported sends no type", func(t *testing.T) {
		e := newPushEnv(t, pushCaps)
		e.fk.Types = nil
		epic, _, _ := e.tree()
		if code := e.run("tracker", "push", epic, "--tree"); code != 0 || len(e.fk.Created) != 3 || e.fk.Created[0].Type != "" ||
			strings.Count(e.err.String(), "warning") != 1 {
			t.Fatalf("exit %d, %+v, %s", code, e.fk.Created, e.err.String())
		}
	})
}

// rootOf is the short id of the oldest task.
func (e *pushEnv) rootOf() string {
	tasks, _ := e.st().Tasks(ctx, store.TaskFilter{})
	return tasks[len(tasks)-1].Short
}

// AC4: the second create fails: the first stays linked, exit 2, the output
// lists both groups, and a re-run completes the rest.
func TestTrackerPushPartialFailure(t *testing.T) {
	e := newPushEnv(t, pushCaps)
	epic, story, sub := e.tree()
	e.fk.FailCreateN = 2
	var out pushJSON
	if code := e.json(&out, "tracker", "push", epic, "--tree"); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if len(out.Pushed) != 1 || out.Pushed[0].Task != epic || len(out.NotPushed) != 2 || out.NotPushed[0].Task != story || out.NotPushed[1].Task != sub {
		t.Fatalf("%+v", out)
	}
	if e.linkOf(epic) == nil || e.linkOf(story) != nil {
		t.Fatal("links after the failure")
	}
	if code := e.run("tracker", "push", epic, "--tree"); code != 2 && code != 0 {
		t.Fatalf("re-run exit %d", code)
	}
	// The failed create left an intent and no issue: the search finds nothing,
	// and the create goes ahead.
	if e.linkOf(story) == nil || e.linkOf(sub) == nil || len(e.fk.Created) != 3 {
		t.Fatalf("re-run: %s, %d creates", e.err.String(), len(e.fk.Created))
	}
}

// AC5: an intent with no link finds the issue by its marker and links it; a
// tracker that cannot search stops the push with no create.
func TestTrackerPushIntentSearchesMarker(t *testing.T) {
	e := newPushEnv(t, pushCaps)
	epic := e.task("epic", "")
	e.fk.FailCreateN = 1
	if code := e.run("tracker", "push", epic); code != 2 || e.intents(epic) != 1 {
		t.Fatalf("exit %d, intents %d", code, e.intents(epic))
	}
	e.fk.FailCreateN = 0
	made := issue("77", "Made earlier")
	made.Labels = []string{"rig-" + epic}
	e.fk = fake.New(pushCaps, []tracker.Issue{made}, nil)
	e.fk.Types = pushTypes
	e.tr = e.fk
	if code := e.run("tracker", "push", epic); code != 0 {
		t.Fatalf("exit %d: %s", code, e.err.String())
	}
	if x := e.linkOf(epic); x == nil || x.Key != "77" || len(e.fk.Created) != 0 || e.intents(epic) != 1 {
		t.Fatalf("link %+v, creates %d", x, len(e.fk.Created))
	}

	e2 := newPushEnv(t, tracker.Caps{Create: true, Types: true})
	epic2 := e2.task("epic", "")
	e2.fk.FailCreateN = 1
	e2.run("tracker", "push", epic2)
	e2.fk.FailCreateN = 0
	if code := e2.run("tracker", "push", epic2); code != 2 || len(e2.fk.Created) != 0 || e2.linkOf(epic2) != nil {
		t.Fatalf("exit %d, creates %d", code, len(e2.fk.Created))
	}
}

// AC7: a dry run writes nothing and shows the payload; --json lists the
// outcomes.
func TestTrackerPushDryRunAndJSON(t *testing.T) {
	e := newPushEnv(t, pushCaps)
	epic, story, _ := e.tree()
	before := e.eventsByTask()
	var out pushJSON
	if code := e.json(&out, "tracker", "push", epic, "--tree", "--dry-run"); code != 0 {
		t.Fatalf("exit %d: %s", code, e.err.String())
	}
	if len(e.fk.Created) != 0 || len(e.newEvents(before)) != 0 || !out.DryRun || len(out.Pushed) != 3 {
		t.Fatalf("created %d, %+v", len(e.fk.Created), out)
	}
	if p := out.Pushed[1]; p.Task != story || p.Type != "Story" || p.Payload["title"] != "Title story" || p.Payload["type"] != "Story" || !strings.Contains(p.Parent, epic) {
		t.Fatalf("%+v", p)
	}
	if code := e.run("tracker", "push", epic, "--tree", "--dry-run"); code != 0 || !strings.Contains(e.out.String(), "payload:") {
		t.Fatalf("text: %d %q", code, e.out.String())
	}

	if code := e.json(&out, "tracker", "push", epic); code != 0 {
		t.Fatalf("push root: %d", code)
	}
	out = pushJSON{}
	if code := e.json(&out, "tracker", "push", epic, "--tree"); code != 0 || len(out.Skipped) != 1 || out.Skipped[0].Task != epic || out.Skipped[0].Key == "" || len(out.Pushed) != 2 {
		t.Fatalf("%d %+v", code, out)
	}
}

// AC8: a tracker.url that only a tracked .rig/config.json supplies is refused,
// as pull refuses it, with no adapter built.
func TestTrackerPushRefusesTrackedTrackerURL(t *testing.T) {
	e := newPushEnv(t, pushCaps)
	e.globalConfig(map[string]any{"tracker": map[string]any{"kind": "jira", "project": "RIG"}})
	e.localTrackerURL("https://evil.example")
	gitc(t, e.repo, "add", ".rig/config.json")
	e.envVars["RIG_WORKFLOW"] = pushWorkflow(t, `["Epic"]`)
	task := e.task("epic", "")
	pull := e.run("tracker", "pull", "X-1")
	pullErr := e.err.String()
	if code := e.run("tracker", "push", task); code != pull || code == 0 || e.err.String() != strings.Replace(pullErr, "tracker pull", "tracker push", 1) || len(e.specs) != 0 {
		t.Fatalf("push exit %d (pull %d)\n%s\nvs\n%s", code, pull, e.err.String(), pullErr)
	}
}

// createsThenFails creates the issue and then reports that a follow-up read
// failed: it returns the Key and URL with an error and no Rev.
type createsThenFails struct{ *fake.Tracker }

func (c createsThenFails) Create(ctx context.Context, ni tracker.NewIssue) (tracker.Issue, error) {
	is, err := c.Tracker.Create(ctx, ni)
	if err != nil {
		return is, err
	}
	return tracker.Issue{Key: is.Key, URL: is.URL}, errors.New("read back failed")
}

// AC10: a create that returns a Key with an error links the task, exits 2
// and reports created-with-error; no snapshot without a Rev; a re-run creates
// nothing for it.
func TestTrackerPushCreateWithErrorLinks(t *testing.T) {
	e := newPushEnv(t, pushCaps)
	epic, story, _ := e.tree()
	e.tr = createsThenFails{e.fk}
	var out pushJSON
	if code := e.json(&out, "tracker", "push", epic, "--tree"); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if len(out.Pushed) != 1 || out.Pushed[0].Status != "created-with-error" || out.Pushed[0].Key == "" || len(out.NotPushed) != 2 || out.NotPushed[0].Task != story {
		t.Fatalf("%+v", out)
	}
	x := e.linkOf(epic)
	if x == nil || x.Key != out.Pushed[0].Key || x.Rev != "" || x.Snapshot != nil {
		t.Fatalf("link %+v", x)
	}
	e.tr = e.fk
	if code := e.run("tracker", "push", epic, "--tree"); code != 0 || len(e.fk.Created) != 3 {
		t.Fatalf("re-run: exit %d, %d creates, %s", code, len(e.fk.Created), e.err.String())
	}
	if e.linkOf(epic).Key != out.Pushed[0].Key {
		t.Fatal("the first task was created again")
	}
}

// Acceptance comes only from the field the workflow maps to the acceptance
// attribute, as a list or as lines of text.
func TestAcceptanceOf(t *testing.T) {
	e := newPushEnv(t, pushCaps)
	w, err := e.a.openWork(ctx, e.repo, true)
	if err != nil {
		t.Fatal(err)
	}
	task := store.Task{Fields: map[string]any{"ac": []any{"x"}}}
	if got := acceptanceOf(w.wf, task); got != nil {
		t.Fatalf("no map: %v", got)
	}
	w.wf.Tracker.Map = map[string]string{"title": "title", "ac": "acceptance"}
	if got := acceptanceOf(w.wf, task); !slices.Equal(got, []string{"x"}) {
		t.Fatalf("list: %v", got)
	}
	task.Fields["ac"] = "a\n\n b \n"
	if got := acceptanceOf(w.wf, task); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("text: %v", got)
	}
}

// AC9: the help lists the command.
func TestTrackerPushInHelp(t *testing.T) {
	if r := rig(t, t.TempDir(), nil, nil, "help"); r.code != 0 || !strings.Contains(r.out, "rig tracker push <task>") {
		t.Fatalf("help: %+v", r)
	}
}

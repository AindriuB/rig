package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AindriuB/rig/internal/secret"
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
	// ids maps a slug made by task to its full ULID.
	ids map[string]string
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
	var v struct{ Short, ID string }
	if code := e.json(&v, args...); code != 0 {
		e.t.Fatalf("task new %s: %d %s", slug, code, e.err.String())
	}
	if e.ids == nil {
		e.ids = map[string]string{}
	}
	e.ids[slug] = v.ID
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

// marker is the label a pushed task made with this slug carries, computed
// here from the task's ULID without calling pushMarker.
func (e *pushEnv) marker(slug string) string {
	id := e.ids[slug]
	if id == "" {
		e.t.Fatalf("no task made with slug %q", slug)
	}
	sum := sha256.Sum256([]byte(id))
	return "rig/" + pushLabelPart(filepath.Base(e.repo), 24) + "/" + slug + "-" + hex.EncodeToString(sum[:4])
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
		var d store.TrackerPushData
		if ev.Kind == store.KindTrackerPush && json.Unmarshal(ev.Data, &d) == nil && d.Key == "" {
			n++
		}
	}
	return n
}

// externals counts the task's external (link) events.
func (e *pushEnv) externals(short string) int {
	e.t.Helper()
	tk, _ := e.st().Task(ctx, short)
	evs, _ := e.st().Events(ctx, tk.ID)
	n := 0
	for _, ev := range evs {
		if ev.Kind == store.KindExternal {
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
		slug := []string{"epic", "story", "sub"}[i]
		x := e.linkOf(short)
		if x == nil || x.Kind != "github" || !slices.Equal(x.Owned, []string{"body", "title"}) || x.Rev == "" {
			t.Fatalf("link %d: %+v", i, x)
		}
		want := tracker.NewIssue{Type: c[i].Type, Title: "Title " + []string{"epic", "story", "sub"}[i], Body: "Body " + []string{"epic", "story", "sub"}[i],
			Labels: []string{e.marker(slug)}, ParentKey: c[i].ParentKey}
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
	// Inside pushSettle the tracker may not list an earlier issue yet: stop.
	if code := e.run("tracker", "push", epic, "--tree"); code != 2 || len(e.fk.Created) != 1 {
		t.Fatalf("re-run inside pushSettle: exit %d, %d creates", code, len(e.fk.Created))
	}
	// Past it, the failed create's intent found no issue: the create goes ahead.
	base := e.a.now()
	e.a.now = func() time.Time { return base.Add(pushSettle + time.Minute) }
	if code := e.run("tracker", "push", epic, "--tree"); code != 0 {
		t.Fatalf("re-run exit %d: %s", code, e.err.String())
	}
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
	made.Labels = []string{e.marker("epic")}
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

	// Text mode reports the same outcome.
	e2 := newPushEnv(t, pushCaps)
	epic2, _, _ := e2.tree()
	e2.tr = createsThenFails{e2.fk}
	e2.out.Reset()
	if code := e2.run("tracker", "push", epic2, "--tree"); code != 2 || !strings.Contains(e2.out.String(), epic2+"  created-with-error") {
		t.Fatalf("text: exit %d %q", code, e2.out.String())
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

// pushAcceptanceWorkflow maps the list field "ac" to the acceptance attribute
// when mapped is set.
func pushAcceptanceWorkflow(t *testing.T, mapped bool) string {
	t.Helper()
	m := `"tracker": {"push": {"levels": ["Epic"]}}`
	if mapped {
		m = `"tracker": {"push": {"levels": ["Epic"]}, "map": {"title": "title", "body": "description", "ac": "acceptance"}}`
	}
	wf := `{
  "name": "pw", "version": 1, "initial": "todo",
  "states": {"todo": {"action": "start"}, "done": {"final": true}},
  "transitions": [{"from": "todo", "to": "done"}],
  "fields": {"ac": {"type": "list"}},
  "findings": {"severities": ["hi"]},
  ` + m + `
}`
	path := filepath.Join(t.TempDir(), "pa.json")
	if err := os.WriteFile(path, []byte(wf), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// AC6: acceptance is sent only when tracker.jira.acceptanceField is set and a
// workflow field is mapped to it; findings and notes never appear in a field.
func TestTrackerPushPayloadScope(t *testing.T) {
	jira := map[string]any{"kind": "jira", "url": "https://acme.example.com", "project": "RIG", "jira": map[string]any{"acceptanceField": "customfield_10020"}}
	noAcc := map[string]any{"kind": "jira", "url": "https://acme.example.com", "project": "RIG"}
	for _, tc := range []struct {
		name   string
		cfg    map[string]any
		mapped bool
		want   []string
	}{
		{"field set and mapped", jira, true, []string{"first", "second"}},
		{"field not set", noAcc, true, nil},
		{"not mapped", jira, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &pushEnv{trkEnv: newTrkEnv(t, tc.cfg)}
			e.stores[storeKeychain].Set(ctx, "jira:acme.example.com", secret.Secret{User: "me@example.com", Token: "t"})
			e.envVars["RIG_WORKFLOW"] = pushAcceptanceWorkflow(t, tc.mapped)
			e.fk = fake.New(pushCaps, nil, nil)
			e.fk.Types = []tracker.IssueType{{Name: "Epic"}}
			e.tr = e.fk
			epic := e.task("epic", "", `ac=["first","second"]`)
			if code := e.run("finding", "add", epic, "--severity", "hi", "--summary", "FINDING-SUMMARY"); code != 0 {
				t.Fatalf("finding: %d %s", code, e.err.String())
			}
			if code := e.run("note", "add", epic, "--kind", "decision", "--text", "NOTE-TEXT"); code != 0 {
				t.Fatalf("note: %d %s", code, e.err.String())
			}
			if code := e.run("tracker", "push", epic); code != 0 || len(e.fk.Created) != 1 {
				t.Fatalf("exit %d, creates %d: %s", code, len(e.fk.Created), e.err.String())
			}
			ni := e.fk.Created[0]
			if !slices.Equal(ni.Acceptance, tc.want) {
				t.Fatalf("acceptance %v, want %v", ni.Acceptance, tc.want)
			}
			if ni.Title != "Title epic" || ni.Body != "Body epic" || ni.Type != "Epic" || ni.ParentKey != "" || !slices.Equal(ni.Labels, []string{e.marker("epic")}) {
				t.Fatalf("payload %+v", ni)
			}
			all := strings.Join(append(append([]string{ni.Type, ni.Title, ni.Body, ni.ParentKey}, ni.Labels...), ni.Acceptance...), "\n")
			if strings.Contains(all, "FINDING-SUMMARY") || strings.Contains(all, "NOTE-TEXT") {
				t.Fatalf("finding or note leaked: %+v", ni)
			}
		})
	}
}

// AC13: a child whose parent task is not linked is refused when pushed alone,
// before any write.
func TestTrackerPushRefusesUnlinkedParent(t *testing.T) {
	e := newPushEnv(t, pushCaps)
	epic, story, _ := e.tree()
	before := e.eventsByTask()
	if code := e.run("tracker", "push", story); code != 1 || len(e.fk.Created) != 0 ||
		!strings.Contains(e.err.String(), epic) || !strings.Contains(e.err.String(), "--tree") || len(e.newEvents(before)) != 0 {
		t.Fatalf("exit %d, creates %d: %s", code, len(e.fk.Created), e.err.String())
	}
	// With the parent linked it goes ahead, under the parent's issue. (Pushed
	// alone it is depth 0, so the tracker's types are left out of it.)
	e.fk.Types = nil
	if code := e.run("tracker", "push", epic); code != 0 {
		t.Fatalf("push parent: %d", code)
	}
	if code := e.run("tracker", "push", story); code != 0 || len(e.fk.Created) != 2 || e.fk.Created[1].ParentKey != e.linkOf(epic).Key {
		t.Fatalf("exit %d, %+v, %s", code, e.fk.Created, e.err.String())
	}
}

// 78-1: a task pushed under a linked parent is typed below the parent's type.
func TestTrackerPushTypesBelowLinkedParent(t *testing.T) {
	e := newPushEnv(t, pushCaps)
	e.envVars["RIG_WORKFLOW"] = pushWorkflow(t, `["Epic","Story"]`)
	e.fk.Types = []tracker.IssueType{{Name: "Epic", Level: lvl(1)}, {Name: "Story", Level: lvl(0)}}
	epic := e.task("epic", "")
	story := e.task("story", epic)
	if code := e.run("tracker", "push", epic); code != 0 {
		t.Fatalf("push epic: %d %s", code, e.err.String())
	}
	if code := e.run("tracker", "push", story); code != 0 || len(e.fk.Created) != 2 || e.fk.Created[1].Type != "Story" {
		t.Fatalf("exit %d, %+v, %s", code, e.fk.Created, e.err.String())
	}
}

// 78-1: a linked parent whose type is not in the levels refuses before any write.
func TestTrackerPushRefusesParentTypeOutsideLevels(t *testing.T) {
	e := newPushEnv(t, pushCaps)
	e.envVars["RIG_WORKFLOW"] = pushWorkflow(t, `["Epic","Story"]`)
	e.fk.Types = []tracker.IssueType{{Name: "Epic", Level: lvl(1)}, {Name: "Story", Level: lvl(0)}, {Name: "Bug"}}
	epic := e.task("epic", "", "type=Bug")
	story := e.task("story", epic)
	if code := e.run("tracker", "push", epic); code != 0 {
		t.Fatalf("push epic: %d %s", code, e.err.String())
	}
	n := len(e.fk.Created)
	if code := e.run("tracker", "push", story); code != 1 || len(e.fk.Created) != n ||
		!strings.Contains(e.err.String(), epic) || !strings.Contains(e.err.String(), `"Bug"`) || !strings.Contains(e.err.String(), "tracker.push.levels") {
		t.Fatalf("exit %d, creates %d: %s", code, len(e.fk.Created), e.err.String())
	}
}

// f-kgy4 (AC2): an untyped root under a linked parent whose type is outside
// the levels refuses before any write.
func TestTrackerPushRefusesUntypedRootUnderUnlistedParentType(t *testing.T) {
	e := newPushEnv(t, pushCaps)
	e.envVars["RIG_WORKFLOW"] = pushWorkflow(t, `["Epic","Story"]`)
	e.fk.Types = []tracker.IssueType{{Name: "Epic", Level: lvl(1)}, {Name: "Story", Level: lvl(0)}, {Name: "Bug"}}
	bug := e.task("bug-report", "", "type=Bug")
	checkout := e.task("checkout", bug)
	if code := e.run("tracker", "push", bug); code != 0 {
		t.Fatalf("push bug: %d %s", code, e.err.String())
	}
	n := len(e.fk.Created)
	if code := e.run("tracker", "push", checkout); code != 1 || len(e.fk.Created) != n ||
		!strings.Contains(e.err.String(), bug) || !strings.Contains(e.err.String(), `"Bug"`) || !strings.Contains(e.err.String(), "tracker.push.levels") {
		t.Fatalf("exit %d, creates %d: %s", code, len(e.fk.Created)-n, e.err.String())
	}
}

// AC6: a root with its own type ignores its linked parent; its children are typed from it.
func TestTrackerPushTypedRootIgnoresLinkedParent(t *testing.T) {
	e := newPushEnv(t, pushCaps)
	wfPath := pushWorkflow(t, `["Epic","Story"]`)
	wf, err := os.ReadFile(wfPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wfPath, []byte(strings.Replace(string(wf), `["Bug"]`, `["Bug","Epic"]`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	e.envVars["RIG_WORKFLOW"] = wfPath
	e.fk.Types = []tracker.IssueType{{Name: "Epic", Level: lvl(1)}, {Name: "Story", Level: lvl(0)}, {Name: "Bug"}}
	bug := e.task("bug-report", "", "type=Bug")
	checkout := e.task("checkout", bug, "type=Epic")
	e.task("checkout-form", checkout)
	if code := e.run("tracker", "push", bug); code != 0 {
		t.Fatalf("push bug: %d %s", code, e.err.String())
	}
	if code := e.run("tracker", "push", checkout, "--tree"); code != 0 || len(e.fk.Created) != 3 ||
		e.fk.Created[1].Type != "Epic" || e.fk.Created[2].Type != "Story" {
		t.Fatalf("exit %d, %+v, %s", code, e.fk.Created, e.err.String())
	}
}

// AC11 (f-1c15): an already linked root takes its level from its own issue, not its parent.
func TestTrackerPushLinkedRootTakesOwnLevel(t *testing.T) {
	e := newPushEnv(t, pushCaps)
	e.envVars["RIG_WORKFLOW"] = pushWorkflow(t, `["Epic","Story"]`)
	e.fk.Types = []tracker.IssueType{{Name: "Epic", Level: lvl(1)}, {Name: "Story", Level: lvl(0)}, {Name: "Bug"}}
	checkout := e.task("checkout", "")
	bug := e.task("bug-report", "", "type=Bug")
	if code := e.run("tracker", "push", checkout); code != 0 {
		t.Fatalf("push checkout: %d %s", code, e.err.String())
	}
	if code := e.run("tracker", "push", bug); code != 0 {
		t.Fatalf("push bug: %d %s", code, e.err.String())
	}
	if code := e.run("task", "parent", checkout, bug); code != 0 {
		t.Fatalf("reparent: %d %s", code, e.err.String())
	}
	e.task("checkout-form", checkout)
	if code := e.run("tracker", "push", checkout, "--tree"); code != 0 || len(e.fk.Created) != 3 ||
		e.fk.Created[2].Type != "Story" {
		t.Fatalf("exit %d, %+v, %s", code, e.fk.Created, e.err.String())
	}
}

// The marker is rig/<repo>/<slug>-<8 hex of SHA-256(ULID)>, within 50 chars.
func TestTrackerPushMarker(t *testing.T) {
	tk := store.Task{ID: "01JD0000000000000000000001", Short: "t-1", Slug: "12-checkout-form"}
	if got := pushRepoName("/x/Shop_App/.git"); got != "shop-app" {
		t.Fatalf("repo %q", got)
	}
	if got := pushRepoName("/x/" + strings.Repeat("a", 30) + "/.git"); len(got) != 24 {
		t.Fatalf("repo %q", got)
	}
	sum := sha256.Sum256([]byte(tk.ID))
	h := hex.EncodeToString(sum[:])[:8]
	if got := pushMarker("shop-app", tk); got != "rig/shop-app/12-checkout-form-"+h {
		t.Fatalf("marker %q", got)
	}
	if a, b := pushMarker("shop", tk), pushMarker("admin", tk); a != "rig/shop/12-checkout-form-"+h || b != "rig/admin/12-checkout-form-"+h {
		t.Fatalf("markers %q %q", a, b)
	}
	// AC1: same slug, same repo, different tasks.
	other := tk
	other.ID = "01JD0000000000000000000002"
	if pushMarker("shop", tk) == pushMarker("shop", other) {
		t.Fatal("two tasks with one slug share a label")
	}
	// AC3: a long slug is cut, ends in -<8 hex>, and is stable.
	tk.Slug = "40-rewrite-the-checkout-form-validation"
	m := pushMarker("storefront-backend", tk)
	if len(m) > 50 || !strings.HasPrefix(m, "rig/storefront-backend/40-rewrite-the-") ||
		!strings.HasSuffix(m, "-"+h) || m != pushMarker("storefront-backend", tk) {
		t.Fatalf("marker %q", m)
	}
}

// gate holds every caller of Capabilities until n have arrived, so that
// racing pushes reach the intent step together.
type gate struct {
	*fake.Tracker
	wg *sync.WaitGroup
}

func (g gate) Capabilities(ctx context.Context) (tracker.Caps, error) {
	g.wg.Done()
	g.wg.Wait()
	return g.Tracker.Capabilities(ctx)
}

// Create is slow, so a push that lost the race would otherwise find the link
// already written. The per-task lock, not the store, is what stops the loser.
func (g gate) Create(ctx context.Context, ni tracker.NewIssue) (tracker.Issue, error) {
	time.Sleep(400 * time.Millisecond)
	return g.Tracker.Create(ctx, ni)
}

// AC14: two pushes of one unlinked task racing make exactly one create.
func TestTrackerPushRaceMakesOneCreate(t *testing.T) {
	for round := 0; round < 2; round++ {
		e := newPushEnv(t, pushCaps)
		epic := e.task("epic", "")
		var wg sync.WaitGroup
		wg.Add(2)
		g := gate{e.fk, &wg}
		codes := make([]int, 2)
		var done sync.WaitGroup
		// The first append waits, holding the store's lock, so that the
		// other push has read the log (no intent yet) before it lands.
		var slow sync.Once
		now := e.a.now
		slowNow := func() time.Time {
			slow.Do(func() { time.Sleep(300 * time.Millisecond) })
			return now()
		}
		for i := range codes {
			a2 := *e.a
			a2.stdout, a2.stderr, a2.now = &bytes.Buffer{}, &bytes.Buffer{}, slowNow
			a2.tracker = func(context.Context, trackerSpec) (tracker.Tracker, error) { return g, nil }
			done.Add(1)
			go func() {
				defer done.Done()
				codes[i] = a2.run(ctx, []string{"tracker", "push", epic})
			}()
		}
		done.Wait()
		if len(e.fk.Created) != 1 {
			t.Fatalf("round %d: %d creates, exits %v", round, len(e.fk.Created), codes)
		}
		if e.intents(epic) != 1 {
			t.Fatalf("round %d: %d intents", round, e.intents(epic))
		}
	}
}

// blockCreate holds Create until release is closed, after signalling entered,
// and counts Search calls.
type blockCreate struct {
	*fake.Tracker
	entered  chan struct{}
	release  chan struct{}
	searches *int32
}

func (b blockCreate) Create(ctx context.Context, ni tracker.NewIssue) (tracker.Issue, error) {
	close(b.entered)
	<-b.release
	return b.Tracker.Create(ctx, ni)
}

func (b blockCreate) Search(ctx context.Context, q tracker.Query) ([]tracker.IssueRef, error) {
	atomic.AddInt32(b.searches, 1)
	return b.Tracker.Search(ctx, q)
}

// AC14 (f-q2sq): push B starts while A is blocked in Create, after A's intent
// is recorded: B exits 2 with the in-flight message, never searches, and only
// A creates.
func TestTrackerPushRefusesWhileAnotherPushIsInFlight(t *testing.T) {
	e := newPushEnv(t, pushCaps)
	epic := e.task("epic", "")
	var searches int32
	bc := blockCreate{e.fk, make(chan struct{}), make(chan struct{}), &searches}
	a1 := *e.a
	a1.stdout, a1.stderr = &bytes.Buffer{}, &bytes.Buffer{}
	a1.tracker = func(context.Context, trackerSpec) (tracker.Tracker, error) { return bc, nil }
	var codeA int
	done := make(chan struct{})
	go func() { defer close(done); codeA = a1.run(ctx, []string{"tracker", "push", epic}) }()
	<-bc.entered
	if e.intents(epic) != 1 {
		t.Fatalf("%d intents", e.intents(epic))
	}
	var errB bytes.Buffer
	a2 := *e.a
	a2.stdout, a2.stderr = &bytes.Buffer{}, &errB
	a2.tracker = func(context.Context, trackerSpec) (tracker.Tracker, error) { return bc, nil }
	codeB := a2.run(ctx, []string{"tracker", "push", epic})
	close(bc.release)
	<-done
	if codeB != 2 || !strings.Contains(errB.String(), "another push of this task is under way") {
		t.Fatalf("B: exit %d: %s", codeB, errB.String())
	}
	if atomic.LoadInt32(&searches) != 0 || len(e.fk.Created) != 1 || codeA != 0 {
		t.Fatalf("searches %d, creates %d, A exit %d", searches, len(e.fk.Created), codeA)
	}
}

// With RIG_PUSH_LOCK_HELPER set to a lock file path, the test binary takes
// that lock, prints "locked" and holds it until stdin closes. Otherwise this
// test does nothing.
func TestTrackerPushLockHelper(t *testing.T) {
	path := os.Getenv("RIG_PUSH_LOCK_HELPER")
	if path == "" {
		t.Skip("child helper")
	}
	l, err := tryPushLockAt(path)
	if err != nil {
		os.Stdout.WriteString("busy: " + err.Error() + "\n")
		os.Exit(3)
	}
	os.Stdout.WriteString("locked\n")
	_, _ = io.Copy(io.Discard, os.Stdin)
	l.release()
	os.Exit(0)
}

// AC15: a child process holds a task's push lock and is killed; a push of the
// task then takes the lock and goes ahead.
func TestTrackerPushLockFreedWhenHolderIsKilled(t *testing.T) {
	e := newPushEnv(t, pushCaps)
	epic := e.task("epic", "")
	tk, err := e.st().Task(ctx, epic)
	if err != nil {
		t.Fatal(err)
	}
	w, err := e.a.openWork(ctx, e.repo, true)
	if err != nil {
		t.Fatal(err)
	}
	common, err := w.repo.CommonDir(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(common, "rig", "push")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^TestTrackerPushLockHelper$")
	cmd.Env = append(os.Environ(), "RIG_PUSH_LOCK_HELPER="+filepath.Join(dir, tk.ID+".lock"))
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if line, _ := bufio.NewReader(out).ReadString('\n'); line != "locked\n" {
		t.Fatalf("child did not take the lock: %q", line)
	}
	if code := e.run("tracker", "push", epic); code != 2 || len(e.fk.Created) != 0 || !strings.Contains(e.err.String(), "another push of this task is under way") {
		t.Fatalf("held: exit %d, %d creates: %s", code, len(e.fk.Created), e.err.String())
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	deadline := time.Now().Add(2 * time.Second)
	for {
		e.err.Reset()
		if code := e.run("tracker", "push", epic); code == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lock not freed: %s", e.err.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(e.fk.Created) != 1 || e.linkOf(epic) == nil {
		t.Fatalf("%d creates", len(e.fk.Created))
	}
}

// AC16: an intent with no link and no search hit: inside pushSettle the push
// stops with no create; past it, it creates once and warns of a duplicate.
func TestTrackerPushSettleWindow(t *testing.T) {
	e := newPushEnv(t, pushCaps)
	epic := e.task("epic", "")
	e.fk.FailCreateN = 1
	if code := e.run("tracker", "push", epic); code != 2 {
		t.Fatalf("exit %d", code)
	}
	e.fk.FailCreateN = 0
	e.err.Reset()
	if code := e.run("tracker", "push", epic); code != 2 || len(e.fk.Created) != 0 || !strings.Contains(e.err.String(), "re-run after") {
		t.Fatalf("inside: exit %d, %d creates: %s", code, len(e.fk.Created), e.err.String())
	}
	base := e.a.now()
	e.a.now = func() time.Time { return base.Add(pushSettle + time.Minute) }
	e.err.Reset()
	if code := e.run("tracker", "push", epic); code != 0 || len(e.fk.Created) != 1 || !strings.Contains(e.err.String(), "duplicate is possible") {
		t.Fatalf("past: exit %d, %d creates: %s", code, len(e.fk.Created), e.err.String())
	}
}

// AC14, AC18: a retry past the window records a fresh intent before it
// creates, so if that create fails too, the window runs from the new attempt.
func TestTrackerPushSettleRunsFromLatestIntent(t *testing.T) {
	e := newPushEnv(t, pushCaps)
	epic := e.task("epic", "")
	e.fk.FailCreateN = 1
	if code := e.run("tracker", "push", epic); code != 2 {
		t.Fatalf("exit %d", code)
	}
	base := e.a.now()
	late := base.Add(pushSettle + time.Minute)
	e.a.now = func() time.Time { return late }
	e.fk.FailCreateN = 2
	if code := e.run("tracker", "push", epic); code != 2 {
		t.Fatalf("second attempt exit %d: %s", code, e.err.String())
	}
	// Past the first intent's window but inside the second's.
	e.a.now = func() time.Time { return late.Add(time.Minute) }
	e.fk.FailCreateN = 0
	e.err.Reset()
	if code := e.run("tracker", "push", epic); code != 2 || len(e.fk.Created) != 0 || !strings.Contains(e.err.String(), "re-run after") {
		t.Fatalf("exit %d, %d creates: %s", code, len(e.fk.Created), e.err.String())
	}
}

// f-49qw (AC2): an untyped child under a typed root whose type is outside the
// levels refuses the whole --tree push before any write.
func TestTrackerPushTreeRefusesUntypedChildUnderUnlistedRootType(t *testing.T) {
	e := newPushEnv(t, pushCaps)
	e.envVars["RIG_WORKFLOW"] = pushWorkflow(t, `["Epic","Story"]`)
	e.fk.Types = []tracker.IssueType{{Name: "Epic", Level: lvl(1)}, {Name: "Story", Level: lvl(0)}, {Name: "Bug"}}
	bug := e.task("bug-report", "", "type=Bug")
	repro := e.task("repro", bug)
	if code := e.run("tracker", "push", bug, "--tree"); code != 1 || len(e.fk.Created) != 0 ||
		!strings.Contains(e.err.String(), repro) || !strings.Contains(e.err.String(), `"Bug"`) {
		t.Fatalf("exit %d, creates %d: %s", code, len(e.fk.Created), e.err.String())
	}
}

// attachEnv makes epic > story, pushes the epic, and arms the fake so the
// story's create (the second) goes wrong.
func attachEnv(t *testing.T) (e *pushEnv, epic, story string) {
	t.Helper()
	e = newPushEnv(t, pushCaps)
	epic = e.task("epic", "")
	story = e.task("story", epic)
	if code := e.run("tracker", "push", epic); code != 0 {
		t.Fatalf("push epic: %d %s", code, e.err.String())
	}
	return
}

func (e *pushEnv) pushJSON(t *testing.T, want int, short string) pushJSON {
	t.Helper()
	var out pushJSON
	e.err.Reset()
	if code := e.json(&out, "tracker", "push", short); code != want {
		t.Fatalf("exit %d, want %d: %s", code, want, e.err.String())
	}
	return out
}

func statusOf(t *testing.T, out pushJSON) string {
	t.Helper()
	if len(out.Pushed) != 1 {
		t.Fatalf("pushed %+v", out)
	}
	return out.Pushed[0].Status
}

// AC1, AC2, AC3: a failed attach is reported, linked without ParentKey, and
// resumed on a later push without a second create.
func TestTrackerPushResumesFailedAttach(t *testing.T) {
	e, epic, story := attachEnv(t)
	e.fk.CreateAttachFailN = 2
	if got := statusOf(t, e.pushJSON(t, 2, story)); got != "created-not-attached" {
		t.Fatalf("status %q", got)
	}
	x := e.linkOf(story)
	if x == nil || x.ParentKey != "" || len(e.fk.Created) != 2 {
		t.Fatalf("link %+v, %d creates", x, len(e.fk.Created))
	}
	// AC3: attach fails again.
	e.fk.AttachErr = errors.New("boom")
	out := e.pushJSON(t, 2, story)
	if got := statusOf(t, out); got != "attach-failed" || len(e.fk.Created) != 2 || len(e.fk.Attached) != 1 || e.linkOf(story).ParentKey != "" {
		t.Fatalf("status %q, %d creates, %d attaches, link %+v", got, len(e.fk.Created), len(e.fk.Attached), e.linkOf(story))
	}
	// AC2: attach works.
	e.fk.AttachErr = nil
	out = e.pushJSON(t, 0, story)
	pk := e.linkOf(epic).Key
	x = e.linkOf(story)
	is, _ := e.fk.Issue(ctx, x.Key)
	if got := statusOf(t, out); got != "attached" || len(e.fk.Created) != 2 || len(e.fk.Attached) != 2 ||
		e.fk.Attached[1].Key != x.Key || e.fk.Attached[1].Parent != pk ||
		is.Parent == nil || is.Parent.Key != pk || x.ParentKey != pk || x.Rev == "" || x.Rev != is.Rev {
		t.Fatalf("status %q, link %+v, issue %+v", got, x, is)
	}
	// Recorded: skipped, no tracker call.
	n := len(e.fk.IssueCalls)
	if out := e.pushJSON(t, 0, story); len(out.Skipped) != 1 || len(e.fk.IssueCalls) != n || len(e.fk.Attached) != 2 {
		t.Fatalf("%+v, %d issue calls", out, len(e.fk.IssueCalls))
	}
}

// AC4, AC5: a read-back failure leaves the issue attached; the next push
// verifies it without create or attach, and the one after skips it.
func TestTrackerPushVerifiesAttachedChild(t *testing.T) {
	e, epic, story := attachEnv(t)
	e.fk.FailCreateReadN = 2
	if got := statusOf(t, e.pushJSON(t, 2, story)); got != "created-with-error" {
		t.Fatalf("status %q", got)
	}
	if x := e.linkOf(story); x.ParentKey != "" || x.Rev != "" || x.Snapshot != nil {
		t.Fatalf("link %+v", x)
	}
	if got := statusOf(t, e.pushJSON(t, 0, story)); got != "verified" {
		t.Fatalf("status %q", got)
	}
	x := e.linkOf(story)
	if len(e.fk.Created) != 2 || len(e.fk.Attached) != 0 || x.ParentKey != e.linkOf(epic).Key || x.Rev == "" || x.Snapshot == nil {
		t.Fatalf("%d creates, %d attaches, link %+v", len(e.fk.Created), len(e.fk.Attached), x)
	}
	n := len(e.fk.IssueCalls)
	if out := e.pushJSON(t, 0, story); len(out.Skipped) != 1 || len(e.fk.IssueCalls) != n {
		t.Fatalf("%+v", out)
	}
}

// AC6: nothing to resume, so skipped with no tracker call.
func TestTrackerPushSkipsWhenNothingToResume(t *testing.T) {
	t.Run("root and clean create", func(t *testing.T) {
		e, epic, story := attachEnv(t)
		if code := e.run("tracker", "push", story); code != 0 {
			t.Fatalf("push: %d", code)
		}
		if x := e.linkOf(story); x.ParentKey != e.linkOf(epic).Key { // AC9
			t.Fatalf("clean create link %+v", x)
		}
		tk, _ := e.st().Task(ctx, story)
		evs, _ := e.st().Events(ctx, tk.ID)
		var rec store.TrackerPushData
		for _, ev := range evs {
			var d store.TrackerPushData
			if ev.Kind == store.KindTrackerPush && json.Unmarshal(ev.Data, &d) == nil && d.Key != "" {
				rec = d
			}
		}
		if rec.Key != e.linkOf(story).Key || rec.Parent != e.linkOf(epic).Key {
			t.Fatalf("keyed record %+v", rec)
		}
		n := len(e.fk.IssueCalls)
		if out := e.pushJSON(t, 0, epic); len(out.Skipped) != 1 || len(out.Pushed) != 0 {
			t.Fatalf("root: %+v", out)
		}
		if out := e.pushJSON(t, 0, story); len(out.Skipped) != 1 || len(out.Pushed) != 0 {
			t.Fatalf("recorded: %+v", out)
		}
		if len(e.fk.IssueCalls) != n {
			t.Fatal("an Issue call was made")
		}
	})
	t.Run("no push intent", func(t *testing.T) {
		e, epic, story := attachEnv(t)
		made, err := e.fk.Create(ctx, tracker.NewIssue{Type: "Story", Title: "x"})
		if err != nil {
			t.Fatal(err)
		}
		if code := e.run("task", "link", story, "github:"+made.Key); code != 0 {
			t.Fatalf("link: %d %s", code, e.err.String())
		}
		_ = epic
		n := len(e.fk.IssueCalls)
		if out := e.pushJSON(t, 0, story); len(out.Skipped) != 1 || len(out.Pushed) != 0 || len(e.fk.IssueCalls) != n || len(e.fk.Attached) != 0 {
			t.Fatalf("%+v", out)
		}
	})
	t.Run("no parent capability", func(t *testing.T) {
		e, _, story := attachEnv(t)
		e.fk.CreateAttachFailN = 2
		e.pushJSON(t, 2, story)
		caps := pushCaps
		caps.Parent = false
		noParent := fake.New(caps, nil, nil)
		e.tr = noParent
		if out := e.pushJSON(t, 0, story); len(out.Skipped) != 1 || len(out.Pushed) != 0 || len(noParent.IssueCalls) != 0 {
			t.Fatalf("%+v", out)
		}
	})
}

// AC7: a child under another parent is reported, never moved.
func TestTrackerPushWrongParent(t *testing.T) {
	e, epic, story := attachEnv(t)
	other := e.task("other", "")
	if code := e.run("tracker", "push", other); code != 0 {
		t.Fatal(e.err.String())
	}
	e.fk.CreateAttachFailN = 3
	e.pushJSON(t, 2, story)
	if err := e.fk.AttachParent(ctx, e.linkOf(story).Key, e.linkOf(other).Key); err != nil {
		t.Fatal(err)
	}
	n := len(e.fk.Attached)
	out := e.pushJSON(t, 2, story)
	if got := statusOf(t, out); got != "wrong-parent" || len(e.fk.Attached) != n ||
		!strings.Contains(out.Pushed[0].Error, e.linkOf(other).Key) || !strings.Contains(out.Pushed[0].Error, e.linkOf(epic).Key) {
		t.Fatalf("%+v", out)
	}
}

// AC8: a tracker that cannot attach says what to do.
func TestTrackerPushNoAttacher(t *testing.T) {
	e, _, story := attachEnv(t)
	e.fk.CreateAttachFailN = 2
	e.pushJSON(t, 2, story)
	e.tr = fake.Plain{Tracker: e.fk}
	out := e.pushJSON(t, 2, story)
	if got := statusOf(t, out); got != "attach-failed" || !strings.Contains(out.Pushed[0].Error, "on the tracker, then re-run") || len(e.fk.Attached) != 0 {
		t.Fatalf("%+v", out)
	}
}

// AC10: a dry run names the resume and calls nothing.
func TestTrackerPushDryRunWouldVerify(t *testing.T) {
	e, _, story := attachEnv(t)
	e.fk.CreateAttachFailN = 2
	e.pushJSON(t, 2, story)
	n := len(e.fk.IssueCalls)
	var out pushJSON
	if code := e.json(&out, "tracker", "push", story, "--dry-run"); code != 0 || statusOf(t, out) != "would-verify" ||
		len(e.fk.IssueCalls) != n || len(e.fk.Attached) != 0 || len(e.fk.Created) != 2 {
		t.Fatalf("%d %+v", code, out)
	}
}

// handIssue makes an issue on the tracker, under parent when given, outside
// any push.
func (e *pushEnv) handIssue(t *testing.T, parent string) string {
	t.Helper()
	made, err := e.fk.Create(ctx, tracker.NewIssue{Type: "Story", Title: "hand made", ParentKey: parent})
	if err != nil {
		t.Fatal(err)
	}
	return made.Key
}

// AC13 (f-csb0): a child pushed to created-not-attached, then relinked by hand
// to another issue, is skipped: push owns only the issue it linked.
func TestTrackerPushHandRelinkAfterNotAttachedIsSkipped(t *testing.T) {
	for _, tc := range []struct {
		name        string
		otherParent bool
	}{{"no parent", false}, {"under another parent", true}} {
		t.Run(tc.name, func(t *testing.T) {
			e, _, story := attachEnv(t)
			other := e.task("other", "")
			if code := e.run("tracker", "push", other); code != 0 {
				t.Fatal(e.err.String())
			}
			e.fk.CreateAttachFailN = 3
			if got := statusOf(t, e.pushJSON(t, 2, story)); got != "created-not-attached" {
				t.Fatalf("status %q", got)
			}
			parent := ""
			if tc.otherParent {
				parent = e.linkOf(other).Key
			}
			hand := e.handIssue(t, parent)
			if code := e.run("task", "link", story, "github:"+hand); code != 0 {
				t.Fatalf("link: %d %s", code, e.err.String())
			}
			n, att := len(e.fk.IssueCalls), len(e.fk.Attached)
			out := e.pushJSON(t, 0, story)
			if len(out.Skipped) != 1 || len(out.Pushed) != 0 || out.Skipped[0].Status != "skipped" ||
				len(e.fk.Attached) != att || len(e.fk.IssueCalls) != n {
				t.Fatalf("%+v, %d attaches, %d issue calls", out, len(e.fk.Attached)-att, len(e.fk.IssueCalls)-n)
			}
		})
	}
}

// AC14 (f-2gs2): an intent whose Create failed with no key does not make a
// later hand link push's own.
func TestTrackerPushHandLinkAfterFailedCreateIsSkipped(t *testing.T) {
	e, _, story := attachEnv(t)
	e.fk.FailCreateN = 2
	if code := e.run("tracker", "push", story); code != 2 || e.intents(story) != 1 {
		t.Fatalf("exit %d, intents %d", code, e.intents(story))
	}
	e.fk.FailCreateN = 0
	hand := e.handIssue(t, "")
	if code := e.run("task", "link", story, "github:"+hand); code != 0 {
		t.Fatalf("link: %d %s", code, e.err.String())
	}
	n, att := len(e.fk.IssueCalls), len(e.fk.Attached)
	out := e.pushJSON(t, 0, story)
	if len(out.Skipped) != 1 || len(out.Pushed) != 0 || len(e.fk.Attached) != att || len(e.fk.IssueCalls) != n {
		t.Fatalf("%+v", out)
	}
}

// AC15 (f-b8wk): a resume writes no external event, so a later report with
// nothing changed re-sends neither status nor comment.
func TestTrackerPushResumeKeepsReportHistory(t *testing.T) {
	caps := pushCaps
	caps.Comment, caps.SetStatus = true, true
	e := newPushEnv(t, caps)
	wf := `{
  "name": "pr", "version": 1, "initial": "todo",
  "states": {"todo": {"action": "start"}, "doing": {"action": "review"}, "done": {"final": true}},
  "transitions": [{"from": "todo", "to": "doing"}, {"from": "doing", "to": "done"}],
  "fields": {"type": {"type": "string"}},
  "tracker": {"push": {"levels": ["Epic","Story","Subtask"], "types": ["Bug"]}, "report": {"status": {"doing": "In Progress"}}}
}`
	path := filepath.Join(t.TempDir(), "pr.json")
	if err := os.WriteFile(path, []byte(wf), 0o644); err != nil {
		t.Fatal(err)
	}
	e.envVars["RIG_WORKFLOW"] = path
	e.fk = fake.New(caps, nil, []string{"In Progress"})
	e.fk.Types = pushTypes
	e.tr = e.fk
	epic := e.task("epic", "")
	story := e.task("story", epic)
	if code := e.run("tracker", "push", epic); code != 0 {
		t.Fatalf("push epic: %d %s", code, e.err.String())
	}
	e.fk.CreateAttachFailN = 2
	e.pushJSON(t, 2, story)
	if code := e.run("task", "move", story, "doing"); code != 0 {
		t.Fatalf("move: %d %s", code, e.err.String())
	}
	if code := e.run("tracker", "report", story); code != 0 {
		t.Fatalf("report: %d %s", code, e.err.String())
	}
	if len(e.fk.StatusCalls) != 1 || len(e.fk.Comments) != 1 {
		t.Fatalf("setup: %d statuses, %d comments", len(e.fk.StatusCalls), len(e.fk.Comments))
	}
	links := e.externals(story)
	if got := statusOf(t, e.pushJSON(t, 0, story)); got != "attached" {
		t.Fatalf("status %q", got)
	}
	if e.externals(story) != links {
		t.Fatal("the resume wrote an external event")
	}
	if code := e.run("tracker", "report", story); code != 0 {
		t.Fatalf("second report: %d %s", code, e.err.String())
	}
	if len(e.fk.StatusCalls) != 1 || len(e.fk.Comments) != 1 {
		t.Fatalf("re-sent: %d statuses, %d comments", len(e.fk.StatusCalls), len(e.fk.Comments))
	}
}

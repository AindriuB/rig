package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/store/memstore"
)

// init makes the test binary double as a scripted editor: with
// RIG_TASK_EDIT_HELPER set it edits the file named by its last argument and
// exits, before any test runs. RIG_EDIT_WRITE replaces the whole file;
// RIG_EDIT_FROM is replaced by RIG_EDIT_TO. No shell is involved.
func init() {
	if os.Getenv("RIG_TASK_EDIT_HELPER") == "" {
		return
	}
	path := os.Args[len(os.Args)-1]
	b, err := os.ReadFile(path)
	if err != nil {
		os.Exit(97)
	}
	if w, ok := os.LookupEnv("RIG_EDIT_WRITE"); ok {
		b = []byte(w)
	}
	if from := os.Getenv("RIG_EDIT_FROM"); from != "" {
		b = []byte(strings.Replace(string(b), from, os.Getenv("RIG_EDIT_TO"), 1))
	}
	if os.WriteFile(path, b, 0o600) != nil {
		os.Exit(98)
	}
	os.Exit(0)
}

// taskWorkflow has a sha-bound state, a final state, a counter, typed fields
// and a hook that always fails.
func taskWorkflow(t *testing.T) string {
	t.Helper()
	wf := `{
  "name": "tw", "version": 3, "initial": "open", "branch": "task/{short}-{slug}",
  "states": {
    "open":    { "action": "start" },
    "bound":   { "action": "finish", "shaBound": true },
    "back":    { "action": "rebind" },
    "done":    { "final": true },
    "stopped": { "action": "decide", "who": "human" }
  },
  "onNewCommit": "back",
  "transitions": [
    { "from": "open", "to": "bound", "takes": "sha", "gates": [{ "shaIsHead": true }] },
    { "from": "bound", "to": "back" },
    { "from": "back", "to": "bound", "takes": "sha" },
    { "from": ["open", "bound"], "to": "done", "gates": [{ "fieldSet": "owner" }] },
    { "from": "*", "to": "stopped", "takes": "reason", "count": "rounds" },
    { "from": "stopped", "to": "$previous" }
  ],
  "fields": {
    "owner": { "type": "string" },
    "tags":  { "type": "list" },
    "size":  { "type": "int" },
    "kind":  { "type": "enum", "values": ["a", "b"] },
    "notes": { "type": "text" }
  },
  "findings": { "severities": ["P1"] },
  "counters": ["rounds"],
  "hooks": [{ "on": "enter:stopped", "run": ["rig-no-such-hook-program", "{short}"] }]
}`
	path := filepath.Join(t.TempDir(), "tw.json")
	if err := os.WriteFile(path, []byte(wf), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// taskEnv is a memstore-backed app under taskWorkflow in a fresh repository.
type taskEnv struct {
	t    *testing.T
	st   *memstore.Store
	repo string
	env  map[string]string
	// before, when set, runs once just before the next Update the command makes.
	before func()
}

// hookStore runs hook once, just before the first Update reaches the store.
type hookStore struct {
	store.Store
	hook func()
}

func (h *hookStore) Update(ctx context.Context, ref string, next func(store.Task, []store.Finding) ([]store.Event, error)) (store.Task, error) {
	if h.hook != nil {
		f := h.hook
		h.hook = nil
		f()
	}
	return h.Store.Update(ctx, ref, next)
}

// setUnderneath writes a field value straight to the store, as a concurrent
// rig would.
func (e *taskEnv) setUnderneath(ref, name string, v any) {
	e.t.Helper()
	ev, err := store.NewEvent(store.KindField, store.FieldData{Name: name, Value: v})
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.st.Update(ctx, ref, func(store.Task, []store.Finding) ([]store.Event, error) {
		return []store.Event{ev}, nil
	}); err != nil {
		e.t.Fatal(err)
	}
}

func newTaskEnv(t *testing.T) *taskEnv {
	t.Helper()
	repo, _ := newRepo(t)
	return &taskEnv{t: t, st: memstore.New(store.Options{Now: tick()}), repo: repo,
		env: map[string]string{"RIG_WORKFLOW": taskWorkflow(t)}}
}

// rig runs `rig <args>` and returns its result.
func (e *taskEnv) rig(args ...string) result {
	e.t.Helper()
	var out, errOut bytes.Buffer
	var st store.Store = e.st
	if e.before != nil {
		st = &hookStore{Store: e.st, hook: e.before}
		e.before = nil
	}
	a := &app{
		stdout: &out, stderr: &errOut, cwd: e.repo,
		getenv: func(k string) string { return e.env[k] },
		store:  func(context.Context, string) (store.Store, error) { return st, nil },
	}
	code := a.run(ctx, args)
	return result{code, out.String(), errOut.String()}
}

// newTask creates a task through the command and returns its JSON.
func (e *taskEnv) newTask(slug string, extra ...string) map[string]any {
	e.t.Helper()
	r := e.rig(append([]string{"task", "new", "--slug", slug, "--title", "Title " + slug, "--json"}, extra...)...)
	if r.code != 0 {
		e.t.Fatalf("task new: %+v", r)
	}
	return decodeObj(e.t, r.out)
}

func decodeObj(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, s)
	}
	return m
}

func keysOf(m map[string]any) []string {
	var k []string
	for n := range m {
		k = append(k, n)
	}
	sort.Strings(k)
	return k
}

// kinds returns the event kinds in a task's log.
func (e *taskEnv) kinds(ref string) []string {
	e.t.Helper()
	evs, err := e.st.Events(ctx, ref)
	if err != nil {
		e.t.Fatal(err)
	}
	var k []string
	for _, ev := range evs {
		k = append(k, ev.Kind)
	}
	return k
}

func countKind(kinds []string, kind string) int {
	n := 0
	for _, k := range kinds {
		if k == kind {
			n++
		}
	}
	return n
}

// AC1: every subcommand and flag; an unknown flag is exit 2.
func TestTaskUnknownFlagAndUsage(t *testing.T) {
	e := newTaskEnv(t)
	e.newTask("a")
	for _, args := range [][]string{
		{"task"},
		{"task", "bogus"},
		{"task", "new", "--slug", "x", "--title", "T", "--frob"},
		{"task", "show", "a", "--frob"},
		{"task", "list", "--frob"},
		{"task", "move", "a", "stopped", "--frob"},
		{"task", "set", "a", "--field", "owner=x", "--frob"},
		{"task", "count", "a", "rounds", "--frob"},
		{"task", "is", "a", "open", "--frob"},
		{"task", "log", "a", "--frob"},
		{"task", "edit", "a", "--frob"},
	} {
		if r := e.rig(args...); r.code != 2 {
			t.Errorf("%v: exit %d, want 2 (%s)", args, r.code, r.errOut)
		}
	}
	if r := e.rig("task", "show", "no-such-task"); r.code != 2 {
		t.Errorf("missing task: exit %d", r.code)
	}
}

func TestTaskNew(t *testing.T) {
	e := newTaskEnv(t)
	got := e.newTask("first", "--body", "Hello", "--field", "owner=ann", "--field", "tags=x,y", "--field", "tags=z", "--field", "size=3", "--field", "kind=b")
	if got["state"] != "open" || got["body"] != "Hello" || got["title"] != "Title first" {
		t.Fatalf("task: %v", got)
	}
	wf := got["workflow"].(map[string]any)
	if wf["name"] != "tw" || wf["version"] != float64(3) {
		t.Fatalf("workflow ref: %v", wf)
	}
	if want := "task/" + got["short"].(string) + "-first"; got["branch"] != want {
		t.Fatalf("branch %v, want %s", got["branch"], want)
	}
	fields := got["fields"].(map[string]any)
	if fields["owner"] != "ann" || fields["size"] != float64(3) || !slices.Equal(anyStrings(fields["tags"]), []string{"x", "y", "z"}) {
		t.Fatalf("fields: %v", fields)
	}

	bf := filepath.Join(t.TempDir(), "body.md")
	os.WriteFile(bf, []byte("from file"), 0o644)
	if g := e.newTask("second", "--body-file", bf); g["body"] != "from file" {
		t.Fatalf("body-file: %v", g["body"])
	}
	big := filepath.Join(t.TempDir(), "big.md")
	os.WriteFile(big, bytes.Repeat([]byte("x"), 64<<10+1), 0o644)
	ok := filepath.Join(t.TempDir(), "ok.md")
	os.WriteFile(ok, bytes.Repeat([]byte("x"), 64<<10), 0o644)
	if r := e.rig("task", "new", "--slug", "big", "--title", "T", "--body-file", ok); r.code != 0 {
		t.Errorf("64 KiB file: %+v", r)
	}
	for name, args := range map[string][]string{
		"too big":       {"--slug", "big2", "--title", "T", "--body-file", big},
		"both bodies":   {"--slug", "b3", "--title", "T", "--body", "x", "--body-file", bf},
		"unknown field": {"--slug", "b4", "--title", "T", "--field", "nope=1"},
		"bad value":     {"--slug", "b5", "--title", "T", "--field", "size=abc"},
		"repeat scalar": {"--slug", "b6", "--title", "T", "--field", "owner=a", "--field", "owner=b"},
		"bad slug":      {"--slug", "Bad_Slug", "--title", "T"},
		"long slug":     {"--slug", strings.Repeat("a", 41), "--title", "T"},
		"no title":      {"--slug", "b7"},
	} {
		if r := e.rig(append([]string{"task", "new"}, args...)...); r.code != 2 {
			t.Errorf("%s: exit %d", name, r.code)
		}
	}
	if r := e.rig("task", "list", "--json"); len(decodeList(t, r.out)) != 3 {
		t.Errorf("only the three valid tasks exist: %s", r.out)
	}
}

func anyStrings(v any) []string {
	var out []string
	for _, e := range v.([]any) {
		out = append(out, e.(string))
	}
	return out
}

func decodeList(t *testing.T, s string) []map[string]any {
	t.Helper()
	var l []map[string]any
	if err := json.Unmarshal([]byte(s), &l); err != nil {
		t.Fatalf("not a JSON array: %v\n%s", err, s)
	}
	return l
}

// AC4: --json field names match docs/phase-2.md "Data model" exactly.
func TestTaskJSONGolden(t *testing.T) {
	e := newTaskEnv(t)
	created := e.newTask("g")
	want := []string{"body", "branch", "counters", "created", "fields", "id", "openFindings", "recordedState",
		"shas", "short", "slug", "state", "stateSha", "title", "updated", "workflow"}
	// pr, headSha and reason are omitted until they have a value.
	if got := keysOf(created); !slices.Equal(got, want) {
		t.Fatalf("new --json keys\n got %v\nwant %v", got, want)
	}
	show := decodeObj(t, e.rig("task", "show", "g", "--json").out)
	if got := keysOf(show); !slices.Equal(got, want) {
		t.Fatalf("show --json keys\n got %v\nwant %v", got, want)
	}
	if got := keysOf(show["workflow"].(map[string]any)); !slices.Equal(got, []string{"name", "version"}) {
		t.Fatalf("workflow keys %v", got)
	}
	l := decodeList(t, e.rig("task", "list", "--json").out)
	if len(l) != 1 || !slices.Equal(keysOf(l[0]), want) {
		t.Fatalf("list --json: %v", l)
	}

	mv := decodeObj(t, e.rig("task", "move", "g", "stopped", "--reason", "why", "--json").out)
	wantMove := append(slices.Clone(want), "hookErrors", "reason")
	sort.Strings(wantMove)
	if got := keysOf(mv); !slices.Equal(got, wantMove) {
		t.Fatalf("move --json keys\n got %v\nwant %v", got, wantMove)
	}

	cnt := decodeObj(t, e.rig("task", "count", "g", "rounds", "--json").out)
	if !slices.Equal(keysOf(cnt), []string{"counter", "task", "value"}) || cnt["value"] != float64(2) || cnt["counter"] != "rounds" {
		t.Fatalf("count --json: %v", cnt)
	}

	evs := decodeList(t, e.rig("task", "log", "g", "--json").out)
	if len(evs) < 3 || !slices.Equal(keysOf(evs[0]), []string{"actor", "data", "id", "kind", "seq", "time", "v"}) {
		t.Fatalf("log --json: %v", evs)
	}
	if evs[0]["kind"] != "created" || evs[1]["kind"] != "transition" {
		t.Fatalf("log not oldest first: %v", evs)
	}
}

// AC2: a gate refusal is exit 1 and writes no event.
func TestTaskMoveRefusal(t *testing.T) {
	e := newTaskEnv(t)
	task := e.newTask("r")
	id := task["id"].(string)
	before := len(e.kinds(id))
	r := e.rig("task", "move", "r", "done")
	if r.code != 1 || !strings.Contains(r.errOut, "fieldSet") {
		t.Fatalf("refused move: %+v", r)
	}
	if r := e.rig("task", "move", "r", "nowhere"); r.code != 1 {
		t.Fatalf("no such transition: exit %d", r.code)
	}
	if len(e.kinds(id)) != before {
		t.Fatalf("a refused move wrote events: %v", e.kinds(id))
	}
	if r := e.rig("task", "set", "r", "--field", "owner=me"); r.code != 0 {
		t.Fatal(r)
	}
	if r := e.rig("task", "move", "r", "done"); r.code != 0 {
		t.Fatalf("after the gate is met: %+v", r)
	}
	if r := e.rig("task", "move", "r", "done", "--pr", "abc"); r.code != 2 {
		t.Fatalf("bad --pr: exit %d", r.code)
	}
}

// A hook failure is on stderr and in hookErrors, exit 0, transition stands.
func TestTaskMoveHookError(t *testing.T) {
	e := newTaskEnv(t)
	e.newTask("h")
	r := e.rig("task", "move", "h", "stopped", "--reason", "need a call", "--json")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	out := decodeObj(t, r.out)
	he, _ := out["hookErrors"].([]any)
	if out["state"] != "stopped" || out["reason"] != "need a call" || len(he) != 1 || !strings.Contains(r.errOut, "hook") {
		t.Fatalf("move: %v\nstderr: %s", out, r.errOut)
	}
	if r := e.rig("task", "move", "h", "open"); r.code != 0 {
		t.Fatalf("back: %+v", r)
	}
}

// AC3: after a new commit a sha-bound task shows onNewCommit, and `is` flips.
func TestTaskShaBound(t *testing.T) {
	e := newTaskEnv(t)
	task := e.newTask("sb")
	branch := task["branch"].(string)
	gitc(t, e.repo, "checkout", "-q", "-b", branch)
	gitc(t, e.repo, "commit", "-q", "--allow-empty", "-m", "one")
	sha := gitc(t, e.repo, "rev-parse", "HEAD")
	if r := e.rig("task", "move", "sb", "bound", "--sha", sha); r.code != 0 {
		t.Fatalf("move: %+v", r)
	}
	if r := e.rig("task", "is", "sb", "bound"); r.code != 0 || r.out != "" {
		t.Fatalf("is bound before: %+v", r)
	}
	gitc(t, e.repo, "commit", "-q", "--allow-empty", "-m", "two")
	if r := e.rig("task", "is", "sb", "bound"); r.code != 1 {
		t.Fatalf("is bound after: exit %d", r.code)
	}
	if r := e.rig("task", "is", "sb", "back"); r.code != 0 {
		t.Fatalf("is back after: exit %d", r.code)
	}
	show := decodeObj(t, e.rig("task", "show", "sb", "--json").out)
	if show["state"] != "back" || show["recordedState"] != "bound" || show["headSha"] == "" || show["stateSha"] != sha {
		t.Fatalf("show: %v", show)
	}
	kinds := e.kinds(task["id"].(string))
	e.rig("task", "is", "sb", "bound")
	if len(e.kinds(task["id"].(string))) != len(kinds) {
		t.Fatal("is wrote an event")
	}
}

func TestTaskSet(t *testing.T) {
	e := newTaskEnv(t)
	task := e.newTask("s", "--field", "owner=ann")
	id := task["id"].(string)
	before := len(e.kinds(id))
	r := e.rig("task", "set", "s", "--field", "owner=bob", "--field", "title=New title", "--field", "tags=a,b", "--json")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	got := decodeObj(t, r.out)
	if got["title"] != "New title" || got["fields"].(map[string]any)["owner"] != "bob" {
		t.Fatalf("set: %v", got)
	}
	if n := countKind(e.kinds(id), "field"); len(e.kinds(id))-before != 3 || n < 3 {
		t.Fatalf("want one field event per changed value: %v", e.kinds(id))
	}
	mid := len(e.kinds(id))
	if r := e.rig("task", "set", "s", "--field", "owner=bob", "--field", "title=New title"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if len(e.kinds(id)) != mid {
		t.Fatalf("unchanged values wrote events: %v", e.kinds(id))
	}
	for _, args := range [][]string{
		{"task", "set", "s", "--field", "nope=1"},
		{"task", "set", "s", "--field", "size=x"},
		{"task", "set", "s", "--field", "title="},
		{"task", "set", "s"},
	} {
		if r := e.rig(args...); r.code != 2 {
			t.Errorf("%v: exit %d", args, r.code)
		}
	}
	if len(e.kinds(id)) != mid {
		t.Fatal("a refused set wrote events")
	}
}

// A set whose field moved after the task was read writes nothing; a field it
// does not change may move freely.
func TestTaskSetRefusesMovedField(t *testing.T) {
	e := newTaskEnv(t)
	task := e.newTask("sc", "--field", "owner=ann")
	id := task["id"].(string)

	e.before = func() { e.setUnderneath(id, "owner", "cat") }
	before := len(e.kinds(id))
	r := e.rig("task", "set", "sc", "--field", "owner=bob")
	if r.code != 1 || !strings.Contains(r.errOut, "owner") || len(e.kinds(id)) != before+1 {
		t.Fatalf("moved field: %+v, kinds %v", r, e.kinds(id))
	}
	if got := decodeObj(t, e.rig("task", "show", "sc", "--json").out); got["fields"].(map[string]any)["owner"] != "cat" {
		t.Fatalf("lost update: %v", got)
	}

	e.before = func() { e.setUnderneath(id, "owner", "dan") }
	r = e.rig("task", "set", "sc", "--field", "title=Other")
	if r.code != 0 {
		t.Fatalf("unrelated field moved: %+v", r)
	}
}

// An edit whose field moved while the editor was open writes nothing and keeps
// the copy.
func TestTaskEditRefusesMovedField(t *testing.T) {
	e := newTaskEnv(t)
	task := e.newTask("ec", "--body", "A")
	id := task["id"].(string)
	e.before = func() { e.setUnderneath(id, "body", "B") }
	before := len(e.kinds(id))
	r := e.editWith(t, map[string]string{"RIG_EDIT_FROM": "A", "RIG_EDIT_TO": "A2"}, "ec")
	if r.code != 1 || !strings.Contains(r.errOut, "body") || len(e.kinds(id)) != before+1 {
		t.Fatalf("moved field: %+v, kinds %v", r, e.kinds(id))
	}
	i := strings.LastIndex(r.errOut, "your copy is at ")
	if i < 0 {
		t.Fatalf("no copy path in %q", r.errOut)
	}
	kept, err := os.ReadFile(strings.TrimSpace(r.errOut[i+len("your copy is at "):]))
	if err != nil || !strings.Contains(string(kept), "A2") {
		t.Fatalf("kept copy %q, %v", kept, err)
	}

	// A field the edit does not change may move.
	e.before = func() { e.setUnderneath(id, "owner", "dan") }
	r = e.editWith(t, map[string]string{"RIG_EDIT_FROM": "B", "RIG_EDIT_TO": "C"}, "ec")
	if r.code != 0 {
		t.Fatalf("unrelated field moved: %+v", r)
	}
}

func TestTaskCountAndLog(t *testing.T) {
	e := newTaskEnv(t)
	e.newTask("c")
	if r := e.rig("task", "count", "c", "rounds"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r := e.rig("task", "count", "c", "nope"); r.code != 2 {
		t.Fatalf("not a counter: exit %d", r.code)
	}
	v := decodeObj(t, e.rig("task", "show", "c", "--json").out)
	if v["counters"].(map[string]any)["rounds"] != float64(1) {
		t.Fatalf("counters: %v", v["counters"])
	}
	r := e.rig("task", "log", "c")
	if r.code != 0 || strings.Count(r.out, "\n") != 2 || !strings.Contains(r.out, "count") {
		t.Fatalf("log text: %+v", r)
	}
}

func TestTaskList(t *testing.T) {
	e := newTaskEnv(t)
	for _, s := range []string{"one", "two", "three"} {
		e.newTask(s, "--field", "owner=o")
	}
	e.rig("task", "move", "two", "done")
	e.rig("task", "move", "three", "stopped", "--reason", "r")
	slugs := func(args ...string) []string {
		r := e.rig(append([]string{"task", "list", "--json"}, args...)...)
		if r.code != 0 {
			t.Fatalf("%v: %+v", args, r)
		}
		var out []string
		for _, m := range decodeList(t, r.out) {
			out = append(out, m["slug"].(string))
		}
		return out
	}
	if got := slugs(); !slices.Equal(got, []string{"three", "one"}) {
		t.Errorf("default (newest first, no final): %v", got)
	}
	if got := slugs("--all"); !slices.Equal(got, []string{"three", "two", "one"}) {
		t.Errorf("--all: %v", got)
	}
	if got := slugs("--state", "done", "--state", "stopped"); !slices.Equal(got, []string{"three", "two"}) {
		t.Errorf("--state: %v", got)
	}
	if got := slugs("--state=open"); !slices.Equal(got, []string{"one"}) {
		t.Errorf("--state=open: %v", got)
	}
	e2 := newTaskEnv(t)
	if r := e2.rig("task", "list", "--json"); strings.TrimSpace(r.out) != "[]" {
		t.Errorf("empty list: %q", r.out)
	}
}

func TestTaskShowText(t *testing.T) {
	e := newTaskEnv(t)
	e.newTask("tx", "--body", "The body.", "--field", "owner=ann")
	if r := e.rig("task", "move", "tx", "stopped", "--reason", "blocked"); r.code != 0 {
		t.Fatal(r)
	}
	task, _ := e.st.Task(ctx, "tx")
	if _, err := e.st.AddNote(ctx, store.Note{Task: task.ID, Kind: "decision", Text: "chose X"}); err != nil {
		t.Fatal(err)
	}
	if r := e.rig("task", "show", "tx"); r.code != 0 || strings.Contains(r.out, "chose X") || !strings.Contains(r.out, "# Title tx") ||
		!strings.Contains(r.out, "The body.") || !strings.Contains(r.out, "action: decide") || !strings.Contains(r.out, "owner: ann") {
		t.Fatalf("plain: %+v", r)
	}
	r := e.rig("task", "show", "tx", "--findings", "--notes", "--history")
	for _, want := range []string{"## Findings", "## Notes", "chose X", "## History", "open -> stopped", "reason: blocked"} {
		if !strings.Contains(r.out, want) {
			t.Errorf("show with flags lacks %q:\n%s", want, r.out)
		}
	}
}

// editEnv runs task edit with this test binary as the editor.
func (e *taskEnv) editWith(t *testing.T, env map[string]string, ref string) result {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RIG_TASK_EDIT_HELPER", "1")
	for k, v := range env {
		t.Setenv(k, v)
	}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TEMP", tmp)
	t.Setenv("TMP", tmp)
	e.env["EDITOR"] = exe
	e.env["RIG_EDITOR_NO_TTY"] = "1"
	r := e.rig("task", "edit", ref, "--json")
	e.env["EDITOR"] = ""
	return r
}

// AC5: edit writes one field event per changed value and nothing else.
func TestTaskEdit(t *testing.T) {
	e := newTaskEnv(t)
	task := e.newTask("ed", "--body", "old body", "--field", "owner=ann", "--field", "tags=a,b")
	id := task["id"].(string)
	before := len(e.kinds(id))

	// Nothing changed: no events.
	r := e.editWith(t, map[string]string{}, "ed")
	if r.code != 0 || len(e.kinds(id)) != before {
		t.Fatalf("unchanged: %+v, kinds %v", r, e.kinds(id))
	}

	// One value changed: one event.
	r = e.editWith(t, map[string]string{"RIG_EDIT_FROM": "old body", "RIG_EDIT_TO": "new body"}, "ed")
	if r.code != 0 || len(e.kinds(id)) != before+1 {
		t.Fatalf("one change: %+v, kinds %v", r, e.kinds(id))
	}
	if got := decodeObj(t, r.out); got["body"] != "new body" || got["state"] != "open" {
		t.Fatalf("after: %v", got)
	}

	// Title, a list and a new field: three events; the state is untouched.
	file := "# Renamed\n\n## body\n\nnew body\n\n## kind\n\nb\n\n## owner\n\nann\n\n## tags\n\na, b, c\n"
	mid := len(e.kinds(id))
	r = e.editWith(t, map[string]string{"RIG_EDIT_WRITE": file}, "ed")
	if r.code != 0 || len(e.kinds(id)) != mid+3 || countKind(e.kinds(id), "field") != 4 {
		t.Fatalf("three changes: %+v, kinds %v", r, e.kinds(id))
	}
	got := decodeObj(t, r.out)
	f := got["fields"].(map[string]any)
	if got["title"] != "Renamed" || f["kind"] != "b" || !slices.Equal(anyStrings(f["tags"]), []string{"a", "b", "c"}) {
		t.Fatalf("after: %v", got)
	}
}

// AC5: a file that does not parse writes nothing and keeps the copy.
func TestTaskEditUnparsable(t *testing.T) {
	e := newTaskEnv(t)
	task := e.newTask("bad", "--body", "b")
	id := task["id"].(string)
	before := len(e.kinds(id))
	for name, file := range map[string]string{
		"no title":    "just words\n",
		"bad int":     "# T\n\n## size\n\nnot a number\n",
		"bad enum":    "# T\n\n## kind\n\nzzz\n",
		"twice":       "# T\n\n## body\n\nx\n\n## body\n\ny\n",
		"empty title": "# \n\n## body\n\nx\n",
	} {
		r := e.editWith(t, map[string]string{"RIG_EDIT_WRITE": file}, "bad")
		if r.code != 2 || len(e.kinds(id)) != before {
			t.Fatalf("%s: %+v, kinds %v", name, r, e.kinds(id))
		}
		i := strings.LastIndex(r.errOut, "your copy is at ")
		if i < 0 {
			t.Fatalf("%s: no copy path in %q", name, r.errOut)
		}
		kept, err := os.ReadFile(strings.TrimSpace(r.errOut[i+len("your copy is at "):]))
		if err != nil || string(kept) != file {
			t.Fatalf("%s: kept copy %q, %v", name, kept, err)
		}
	}
}

// AC5: without a terminal, or without an editor, edit is exit 2.
func TestTaskEditNeedsTerminalAndEditor(t *testing.T) {
	e := newTaskEnv(t)
	e.newTask("nt")
	if r := e.rig("task", "edit", "nt"); r.code != 2 {
		t.Fatalf("no editor, no terminal: %+v", r)
	}
	e.env["EDITOR"] = "true"
	if r := e.rig("task", "edit", "nt"); r.code != 2 || !strings.Contains(r.errOut, "terminal") {
		t.Fatalf("editor but no terminal: %+v", r)
	}
	delete(e.env, "EDITOR")
	e.env["RIG_EDITOR_NO_TTY"] = "1"
	if r := e.rig("task", "edit", "nt"); r.code != 2 || !strings.Contains(r.errOut, "EDITOR") {
		t.Fatalf("terminal but no editor: %+v", r)
	}
	e.env["VISUAL"] = "rig-no-such-editor"
	if r := e.rig("task", "edit", "nt"); r.code != 2 {
		t.Fatalf("missing editor program: %+v", r)
	}
}

func TestEditFileRoundTrip(t *testing.T) {
	e := newTaskEnv(t)
	task := e.newTask("rt", "--body", "line1\n\n## not a field\n\nline2", "--field", "tags=a,b", "--field", "size=4", "--field", "notes=n1\nn2")
	w, err := (&app{cwd: e.repo, getenv: func(k string) string { return e.env[k] },
		store: func(context.Context, string) (store.Store, error) { return e.st, nil }}).openWork(ctx, "", true)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := e.st.Task(ctx, task["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	changed, err := parseEdit(w.wf, cur, editFile(w.wf, cur))
	if err != nil || len(changed) != 0 {
		t.Fatalf("a file written and read back differs: %v, %v", changed, err)
	}
}

// One end-to-end run on the git-ref store with the built-in basic workflow.
func TestTaskEndToEndGitRef(t *testing.T) {
	repo, _ := newRepo(t)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	run := func(args ...string) result {
		var out, errOut bytes.Buffer
		a := &app{stdout: &out, stderr: &errOut, cwd: repo, now: tick(),
			getenv: func(k string) string {
				switch k {
				case "RIG_WORKFLOW":
					return "basic"
				case "RIG_ACTOR":
					return "tester"
				}
				return ""
			}}
		a.store = a.openStore
		code := a.run(ctx, args)
		return result{code, out.String(), errOut.String()}
	}
	r := run("task", "new", "--slug", "e2e", "--title", "End to end", "--json")
	if r.code != 0 {
		t.Fatalf("new: %+v", r)
	}
	if got := decodeObj(t, r.out); got["state"] != "open" || got["workflow"].(map[string]any)["name"] != "basic" {
		t.Fatalf("new: %v", got)
	}
	if r := run("task", "move", "e2e", "active"); r.code != 0 {
		t.Fatalf("move: %+v", r)
	}
	if r := run("task", "is", "e2e", "active"); r.code != 0 {
		t.Fatalf("is: %+v", r)
	}
	if r := run("task", "move", "e2e", "open"); r.code != 1 {
		t.Fatalf("no such transition: %+v", r)
	}
	if r := run("task", "set", "e2e", "--field", "title=Done soon"); r.code != 0 {
		t.Fatalf("set: %+v", r)
	}
	if r := run("task", "move", "e2e", "done"); r.code != 0 {
		t.Fatalf("done: %+v", r)
	}
	if got := decodeList(t, run("task", "list", "--json").out); len(got) != 0 {
		t.Fatalf("final tasks are left out: %v", got)
	}
	l := decodeList(t, run("task", "log", "e2e", "--json").out)
	var kinds []string
	for _, ev := range l {
		kinds = append(kinds, ev["kind"].(string))
	}
	if !slices.Equal(kinds, []string{"created", "transition", "field", "transition"}) {
		t.Fatalf("log kinds: %v", kinds)
	}
}

// A value line that looks like a heading or an escape survives an edit, and an
// edit that changes another field leaves the body byte-identical.
func TestTaskEditEscapesHeadingLines(t *testing.T) {
	e := newTaskEnv(t)
	body := "intro\n## owner\nsee\n\\x\n## body\n# h\n\\\\y"
	task := e.newTask("esc", "--body", body, "--field", "notes=## notes\nmore")
	id := task["id"].(string)
	before := len(e.kinds(id))
	if r := e.editWith(t, map[string]string{}, "esc"); r.code != 0 || len(e.kinds(id)) != before {
		t.Fatalf("no-op: %+v, kinds %v", r, e.kinds(id))
	}
	r := e.editWith(t, map[string]string{"RIG_EDIT_FROM": "## kind\n\n", "RIG_EDIT_TO": "## kind\n\nb"}, "esc")
	if r.code != 0 || len(e.kinds(id)) != before+1 {
		t.Fatalf("one change: %+v, kinds %v", r, e.kinds(id))
	}
	got := decodeObj(t, r.out)
	if got["body"] != body || got["fields"].(map[string]any)["kind"] != "b" || got["fields"].(map[string]any)["notes"] != "## notes\nmore" {
		t.Fatalf("after: %v", got)
	}
}

// Values that do not survive the round trip are not rewritten by a no-op edit.
func TestTaskEditNoOpKeepsAwkwardValues(t *testing.T) {
	e := newTaskEnv(t)
	r := e.rig("task", "new", "--slug", "awk", "--title", "Fix it ", "--body", "a\r\nb\r\n", "--field", "owner= ann", "--json")
	if r.code != 0 {
		t.Fatal(r)
	}
	id := decodeObj(t, r.out)["id"].(string)
	before := len(e.kinds(id))
	if r := e.editWith(t, map[string]string{}, "awk"); r.code != 0 || len(e.kinds(id)) != before {
		t.Fatalf("no-op: %+v, kinds %v", r, e.kinds(id))
	}
	task, _ := e.st.Task(ctx, id)
	if task.Title != "Fix it " || task.Body != "a\r\nb\r\n" || task.Fields["owner"] != " ann" {
		t.Fatalf("rewritten: %+v", task)
	}
	nl, err := e.st.CreateTask(ctx, store.NewTask{Slug: "nl", Title: "two\nlines", Initial: "open"})
	if err != nil {
		t.Fatal(err)
	}
	if r := e.editWith(t, map[string]string{}, "nl"); r.code != 0 || len(e.kinds(nl.ID)) != 1 {
		t.Fatalf("newline title: %+v, kinds %v", r, e.kinds(nl.ID))
	}
}

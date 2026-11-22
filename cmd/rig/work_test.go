package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/store/gitref"
	"github.com/AindriuB/rig/internal/store/memstore"
	"github.com/AindriuB/rig/internal/workflow"
)

// The test binary doubles as a command gate: with RIG_CMD_HELPER set it
// appends its stdin to that file and exits RIG_CMD_EXIT, without running
// tests. No shell is involved, so this works on every OS.
func TestMain(m *testing.M) {
	if out := os.Getenv("RIG_CMD_HELPER"); out != "" {
		stdin, _ := io.ReadAll(os.Stdin)
		f, err := os.OpenFile(out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(99)
		}
		_, _ = f.Write(append(stdin, '\n'))
		_ = f.Close()
		code, _ := strconv.Atoi(os.Getenv("RIG_CMD_EXIT"))
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// testWorkflow is a workflow with a sha-bound state, a field gate, a command
// gate (run by this test binary), a findings gate and $previous, once listed
// before an explicit route to the same state.
func testWorkflow(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	argv, _ := json.Marshal([]string{exe, "{short}"})
	wf := `{
  "name": "t", "version": 1, "initial": "open", "branch": "task/{short}-{slug}",
  "states": {
    "open":    { "action": "start" },
    "bound":   { "action": "finish", "shaBound": true },
    "back":    { "action": "rebind" },
    "checked": { "action": "finish" },
    "done":    { "final": true },
    "stopped": { "action": "decide", "who": "human" },
    "reviewed": { "action": "finish" },
    "paused":  { "action": "decide", "who": "human" },
    "gated":   { "action": "finish" }
  },
  "onNewCommit": "back",
  "transitions": [
    { "from": "open", "to": "bound", "takes": "sha", "gates": [{ "shaIsHead": true }] },
    { "from": "bound", "to": "back" },
    { "from": "back", "to": "bound", "takes": "sha" },
    { "from": ["open", "bound", "checked", "reviewed"], "to": "done", "gates": [{ "fieldSet": "owner" }] },
    { "from": "open", "to": "reviewed", "gates": [{ "noOpenFindings": { "severity": "P1" } }] },
    { "from": "open", "to": "paused" },
    { "from": "paused", "to": "$previous" },
    { "from": "paused", "to": "open", "gates": [{ "fieldSet": "owner" }] },
    { "from": "open", "to": "checked", "gates": [{ "command": ` + string(argv) + ` }] },
    { "from": "open", "to": "gated", "gates": [{ "dependsIn": ["stopped"] }] },
    { "from": "*", "to": "stopped", "takes": "reason" },
    { "from": "stopped", "to": "$previous" }
  ],
  "fields": { "owner": { "type": "string" }, "depends": { "type": "tasks" } },
  "findings": { "severities": ["P1", "P2"] }
}`
	path := filepath.Join(t.TempDir(), "t.json")
	if err := os.WriteFile(path, []byte(wf), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// tick is a clock that moves one second on every call, so each write gets
// its own Updated time.
func tick() func() time.Time {
	var n atomic.Int64
	base := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	return func() time.Time { return base.Add(time.Duration(n.Add(1)) * time.Second) }
}

// workApp is an app on a memstore under the test workflow, in a fresh
// repository (for branch heads), with its stderr captured.
func workApp(t *testing.T, st store.Store) (*app, *bytes.Buffer) {
	t.Helper()
	wf := testWorkflow(t)
	repo, _ := newRepo(t)
	var errOut bytes.Buffer
	a := &app{
		stdout: io.Discard, stderr: &errOut, cwd: repo,
		getenv: func(k string) string {
			if k == "RIG_WORKFLOW" {
				return wf
			}
			return ""
		},
		store: func(context.Context, string) (store.Store, error) { return st, nil },
	}
	return a, &errOut
}

func newTask(t *testing.T, st store.Store) store.Task {
	t.Helper()
	task, err := st.CreateTask(ctx, store.NewTask{Slug: "s", Title: "S", Initial: "open", Workflow: store.WorkflowRef{Name: "t", Version: 1}})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// transitions returns the task's transition events.
func transitions(t *testing.T, st store.Store, ref string) []store.TransitionData {
	t.Helper()
	evs, err := st.Events(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.TransitionData
	for _, e := range evs {
		if e.Kind == store.KindTransition {
			var d store.TransitionData
			if err := json.Unmarshal(e.Data, &d); err != nil {
				t.Fatal(err)
			}
			out = append(out, d)
		}
	}
	return out
}

func TestLoadWorkflowOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	repo, _ := newRepo(t)
	file := testWorkflow(t)
	env := map[string]string{}
	a := &app{getenv: func(k string) string { return env[k] }}

	load := func() (string, workflow.Source) {
		t.Helper()
		wf, src, err := a.loadWorkflow(ctx, repo)
		if err != nil {
			t.Fatal(err)
		}
		return wf.Name, src
	}
	if name, src := load(); name != "basic" || src.Preset != "basic" {
		t.Fatalf("no config: %s from %v, want basic", name, src)
	}

	cfg, _ := json.Marshal(map[string]string{"workflow": file})
	if err := os.MkdirAll(filepath.Join(home, ".rig"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".rig", "config.json"), cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	if name, src := load(); name != "t" || src.Path != file {
		t.Fatalf("config: %s from %v, want t from %s", name, src, file)
	}

	env["RIG_WORKFLOW"] = "basic"
	if name, src := load(); name != "basic" || src.Preset != "basic" {
		t.Fatalf("RIG_WORKFLOW: %s from %v, want basic", name, src)
	}
}

func TestOpenWorkInvalidWorkflow(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"name":"bad","version":1,"initial":"nowhere","states":{"a":{}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	st := memstore.New(store.Options{})
	a, _ := workApp(t, st)
	a.getenv = func(k string) string {
		if k == "RIG_WORKFLOW" {
			return bad
		}
		return ""
	}
	var invalid *workflow.InvalidError
	if w, err := a.openWork(ctx, "", true); w != nil || !errors.As(err, &invalid) || !strings.Contains(err.Error(), "initial") {
		t.Fatalf("needValid: work %v, err %v", w, err)
	}
	w, err := a.openWork(ctx, "", false)
	if w == nil || w.wf == nil || !errors.As(err, &invalid) {
		t.Fatalf("report only: work %v, err %v", w, err)
	}
	if code := a.storeFail("workflow", err); code != 2 {
		t.Fatalf("invalid workflow exits %d, want 2", code)
	}
}

func TestMove(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	a, errOut := workApp(t, st)
	w, err := a.openWork(ctx, "", true)
	if err != nil {
		t.Fatal(err)
	}
	task := newTask(t, st)

	// A legal move writes one transition event.
	res, err := w.move(ctx, task.Short, "stopped", workflow.Args{Reason: "wait"})
	if err != nil || res.view.State != "stopped" || res.view.Reason != "wait" {
		t.Fatalf("move to stopped: %+v, %v", res.view, err)
	}
	if tr := transitions(t, st, task.ID); len(tr) != 1 || tr[0] != (store.TransitionData{From: "open", To: "stopped", Reason: "wait"}) {
		t.Fatalf("transitions %+v", tr)
	}

	// $previous: stopped goes back to the state before it.
	if _, err := w.move(ctx, task.Short, "done", workflow.Args{}); a.storeFail("task", err) != 1 {
		t.Fatalf("stopped → done: %v", err)
	}
	res, err = w.move(ctx, task.Short, "open", workflow.Args{})
	if err != nil || res.view.State != "open" {
		t.Fatalf("$previous: %+v, %v", res.view, err)
	}
	if tr := transitions(t, st, task.ID); len(tr) != 2 || tr[1].To != "open" {
		t.Fatalf("transitions %+v", tr)
	}

	// A failing gate is a refusal, exit 1, and writes nothing.
	errOut.Reset()
	before, _ := st.Events(ctx, task.ID)
	_, err = w.move(ctx, task.Short, "done", workflow.Args{})
	var r *refusal
	if !errors.As(err, &r) || len(r.gates) != 1 || r.gates[0].OK {
		t.Fatalf("gate: %v", err)
	}
	if code := a.storeFail("task", err); code != 1 || !strings.Contains(errOut.String(), "fieldSet: field owner is not set") {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
	if after, _ := st.Events(ctx, task.ID); len(after) != len(before) {
		t.Fatalf("a refused move wrote %d events", len(after)-len(before))
	}

	// No such transition is a refusal; no such task exits 2.
	errOut.Reset()
	if _, err := w.move(ctx, task.Short, "nowhere", workflow.Args{}); a.storeFail("task", err) != 1 ||
		!strings.Contains(errOut.String(), "no transition from open to nowhere") {
		t.Fatalf("no transition: %v, %q", err, errOut.String())
	}
	_, err = w.move(ctx, "t-zzzz", "done", workflow.Args{})
	if !errors.Is(err, store.ErrNotFound) || a.storeFail("task", err) != 2 {
		t.Fatalf("missing task: %v", err)
	}
}

// racyStore writes a field event to the task just before each of its first
// races Updates, as another writer would between check and write.
type racyStore struct {
	store.Store
	races, calls int
}

func (r *racyStore) Update(ctx context.Context, ref string, next func(store.Task, []store.Finding) ([]store.Event, error)) (store.Task, error) {
	r.calls++
	if r.calls <= r.races {
		ev, err := store.NewEvent(store.KindField, store.FieldData{Name: "note", Value: strconv.Itoa(r.calls)})
		if err != nil {
			return store.Task{}, err
		}
		if _, err := r.Store.Update(ctx, ref, func(store.Task, []store.Finding) ([]store.Event, error) {
			return []store.Event{ev}, nil
		}); err != nil {
			return store.Task{}, err
		}
	}
	return r.Store.Update(ctx, ref, next)
}

func TestMoveRetriesWhenTheTaskChanged(t *testing.T) {
	mem := memstore.New(store.Options{Now: tick()})
	st := &racyStore{Store: mem, races: 2}
	a, _ := workApp(t, st)
	w, err := a.openWork(ctx, "", true)
	if err != nil {
		t.Fatal(err)
	}
	task := newTask(t, mem)
	if _, err := w.move(ctx, task.Short, "stopped", workflow.Args{Reason: "r"}); err != nil {
		t.Fatal(err)
	}
	if st.calls != 3 || len(transitions(t, mem, task.ID)) != 1 {
		t.Fatalf("%d updates, %d transitions; want 3 and 1", st.calls, len(transitions(t, mem, task.ID)))
	}

	// A task that keeps changing gives up with ErrConflict, exit 2.
	task = newTask(t, mem)
	st.races, st.calls = 100, 0
	_, err = w.move(ctx, task.ID, "stopped", workflow.Args{Reason: "r"})
	if !errors.Is(err, gitref.ErrConflict) || a.storeFail("task", err) != 2 {
		t.Fatalf("err %v", err)
	}
	if st.calls != moveRetries+1 || len(transitions(t, mem, task.ID)) != 0 {
		t.Fatalf("%d updates, %d transitions", st.calls, len(transitions(t, mem, task.ID)))
	}
}

func TestMoveCommandGateSeesTheView(t *testing.T) {
	out := filepath.Join(t.TempDir(), "stdin.jsonl")
	t.Setenv("RIG_CMD_HELPER", out)
	st := memstore.New(store.Options{Now: tick()})
	a, _ := workApp(t, st)
	w, err := a.openWork(ctx, "", true)
	if err != nil {
		t.Fatal(err)
	}
	task := newTask(t, st)

	t.Setenv("RIG_CMD_EXIT", "3")
	if _, err := w.move(ctx, task.Short, "checked", workflow.Args{}); a.storeFail("task", err) != 1 {
		t.Fatalf("failing command gate: %v", err)
	}
	t.Setenv("RIG_CMD_EXIT", "0")
	res, err := w.move(ctx, task.Short, "checked", workflow.Args{})
	if err != nil || res.view.State != "checked" {
		t.Fatalf("passing command gate: %+v, %v", res.view, err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var v workflow.TaskView
	if len(lines) != 2 || json.Unmarshal([]byte(lines[1]), &v) != nil {
		t.Fatalf("gate stdin %q", data)
	}
	if v.ID != task.ID || v.State != "open" || v.Branch != "task/"+task.Short+"-s" {
		t.Fatalf("gate saw %+v", v)
	}
}

// sneakyStore runs before once, just before the first Update, as another
// writer would between the gate check and the write.
type sneakyStore struct {
	store.Store
	before func(ref string)
	calls  int
}

func (s *sneakyStore) Update(ctx context.Context, ref string, next func(store.Task, []store.Finding) ([]store.Event, error)) (store.Task, error) {
	s.calls++
	if s.before != nil {
		s.before(ref)
		s.before = nil
	}
	return s.Store.Update(ctx, ref, next)
}

// fixed is a clock that never moves, so a change keeps the task's Updated.
func fixed() time.Time { return time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC) }

func TestMoveRechecksAFindingAddedAtTheSameTime(t *testing.T) {
	mem := memstore.New(store.Options{Now: fixed})
	st := &sneakyStore{Store: mem}
	a, _ := workApp(t, st)
	w, err := a.openWork(ctx, "", true)
	if err != nil {
		t.Fatal(err)
	}
	task := newTask(t, mem)
	st.before = func(ref string) {
		ev, err := store.NewEvent(store.KindFinding, store.FindingData{Severity: "P1", Summary: "late"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := mem.Update(ctx, ref, func(store.Task, []store.Finding) ([]store.Event, error) {
			return []store.Event{ev}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, err = w.move(ctx, task.Short, "reviewed", workflow.Args{})
	if code := a.storeFail("task", err); code != 1 || st.calls != 1 {
		t.Fatalf("exit %d after %d updates, err %v; want 1 after 1", code, st.calls, err)
	}
	if got, _ := mem.Task(ctx, task.ID); got.State != "open" || len(transitions(t, mem, task.ID)) != 0 {
		t.Fatalf("task moved to %s", got.State)
	}
}

func TestMoveRechecksAHeadThatMoved(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	mem := memstore.New(store.Options{Now: fixed})
	st := &sneakyStore{Store: mem}
	a, _ := workApp(t, st)
	w, err := a.openWork(ctx, "", true)
	if err != nil {
		t.Fatal(err)
	}
	task := newTask(t, mem)
	branch := "task/" + task.Short + "-s"
	gitc(t, a.cwd, "branch", branch)
	sha := gitc(t, a.cwd, "rev-parse", branch)
	st.before = func(string) {
		gitc(t, a.cwd, "checkout", "-q", branch)
		gitc(t, a.cwd, "commit", "-q", "--allow-empty", "-m", "more")
		gitc(t, a.cwd, "checkout", "-q", "main")
	}
	_, err = w.move(ctx, task.Short, "bound", workflow.Args{Sha: sha})
	if code := a.storeFail("task", err); code != 1 || st.calls != 1 {
		t.Fatalf("exit %d after %d updates, err %v; want 1 after 1", code, st.calls, err)
	}
	if got, _ := mem.Task(ctx, task.ID); got.State != "open" || len(transitions(t, mem, task.ID)) != 0 {
		t.Fatalf("task moved to %s", got.State)
	}
}

func TestMoveLiteralPrevious(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	a, errOut := workApp(t, st)
	w, err := a.openWork(ctx, "", true)
	if err != nil {
		t.Fatal(err)
	}
	task := newTask(t, st)

	// A task with no previous state refuses "$previous", exit 1.
	if _, err := w.move(ctx, task.Short, workflow.Previous, workflow.Args{}); a.storeFail("task", err) != 1 ||
		!strings.Contains(errOut.String(), "no previous state") {
		t.Fatalf("no previous: %v, %q", err, errOut.String())
	}

	if _, err := w.move(ctx, task.Short, "stopped", workflow.Args{Reason: "r"}); err != nil {
		t.Fatal(err)
	}
	res, err := w.move(ctx, task.Short, workflow.Previous, workflow.Args{})
	if err != nil || res.view.State != "open" {
		t.Fatalf("$previous: %+v, %v", res.view, err)
	}
	if tr := transitions(t, st, task.ID); len(tr) != 2 || tr[1].To != "open" {
		t.Fatalf("transitions %+v", tr)
	}
}

func TestMovePrefersTheExplicitRoute(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	a, errOut := workApp(t, st)
	w, err := a.openWork(ctx, "", true)
	if err != nil {
		t.Fatal(err)
	}
	task := newTask(t, st)
	if _, err := w.move(ctx, task.Short, "paused", workflow.Args{}); err != nil {
		t.Fatal(err)
	}
	// paused → $previous (open) is listed first, but paused → open is the
	// explicit route, and its fieldSet gate refuses.
	_, err = w.move(ctx, task.Short, "open", workflow.Args{})
	if code := a.storeFail("task", err); code != 1 || !strings.Contains(errOut.String(), "field owner is not set") {
		t.Fatalf("exit %d, stderr %q, err %v", code, errOut.String(), err)
	}
	if got, _ := st.Task(ctx, task.ID); got.State != "paused" {
		t.Fatalf("task moved to %s", got.State)
	}
}

// TestWorkGitref runs head, view and move on the git-ref store of a real
// repository.
func TestWorkGitref(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	repo, _ := newRepo(t)
	wf := testWorkflow(t)
	a := &app{
		stdout: io.Discard, stderr: io.Discard, cwd: repo, now: tick(),
		getenv: func(k string) string {
			return map[string]string{"RIG_WORKFLOW": wf, "RIG_ACTOR": "tester"}[k]
		},
	}
	w, err := a.openWork(ctx, repo, true)
	if err != nil {
		t.Fatal(err)
	}
	task := newTask(t, w.st)

	// No branch yet: head is "", and shaIsHead refuses.
	if head, err := w.head(ctx, task); err != nil || head != "" {
		t.Fatalf("missing branch: head %q, %v", head, err)
	}
	if _, err := w.move(ctx, task.Short, "bound", workflow.Args{Sha: "abc"}); a.storeFail("task", err) != 1 {
		t.Fatalf("bound without a branch: %v", err)
	}

	branch := "task/" + task.Short + "-s"
	gitc(t, repo, "branch", branch)
	sha := gitc(t, repo, "rev-parse", branch)
	res, err := w.move(ctx, task.Short, "bound", workflow.Args{Sha: sha})
	if err != nil || res.view.State != "bound" || res.view.HeadSha != sha || res.view.StateSha != sha {
		t.Fatalf("move to bound: %+v, %v", res.view, err)
	}

	// A commit on the branch moves the sha-bound task to onNewCommit.
	gitc(t, repo, "checkout", "-q", branch)
	gitc(t, repo, "commit", "-q", "--allow-empty", "-m", "more")
	gitc(t, repo, "checkout", "-q", "main")
	task, err = w.st.Task(ctx, task.Short)
	if err != nil {
		t.Fatal(err)
	}
	v, err := w.view(ctx, task)
	if err != nil || v.State != "back" || v.RecordedState != "bound" || v.HeadSha == sha {
		t.Fatalf("view after a new commit: %+v, %v", v, err)
	}

	// The move starts from the effective state.
	head := v.HeadSha
	res, err = w.move(ctx, task.Short, "bound", workflow.Args{Sha: head})
	if err != nil || res.view.State != "bound" || res.view.StateSha != head {
		t.Fatalf("back → bound: %+v, %v", res.view, err)
	}
	if tr := transitions(t, w.st, task.ID); len(tr) != 2 || tr[1].From != "back" {
		t.Fatalf("transitions %+v", tr)
	}
}

// depStore runs hook once, right after the first read of the task dep, as a
// dependency changing while a gate runs.
type depStore struct {
	store.Store
	dep  string
	hook func()
}

func (s *depStore) Task(ctx context.Context, ref string) (store.Task, error) {
	t, err := s.Store.Task(ctx, ref)
	if err == nil && t.ID == s.dep && s.hook != nil {
		h := s.hook
		s.hook = nil
		h()
	}
	return t, err
}

// dependentTask is a task depending on a second one, which is stopped.
func dependentTask(t *testing.T, mem store.Store, w *work) (task, dep store.Task) {
	t.Helper()
	dep = newTask(t, mem)
	task = newTask(t, mem)
	ev, err := store.NewEvent(store.KindField, store.FieldData{Name: "depends", Value: []string{dep.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Update(ctx, task.ID, func(store.Task, []store.Finding) ([]store.Event, error) {
		return []store.Event{ev}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.move(ctx, dep.ID, "stopped", workflow.Args{Reason: "r"}); err != nil {
		t.Fatal(err)
	}
	return task, dep
}

func TestMoveRechecksADependencyThatLeftItsState(t *testing.T) {
	mem := memstore.New(store.Options{Now: tick()})
	st := &depStore{Store: mem}
	a, _ := workApp(t, st)
	w, err := a.openWork(ctx, "", true)
	if err != nil {
		t.Fatal(err)
	}
	task, dep := dependentTask(t, mem, w)
	st.dep = dep.ID
	st.hook = func() {
		if _, err := w.move(ctx, dep.ID, "$previous", workflow.Args{}); err != nil {
			t.Error(err)
		}
	}
	_, err = w.move(ctx, task.Short, "gated", workflow.Args{})
	if code := a.storeFail("task", err); code != 1 {
		t.Fatalf("exit %d, err %v; want 1", code, err)
	}
	if got, _ := mem.Task(ctx, task.ID); got.State != "open" || len(transitions(t, mem, task.ID)) != 0 {
		t.Fatalf("task moved to %s", got.State)
	}
}

func TestMoveWritesWhenTheDependencyHolds(t *testing.T) {
	mem := memstore.New(store.Options{Now: tick()})
	a, _ := workApp(t, mem)
	w, err := a.openWork(ctx, "", true)
	if err != nil {
		t.Fatal(err)
	}
	task, _ := dependentTask(t, mem, w)
	res, err := w.move(ctx, task.Short, "gated", workflow.Args{})
	if err != nil || res.view.State != "gated" {
		t.Fatalf("%+v, %v", res.view, err)
	}
}

// Two gates reading one dependency must see one state: if the reads differ,
// the move starts over even when the last read matches the store.
func TestRecheckDepsCatchesReadsThatDisagree(t *testing.T) {
	mem := memstore.New(store.Options{Now: tick()})
	a, _ := workApp(t, mem)
	w, err := a.openWork(ctx, "", true)
	if err != nil {
		t.Fatal(err)
	}
	dep := newTask(t, mem)
	env, reads := w.recordingEnv()
	if _, err := env.Task(ctx, dep.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.move(ctx, dep.ID, "stopped", workflow.Args{Reason: "r"}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Task(ctx, dep.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.recheckDeps(ctx, reads); !errors.Is(err, errMoved) {
		t.Fatalf("recheckDeps = %v, want errMoved", err)
	}
}

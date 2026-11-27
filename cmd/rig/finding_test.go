package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/store/memstore"
	"github.com/AindriuB/rig/internal/workflow"
)

// runIn runs a command on a, returning its exit code, stdout and stderr.
func runIn(a *app, args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	a.stdout, a.stderr = &out, &errOut
	code := a.run(ctx, args)
	return code, out.String(), errOut.String()
}

func eventCount(t *testing.T, st store.Store, ref, kind string) int {
	t.Helper()
	evs, err := st.Events(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func mustFindings(t *testing.T, st store.Store, id string) []store.Finding {
	t.Helper()
	fs, err := st.Findings(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func reviewLoopPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "internal", "workflow", "testdata", "review-loop.json"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type addJSON struct {
	Finding   store.Finding `json:"finding"`
	Duplicate bool          `json:"duplicate"`
}

func TestFindingAddAndDuplicate(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	a, _ := workApp(t, st)
	task := newTask(t, st)

	code, out, errOut := runIn(a, "finding", "add", task.Short, "--severity", "P1", "--summary", "Nil  deref", "--path", "a.go:10", "--json")
	if code != 0 {
		t.Fatalf("add: %d %s", code, errOut)
	}
	var top map[string]json.RawMessage
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &top); err != nil || len(top) != 2 || json.Unmarshal(top["finding"], &raw) != nil {
		t.Fatalf("add json: %v %s", err, out)
	}
	if string(top["duplicate"]) != "false" {
		t.Fatalf("duplicate = %s", top["duplicate"])
	}
	var got addJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Duplicate {
		t.Fatalf("add json: %v %s", err, out)
	}
	for _, k := range []string{"id", "short", "task", "severity", "status", "key", "summary", "path", "counters", "created", "updated"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("finding JSON lacks %q: %s", k, out)
		}
	}
	if got.Finding.Status != "open" || got.Finding.Key != "P1|a.go|nil deref" {
		t.Errorf("finding: %+v", got.Finding)
	}

	// Same key (line, case and spacing differ): no new event.
	code, out, _ = runIn(a, "finding", "add", task.Short, "--severity", "P1", "--summary", "nil deref", "--path", "a.go:99", "--json")
	if code != 0 {
		t.Fatalf("duplicate exits %d", code)
	}
	var dup addJSON
	if err := json.Unmarshal([]byte(out), &dup); err != nil || !dup.Duplicate || dup.Finding.ID != got.Finding.ID {
		t.Fatalf("duplicate json: %v %s", err, out)
	}
	if n := eventCount(t, st, task.ID, store.KindFinding); n != 1 {
		t.Fatalf("%d finding events, want 1", n)
	}

	// Once fixed, the same finding is new again.
	if code, _, e := runIn(a, "finding", "set", task.Short, got.Finding.Short, "--status", "fixed"); code != 0 {
		t.Fatalf("set: %d %s", code, e)
	}
	_, out, _ = runIn(a, "finding", "add", task.Short, "--severity", "P1", "--summary", "nil deref", "--path", "a.go", "--json")
	if strings.Contains(out, `"duplicate": true`) {
		t.Fatalf("a fixed finding still dedupes: %s", out)
	}
}

func TestFindingAddSnapshotsCounters(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	a, _ := workApp(t, st)
	task := newTask(t, st)
	if _, err := st.Update(ctx, task.ID, func(store.Task, []store.Finding) ([]store.Event, error) {
		ev, err := store.NewEvent(store.KindCount, store.CountData{Counter: "rounds", By: 2})
		return []store.Event{ev}, err
	}); err != nil {
		t.Fatal(err)
	}
	if code, _, e := runIn(a, "finding", "add", task.Short, "--severity", "P2", "--summary", "x"); code != 0 {
		t.Fatalf("add: %d %s", code, e)
	}
	fs := mustFindings(t, st, task.ID)
	if len(fs) != 1 || fs[0].Counters["rounds"] != 2 {
		t.Fatalf("counters %v", fs)
	}
}

func TestFindingUsageAndWorkflowErrors(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	a, _ := workApp(t, st)
	task := newTask(t, st)
	for name, args := range map[string][]string{
		"unknown severity": {"finding", "add", task.Short, "--severity", "P9", "--summary", "x"},
		"unknown field":    {"finding", "add", task.Short, "--severity", "P1", "--summary", "x", "--field", "route=a"},
		"unknown flag":     {"finding", "add", task.Short, "--severity", "P1", "--summary", "x", "--bogus"},
		"no summary":       {"finding", "add", task.Short, "--severity", "P1"},
		"no task":          {"finding", "add", "--severity", "P1", "--summary", "x"},
		"missing task":     {"finding", "add", "nope", "--severity", "P1", "--summary", "x"},
		"unknown sub":      {"finding", "frob"},
		"no sub":           {"finding"},
		"list bad status":  {"finding", "list", task.Short, "--status", "weird"},
		"list unknown flg": {"finding", "list", task.Short, "--bogus"},
		"set bad status":   {"finding", "set", task.Short, "f-x", "--status", "weird"},
		"set no finding":   {"finding", "set", task.Short, "f-nope", "--status", "fixed"},
	} {
		if code, _, _ := runIn(a, args...); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
	}
	if n := eventCount(t, st, task.ID, store.KindFinding); n != 0 {
		t.Fatalf("%d finding events written by failures", n)
	}
}

func TestFindingFields(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	a, _ := workApp(t, st)
	loop := reviewLoopPath(t)
	a.getenv = func(k string) string {
		if k == "RIG_WORKFLOW" {
			return loop
		}
		return ""
	}
	wf, _, err := a.loadWorkflow(ctx, a.cwd)
	if err != nil {
		t.Fatal(err)
	}
	task, err := st.CreateTask(ctx, store.NewTask{Slug: "s", Title: "S", Initial: wf.Initial, Workflow: store.WorkflowRef{Name: wf.Name, Version: wf.Version}})
	if err != nil {
		t.Fatal(err)
	}
	if code, _, e := runIn(a, "finding", "add", task.Short, "--severity", "P1", "--summary", "x", "--field", "route=implementer"); code != 0 {
		t.Fatalf("field add: %d %s", code, e)
	}
	if code, _, _ := runIn(a, "finding", "add", task.Short, "--severity", "P1", "--summary", "y", "--field", "route=nobody"); code != 2 {
		t.Fatalf("bad enum value: exit %d", code)
	}
	fs := mustFindings(t, st, task.ID)
	if len(fs) != 1 || fs[0].Fields["route"] != "implementer" {
		t.Fatalf("fields: %v", fs)
	}
}

func TestFindingListOrderAndBlocking(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	a, _ := workApp(t, st)
	task := newTask(t, st)
	// The test workflow's only noOpenFindings gate names P1, so P2 is not
	// blocking. Added oldest first: P2 a, P1 b, P2 c, P1 d.
	for _, f := range [][2]string{{"P2", "a"}, {"P1", "b"}, {"P2", "c"}, {"P1", "d"}} {
		if code, _, e := runIn(a, "finding", "add", task.Short, "--severity", f[0], "--summary", f[1]); code != 0 {
			t.Fatalf("add: %d %s", code, e)
		}
	}
	list := func(args ...string) string {
		t.Helper()
		code, out, e := runIn(a, append([]string{"finding", "list", task.Short, "--json"}, args...)...)
		if code != 0 {
			t.Fatalf("list: %d %s", code, e)
		}
		var fs []store.Finding
		if err := json.Unmarshal([]byte(out), &fs); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		var got string
		for _, f := range fs {
			got += f.Summary
		}
		return got
	}
	if got := list(); got != "bdac" {
		t.Fatalf("order %q, want bdac", got)
	}
	if got := list("--blocking"); got != "bd" {
		t.Fatalf("blocking %q, want bd", got)
	}

	// --blocking is what a noOpenFindings gate counts: the move to reviewed
	// is refused while it lists anything, and allowed once it is empty.
	w, err := a.openWork(ctx, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.move(ctx, task.Short, "reviewed", workflow.Args{}); a.storeFail("task", err) != 1 {
		t.Fatalf("move with blocking findings: %v, want a refusal", err)
	}
	for _, f := range mustFindings(t, st, task.ID) {
		if f.Severity == "P1" {
			if code, _, e := runIn(a, "finding", "set", task.Short, f.Short, "--status", "wontfix"); code != 0 {
				t.Fatalf("set: %d %s", code, e)
			}
		}
	}
	if got := list("--blocking"); got != "" {
		t.Fatalf("blocking after resolving: %q", got)
	}
	if got := list("--status", "wontfix", "--status=open"); got != "bdac" {
		t.Fatalf("repeated --status %q", got)
	}
	if got := list("--status", "wontfix"); got != "bd" {
		t.Fatalf("--status wontfix %q", got)
	}
	if _, err := w.move(ctx, task.Short, "reviewed", workflow.Args{}); err != nil {
		t.Fatalf("move with none blocking: %v", err)
	}
}

func TestFindingSetJSON(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	a, _ := workApp(t, st)
	task := newTask(t, st)
	runIn(a, "finding", "add", task.Short, "--severity", "P2", "--summary", "x")
	f := mustFindings(t, st, task.ID)[0]
	for _, ref := range []string{f.Short, f.ID} {
		code, out, e := runIn(a, "finding", "set", task.Short, ref, "--status", "deferred", "--json")
		var got store.Finding
		if code != 0 || json.Unmarshal([]byte(out), &got) != nil || got.Status != "deferred" || got.ID != f.ID {
			t.Fatalf("set by %s: %d %s %s", ref, code, out, e)
		}
	}
	if n := eventCount(t, st, task.ID, store.KindFindingStatus); n != 1 {
		t.Fatalf("%d status events, want 1 (second set was a no-op)", n)
	}
}

func TestFindingAddValuesAreNotFlags(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	a, _ := workApp(t, st)
	task := newTask(t, st)
	code, _, e := runIn(a, "finding", "add", task.Short, "--severity", "P2", "--summary", "--field=route=implementer")
	if code != 0 {
		t.Fatalf("add: %d %s", code, e)
	}
	fs := mustFindings(t, st, task.ID)
	if len(fs) != 1 || fs[0].Summary != "--field=route=implementer" || len(fs[0].Fields) != 0 {
		t.Fatalf("saved %+v", fs)
	}
	code, out, e := runIn(a, "finding", "add", task.Short, "--severity", "P2", "--summary", "--json")
	if code != 0 || strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("add: %d %q %s", code, out, e)
	}
	if fs = mustFindings(t, st, task.ID); len(fs) != 2 || fs[1].Summary != "--json" {
		t.Fatalf("saved %+v", fs)
	}
}

// readbackStore runs hook once, just before the first Findings read after an
// Update, as a concurrent writer would.
type readbackStore struct {
	store.Store
	armed bool
	hook  func()
}

func (r *readbackStore) Update(ctx context.Context, ref string, next func(store.Task, []store.Finding) ([]store.Event, error)) (store.Task, error) {
	t, _, err := r.UpdateEvents(ctx, ref, next)
	return t, err
}

func (r *readbackStore) UpdateEvents(ctx context.Context, ref string, next func(store.Task, []store.Finding) ([]store.Event, error)) (store.Task, []store.Event, error) {
	t, evs, err := r.Store.UpdateEvents(ctx, ref, next)
	r.armed = true
	return t, evs, err
}

func (r *readbackStore) Findings(ctx context.Context, ref string) ([]store.Finding, error) {
	if r.armed && r.hook != nil {
		r.armed = false
		r.hook()
	}
	return r.Store.Findings(ctx, ref)
}

func TestFindingAddAndSetSurviveConcurrentChange(t *testing.T) {
	mem := memstore.New(store.Options{Now: tick()})
	r := &readbackStore{Store: mem}
	a, _ := workApp(t, r)
	task := newTask(t, mem)
	fixAll := func() {
		for _, f := range mustFindings(t, mem, task.ID) {
			mem.Update(ctx, task.ID, func(store.Task, []store.Finding) ([]store.Event, error) {
				ev, _ := store.NewEvent(store.KindFindingStatus, store.FindingStatusData{Finding: f.ID, Status: store.StatusFixed})
				return []store.Event{ev}, nil
			})
		}
	}
	r.hook = fixAll
	code, out, e := runIn(a, "finding", "add", task.Short, "--severity", "P2", "--summary", "mine", "--json")
	var got addJSON
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || got.Finding.Summary != "mine" || got.Duplicate {
		t.Fatalf("add: %d %s %s", code, out, e)
	}
	r.hook = nil
	runIn(a, "finding", "add", task.Short, "--severity", "P2", "--summary", "other")
	other := mustFindings(t, mem, task.ID)[1]
	r.hook = func() {
		mem.Update(ctx, task.ID, func(store.Task, []store.Finding) ([]store.Event, error) {
			ev, _ := store.NewEvent(store.KindFindingStatus, store.FindingStatusData{Finding: other.ID, Status: store.StatusDeferred})
			return []store.Event{ev}, nil
		})
	}
	code, out, e = runIn(a, "finding", "set", task.Short, other.Short, "--status", "wontfix", "--json")
	var set store.Finding
	if code != 0 || json.Unmarshal([]byte(out), &set) != nil || set.ID != other.ID {
		t.Fatalf("set: %d %s %s", code, out, e)
	}
}

// TestFindingNoteEndToEnd runs both commands on a gitref store in a throwaway
// repository, under the review-loop workflow.
func TestFindingNoteEndToEnd(t *testing.T) {
	repo, _ := newRepo(t)
	loop := reviewLoopPath(t)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "none"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(t.TempDir(), "none"))
	a := &app{cwd: repo, getenv: func(k string) string {
		switch k {
		case "RIG_WORKFLOW":
			return loop
		case "RIG_ACTOR":
			return "tester"
		}
		return ""
	}}
	w, err := a.openWork(ctx, "", true)
	if err != nil {
		t.Fatal(err)
	}
	task, err := w.st.CreateTask(ctx, store.NewTask{Slug: "s", Title: "S", Initial: w.wf.Initial, Workflow: store.WorkflowRef{Name: w.wf.Name, Version: w.wf.Version}})
	if err != nil {
		t.Fatal(err)
	}

	code, out, e := runIn(a, "finding", "add", task.Short, "--severity", "P1", "--summary", "boom", "--field", "route=orchestrator", "--json")
	if code != 0 {
		t.Fatalf("add: %d %s", code, e)
	}
	if code, out2, _ := runIn(a, "finding", "add", task.Short, "--severity", "P1", "--summary", "boom", "--json"); code != 0 || !strings.Contains(out2, `"duplicate": true`) {
		t.Fatalf("duplicate: %d %s", code, out2)
	}
	if n := eventCount(t, w.st, task.ID, store.KindFinding); n != 1 {
		t.Fatalf("%d finding events, want 1", n)
	}
	var added addJSON
	if err := json.Unmarshal([]byte(out), &added); err != nil {
		t.Fatal(err)
	}
	code, out, _ = runIn(a, "finding", "list", task.Short, "--blocking", "--json")
	if code != 0 || !strings.Contains(out, added.Finding.ID) {
		t.Fatalf("blocking list: %d %s", code, out)
	}
	if code, _, _ := runIn(a, "finding", "set", task.Short, added.Finding.Short, "--status", "fixed"); code != 0 {
		t.Fatal("set failed")
	}
	if _, out, _ = runIn(a, "finding", "list", task.Short, "--json"); strings.TrimSpace(out) != "[]" {
		t.Fatalf("open list after fix: %s", out)
	}
	if code, _, _ := runIn(a, "note", "add", task.Short, "--kind", "decision", "--text", "ship it"); code != 0 {
		t.Fatal("note add failed")
	}
	if code, _, _ := runIn(a, "note", "add", "--kind", "history", "--text", "repo-wide"); code != 0 {
		t.Fatal("repo note add failed")
	}
	_, out, _ = runIn(a, "note", "list", task.Short, "--json")
	if !strings.Contains(out, "ship it") || strings.Contains(out, "repo-wide") {
		t.Fatalf("task notes: %s", out)
	}
	_, out, _ = runIn(a, "note", "list", "--json")
	if !strings.Contains(out, "repo-wide") {
		t.Fatalf("repository notes: %s", out)
	}
}

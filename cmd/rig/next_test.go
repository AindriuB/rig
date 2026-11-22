package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/forge"
	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/store/memstore"
)

// nextTask creates a task with the slug directly in state.
func nextTask(t *testing.T, st store.Store, slug, state string) store.Task {
	t.Helper()
	task, err := st.CreateTask(ctx, store.NewTask{Slug: slug, Title: slug, Initial: state, Workflow: store.WorkflowRef{Name: "t", Version: 1}})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// runNext runs `rig next` with args and returns the exit code and its output.
// The app's forge fails the test on any call.
func runNext(t *testing.T, a *app, args ...string) (int, string) {
	t.Helper()
	a.forge = func(context.Context, string) (forge.Forge, error) {
		t.Error("rig next called the forge")
		return nil, errors.New("no forge in rig next")
	}
	var out bytes.Buffer
	a.stdout = &out
	code := a.next(ctx, args)
	return code, out.String()
}

func nextJSON(t *testing.T, a *app, args ...string) nextOut {
	t.Helper()
	code, out := runNext(t, a, append(args, "--json")...)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	var got nextOut
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return got
}

func shorts(items []nextItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Slug)
	}
	return out
}

func find(t *testing.T, tr []nextTransition, to string) nextTransition {
	t.Helper()
	for _, x := range tr {
		if x.To == to {
			return x
		}
	}
	t.Fatalf("no transition to %s in %+v", to, tr)
	return nextTransition{}
}

func TestNextTransitionsAndGates(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	a, _ := workApp(t, st)
	task := nextTask(t, st, "alpha", "open")
	got := nextJSON(t, a)
	if len(got.Items) != 1 || got.Skipped != 0 {
		t.Fatalf("%+v", got)
	}
	it := got.Items[0]
	if it.Task != task.ID || it.Short != task.Short || it.Slug != "alpha" || it.State != "open" || it.Action != "start" || it.Who != "agent" {
		t.Fatalf("item %+v", it)
	}
	// No branch: shaIsHead fails.
	if b := find(t, it.Transitions, "bound"); b.OK == nil || *b.OK || len(b.Failing) != 1 || b.Failing[0] != "shaIsHead" {
		t.Fatalf("bound: %+v", b)
	}
	if d := find(t, it.Transitions, "done"); d.OK == nil || *d.OK || d.Failing[0] != "fieldSet" {
		t.Fatalf("done: %+v", d)
	}
	if r := find(t, it.Transitions, "reviewed"); r.OK == nil || !*r.OK || len(r.Failing) != 0 {
		t.Fatalf("reviewed: %+v", r)
	}
	// A command gate is not run: ok is null, and it is reported as such.
	c := find(t, it.Transitions, "checked")
	if c.OK != nil || len(c.Failing) != 0 || len(c.Gates) != 1 || c.Gates[0].Gate != "command" || c.Gates[0].OK != nil || c.Gates[0].Message != "not run" {
		t.Fatalf("checked: %+v", c)
	}
}

func TestNextJSONFieldNames(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	a, _ := workApp(t, st)
	nextTask(t, st, "alpha", "open")
	nextTask(t, st, "omega", "done")
	_, out := runNext(t, a, "--json")
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil || len(raw) != 2 || raw["items"] == nil || string(raw["skipped"]) != "1" {
		t.Fatalf("top level: %v %s", err, out)
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw["items"], &items); err != nil || len(items) != 1 {
		t.Fatal(err, out)
	}
	for _, k := range []string{"task", "short", "slug", "state", "action", "who", "transitions"} {
		if _, ok := items[0][k]; !ok {
			t.Errorf("item lacks %q: %s", k, out)
		}
	}
	if len(items[0]) != 7 {
		t.Errorf("item has extra keys: %s", out)
	}
	var trs []map[string]json.RawMessage
	if err := json.Unmarshal(items[0]["transitions"], &trs); err != nil || len(trs) == 0 {
		t.Fatal(err, out)
	}
	for _, k := range []string{"to", "ok", "failing"} {
		if _, ok := trs[0][k]; !ok {
			t.Errorf("transition lacks %q: %s", k, out)
		}
	}
}

func TestNextOrder(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	a, _ := workApp(t, st)
	// Created oldest first. Distances to "done": open 1, back 2 (back,
	// bound, done), paused 2 (human; paused, open, done), stopped and gated
	// have no path.
	nextTask(t, st, "a-open-old", "open")
	nextTask(t, st, "b-gated", "gated")
	nextTask(t, st, "c-stopped", "stopped")
	nextTask(t, st, "d-paused", "paused")
	nextTask(t, st, "e-back", "back")
	nextTask(t, st, "f-open-new", "open")
	nextTask(t, st, "g-done", "done")
	nextTask(t, st, "h-unknown", "vanished")

	got := nextJSON(t, a)
	want := []string{"d-paused", "c-stopped", "a-open-old", "f-open-new", "e-back", "b-gated", "h-unknown"}
	if g := shorts(got.Items); strings.Join(g, ",") != strings.Join(want, ",") {
		t.Fatalf("order %v, want %v", g, want)
	}
	if got.Skipped != 1 {
		t.Fatalf("skipped %d, want 1 (the final task)", got.Skipped)
	}
	last := got.Items[len(got.Items)-1]
	if last.Action != "unknown-state" || last.Who != "agent" || last.State != "vanished" {
		t.Fatalf("unknown state: %+v", last)
	}
	// "*" still lets an unknown state be stopped.
	if find(t, last.Transitions, "stopped").OK == nil {
		t.Fatalf("%+v", last.Transitions)
	}

	human := nextJSON(t, a, "--who", "human")
	if g := shorts(human.Items); strings.Join(g, ",") != "d-paused,c-stopped" || human.Skipped != 6 {
		t.Fatalf("--who human: %v skipped %d", g, human.Skipped)
	}
	lim := nextJSON(t, a, "--limit", "3")
	if g := shorts(lim.Items); len(g) != 3 || g[2] != "a-open-old" || lim.Skipped != 5 {
		t.Fatalf("--limit 3: %v skipped %d", g, lim.Skipped)
	}
}

func TestNextArgs(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	a, _ := workApp(t, st)
	for _, args := range [][]string{{"--bogus"}, {"--who", "robot"}, {"--limit", "0"}, {"--limit", "x"}, {"extra"}} {
		if code, _ := runNext(t, a, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	if code, out := runNext(t, a); code != 0 || !strings.Contains(out, "nothing to do") {
		t.Errorf("empty store: %d %q", code, out)
	}
}

func TestNextWritesNothing(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	a, _ := workApp(t, st)
	task := nextTask(t, st, "alpha", "open")
	before, _ := st.Events(ctx, task.ID)
	nextJSON(t, a)
	runNext(t, a)
	after, _ := st.Events(ctx, task.ID)
	if len(before) != len(after) {
		t.Fatalf("events %d -> %d", len(before), len(after))
	}
}

func TestNextGitref(t *testing.T) {
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

	bound := func() nextTransition {
		got := nextJSON(t, a)
		if len(got.Items) != 1 {
			t.Fatalf("%+v", got)
		}
		return find(t, got.Items[0].Transitions, "bound")
	}
	if b := bound(); b.OK == nil || *b.OK {
		t.Fatalf("no branch: %+v", b)
	}
	gitc(t, repo, "branch", "task/"+task.Short+"-s")
	// The sha argument is the branch head, so shaIsHead passes.
	if b := bound(); b.OK == nil || !*b.OK {
		t.Fatalf("branch present: %+v", b)
	}
}

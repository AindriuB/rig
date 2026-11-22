package main

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/store/memstore"
)

func TestNoteAddAndList(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	a, _ := workApp(t, st)
	task := newTask(t, st)

	code, out, e := runIn(a, "note", "add", task.Short, "--kind", "decision", "--text", "first", "--json")
	var n store.Note
	if code != 0 || json.Unmarshal([]byte(out), &n) != nil || n.Task != task.ID || n.Kind != "decision" || n.Text != "first" || n.Short == "" {
		t.Fatalf("add: %d %s %s", code, out, e)
	}
	var raw map[string]any
	_ = json.Unmarshal([]byte(out), &raw)
	for _, k := range []string{"id", "short", "task", "kind", "text", "created"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("note JSON lacks %q", k)
		}
	}
	runIn(a, "note", "add", task.Short, "--kind", "history", "--text", "second")
	runIn(a, "note", "add", task.Short, "--kind", "decision", "--text", "third")
	runIn(a, "note", "add", "--kind", "decision", "--text", "repo")

	texts := func(args ...string) []string {
		t.Helper()
		code, out, e := runIn(a, append([]string{"note", "list", "--json"}, args...)...)
		var ns []store.Note
		if code != 0 || json.Unmarshal([]byte(out), &ns) != nil {
			t.Fatalf("list %v: %d %s %s", args, code, out, e)
		}
		got := []string{}
		for _, x := range ns {
			got = append(got, x.Text)
		}
		return got
	}
	eq := func(got []string, want ...string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	eq(texts(task.Short), "third", "second", "first")
	eq(texts(task.Short, "--kind", "decision"), "third", "first")
	eq(texts(task.Short, "--limit", "2"), "third", "second")
	eq(texts("--kind", "none"))
	if all := texts(); len(all) != 4 {
		t.Fatalf("all notes: %v", all)
	}
}

func TestNoteErrors(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	a, _ := workApp(t, st)
	task := newTask(t, st)
	for name, args := range map[string][]string{
		"no kind":     {"note", "add", task.Short, "--text", "x"},
		"no text":     {"note", "add", task.Short, "--kind", "k"},
		"unknown flg": {"note", "add", task.Short, "--kind", "k", "--text", "x", "--bogus"},
		"missing":     {"note", "add", "nope", "--kind", "k", "--text", "x"},
		"too many":    {"note", "add", "a", "b", "--kind", "k", "--text", "x"},
		"bad limit":   {"note", "list", "--limit", "x"},
		"list flag":   {"note", "list", "--bogus"},
		"no sub":      {"note"},
		"unknown sub": {"note", "frob"},
	} {
		if code, _, _ := runIn(a, args...); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
	}
}

package storetest

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/AindriuB/rig/internal/store"
)

func linkedTask(slug, key string) store.NewTask {
	return store.NewTask{
		Slug: slug, Title: "T " + slug, Workflow: basic, Initial: "todo",
		External: &store.External{
			Kind: "jira", Host: "j.example", Project: "AB", Key: key, URL: "https://j.example/" + key,
			Type: "Story", Owned: []string{"title", "body"}, ParentKey: "AB-1", Rev: "r1",
		},
		Snapshot: map[string]any{"title": "T " + slug},
	}
}

func testLinkedTask(t *testing.T, s store.Store) {
	ctx := context.Background()
	task, err := s.CreateTask(ctx, linkedTask("linked", "AB-7"))
	if err != nil {
		t.Fatal(err)
	}
	e := task.External
	if e == nil || e.Kind != "jira" || e.Host != "j.example" || e.Key != "AB-7" || e.Type != "Story" ||
		e.ParentKey != "AB-1" || !reflect.DeepEqual(e.Owned, []string{"title", "body"}) ||
		e.Rev != "r1" || e.SyncedAt.IsZero() || e.Snapshot["title"] != "T linked" {
		t.Fatalf("external = %+v", e)
	}
	got, err := s.Task(ctx, "ab-7")
	if err != nil || !reflect.DeepEqual(got, task) {
		t.Fatalf("Task(ab-7) = %+v, %v; want %+v", got, err, task)
	}
	if evs := events(t, s, task.ID); len(evs) != 1 {
		t.Fatalf("%d events, want the created event alone", len(evs))
	}
	// A later tracker-sync replaces the snapshot.
	task = update(t, s, task.ID, []string{store.KindTrackerSync},
		store.TrackerSyncData{Rev: "r2", Values: map[string]any{"title": "New"}})
	if task.External.Rev != "r2" || task.External.Snapshot["title"] != "New" || task.External.SyncedAt.IsZero() {
		t.Fatalf("after sync: %+v", task.External)
	}
}

// A repeated (kind, host, key) returns the existing task and writes nothing.
func testLinkedRepeat(t *testing.T, s store.Store) {
	ctx := context.Background()
	first, err := s.CreateTask(ctx, linkedTask("one", "AB-7"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CreateTask(ctx, linkedTask("two", "AB-7"))
	if !errors.Is(err, store.ErrLinked) || again.ID != first.ID {
		t.Fatalf("repeat = %+v, %v", again, err)
	}
	if all := mustTasks(t, s); len(all) != 1 {
		t.Fatalf("%d tasks, want 1", len(all))
	}
	other := linkedTask("three", "AB-7")
	other.External.Host = "other.example"
	if _, err := s.CreateTask(ctx, other); err != nil {
		t.Fatalf("same key on another host: %v", err)
	}
	if _, err := s.CreateTask(ctx, linkedTask("four", "AB-8")); err != nil {
		t.Fatalf("other key: %v", err)
	}
}

func testParentedTask(t *testing.T, s store.Store) {
	ctx := context.Background()
	parent := create(t, s, "epic")
	n := store.NewTask{Slug: "kid", Workflow: basic, Initial: "todo", Parent: "epic"}
	kid, err := s.CreateTask(ctx, n)
	if err != nil || kid.Parent != parent.ID {
		t.Fatalf("kid = %+v, %v; want parent %s", kid, err, parent.ID)
	}
	if got, _ := s.Task(ctx, "kid"); got.Parent != parent.ID {
		t.Fatalf("refolded parent = %q", got.Parent)
	}
	n.Slug, n.Parent = "orphan", "nothing"
	if _, err := s.CreateTask(ctx, n); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown parent err = %v", err)
	}
	if all := mustTasks(t, s); len(all) != 2 {
		t.Fatalf("%d tasks, want 2", len(all))
	}
}

// Parent events fold, survive a refold and clear; CheckParent refuses a
// self-parent and a cycle.
func testParentUpdates(t *testing.T, s store.Store) {
	ctx := context.Background()
	a, b := create(t, s, "a"), create(t, s, "b")
	if got := update(t, s, b.ID, []string{store.KindParent}, store.ParentData{Task: a.ID}); got.Parent != a.ID {
		t.Fatalf("parent = %q", got.Parent)
	}
	if got, _ := s.Task(ctx, b.ID); got.Parent != a.ID {
		t.Fatalf("refolded parent = %q", got.Parent)
	}
	all := mustTasks(t, s)
	if err := store.CheckParent(a.ID, a.ID, all); err == nil {
		t.Fatal("self parent allowed")
	}
	if err := store.CheckParent(a.ID, b.ID, all); err == nil {
		t.Fatal("cycle allowed")
	}
	if got := update(t, s, b.ID, []string{store.KindParent}, store.ParentData{}); got.Parent != "" {
		t.Fatalf("parent after clear = %q", got.Parent)
	}
	if err := store.CheckParent(a.ID, b.ID, mustTasks(t, s)); err != nil {
		t.Fatalf("after clear: %v", err)
	}
}

func mustTasks(t *testing.T, s store.Store) []store.Task {
	t.Helper()
	all, err := s.Tasks(context.Background(), store.TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return all
}

func testBranchStamped(t *testing.T, s store.Store) {
	ctx := context.Background()
	n := linkedTask("stamp", "AB-7")
	n.BranchTemplate = "rig/{key}-{slug}-{short}"
	n.Branch = "ignored"
	task, err := s.CreateTask(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	if want := "rig/ab-7-stamp-" + task.Short; task.Branch != want {
		t.Fatalf("branch = %q, want %q", task.Branch, want)
	}
	if got, _ := s.Task(ctx, task.ID); got.Branch != task.Branch {
		t.Fatalf("refolded branch = %q", got.Branch)
	}
	plain := store.NewTask{Slug: "plain", Workflow: basic, Initial: "todo", Branch: "task/plain"}
	if got, err := s.CreateTask(ctx, plain); err != nil || got.Branch != "task/plain" {
		t.Fatalf("no template: %+v, %v", got, err)
	}
}

// Resolve finds a task by external key; a key equal to another task's slug
// is ambiguous.
func testResolveByKey(t *testing.T, s store.Store) {
	ctx := context.Background()
	task, err := s.CreateTask(ctx, linkedTask("keyed", "AB-9"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"AB-9", "ab-9"} {
		if got, err := s.Task(ctx, ref); err != nil || got.ID != task.ID {
			t.Fatalf("Task(%q) = %+v, %v", ref, got, err)
		}
	}
	create(t, s, "ab-9")
	if _, err := s.Task(ctx, "ab-9"); !errors.Is(err, store.ErrAmbiguous) {
		t.Fatalf("Task(ab-9) err = %v, want ErrAmbiguous", err)
	}
}

// A link is unique by kind, host (any case) and key (any case); the host is
// stored lower-cased and the key as the tracker gave it.
func testLinkedCaseAndKind(t *testing.T, s store.Store) {
	ctx := context.Background()
	first, err := s.CreateTask(ctx, linkedTask("one", "AB-7"))
	if err != nil {
		t.Fatal(err)
	}
	lowKey := linkedTask("two", "ab-7")
	if got, err := s.CreateTask(ctx, lowKey); !errors.Is(err, store.ErrLinked) || got.ID != first.ID {
		t.Fatalf("key in another case = %+v, %v", got, err)
	}
	upHost := linkedTask("three", "AB-7")
	upHost.External.Host = "J.EXAMPLE"
	if got, err := s.CreateTask(ctx, upHost); !errors.Is(err, store.ErrLinked) || got.ID != first.ID {
		t.Fatalf("host in another case = %+v, %v", got, err)
	}
	kind := linkedTask("four", "AB-7")
	kind.External.Kind = "github"
	k, err := s.CreateTask(ctx, kind)
	if err != nil {
		t.Fatalf("same host and key, other kind: %v", err)
	}
	if k.ID == first.ID {
		t.Fatal("other kind returned the first task")
	}
	mixed := linkedTask("five", "CD-1")
	mixed.External.Host = "Mixed.Example"
	m, err := s.CreateTask(ctx, mixed)
	if err != nil || m.External.Host != "mixed.example" || m.External.Key != "CD-1" {
		t.Fatalf("created link = %+v, %v", m.External, err)
	}
	if got, _ := s.Task(ctx, m.ID); got.External.Host != "mixed.example" {
		t.Fatalf("refolded host = %q", got.External.Host)
	}
	got := update(t, s, m.ID, []string{store.KindExternal}, store.ExternalData{Kind: "jira", Host: "UP.Example", Key: "CD-1"})
	if got.External.Host != "up.example" {
		t.Fatalf("external event host = %q", got.External.Host)
	}
}

// An external event with an empty kind and key clears the link, and the issue
// can then be linked to a new task. It is refused on an unlinked task and when
// only one of kind and key is empty.
func testUnlinkTask(t *testing.T, s store.Store) {
	ctx := context.Background()
	task, err := s.CreateTask(ctx, linkedTask("one", "AB-7"))
	if err != nil {
		t.Fatal(err)
	}
	updateErr := func(ref string, d store.ExternalData) error {
		_, err := s.Update(ctx, ref, func(store.Task, []store.Finding) ([]store.Event, error) {
			e, err := store.NewEvent(store.KindExternal, d)
			return []store.Event{e}, err
		})
		return err
	}
	for _, d := range []store.ExternalData{{Kind: "jira"}, {Key: "AB-7"}} {
		if err := updateErr(task.ID, d); err == nil {
			t.Fatalf("half-empty %+v accepted", d)
		}
	}
	if got, _ := s.Task(ctx, task.ID); got.External == nil {
		t.Fatal("a refused event cleared the link")
	}
	got := update(t, s, task.ID, []string{store.KindExternal}, store.ExternalData{})
	if got.External != nil {
		t.Fatalf("after clear: %+v", got.External)
	}
	if again, _ := s.Task(ctx, task.ID); again.External != nil {
		t.Fatalf("refolded: %+v", again.External)
	}
	if err := updateErr(task.ID, store.ExternalData{}); err == nil {
		t.Fatal("clearing an unlinked task accepted")
	}
	if _, err := s.CreateTask(ctx, linkedTask("two", "AB-7")); err != nil {
		t.Fatalf("relink after clear: %v", err)
	}
}

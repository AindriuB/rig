// Package storetest is the contract test every store backend passes.
//
// A backend's test calls Run with a function that returns a new, empty store
// for each subtest.
package storetest

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/store"
)

// Run runs the contract against stores from newStore. Each subtest gets a
// store of its own.
func Run(t *testing.T, newStore func(t *testing.T) store.Store) {
	tests := []struct {
		name string
		fn   func(*testing.T, store.Store)
	}{
		{"CreateAndResolve", testCreateAndResolve},
		{"Ambiguous", testAmbiguous},
		{"NotFound", testNotFound},
		{"UpdateAppendsInOrder", testUpdateAppendsInOrder},
		{"UpdateRefusalWritesNothing", testUpdateRefusalWritesNothing},
		{"Findings", testFindings},
		{"Notes", testNotes},
		{"TasksNewestFirst", testTasksNewestFirst},
		{"LinkedTask", testLinkedTask},
		{"LinkedRepeatWritesNothing", testLinkedRepeat},
		{"ParentedTask", testParentedTask},
		{"ParentUpdates", testParentUpdates},
		{"BranchStamped", testBranchStamped},
		{"ResolveByKey", testResolveByKey},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { tc.fn(t, newStore(t)) })
	}
}

var basic = store.WorkflowRef{Name: "basic", Version: 1}

func create(t *testing.T, s store.Store, slug string) store.Task {
	t.Helper()
	task, err := s.CreateTask(context.Background(), store.NewTask{
		Slug: slug, Title: "Title of " + slug, Body: "body", Branch: "task/" + slug,
		Fields: map[string]any{"size": "s"}, Workflow: basic, Initial: "todo",
	})
	if err != nil {
		t.Fatalf("CreateTask(%s): %v", slug, err)
	}
	return task
}

func update(t *testing.T, s store.Store, ref string, kinds []string, data ...any) store.Task {
	t.Helper()
	task, err := s.Update(context.Background(), ref, func(store.Task, []store.Finding) ([]store.Event, error) {
		var evs []store.Event
		for i, k := range kinds {
			e, err := store.NewEvent(k, data[i])
			if err != nil {
				return nil, err
			}
			evs = append(evs, e)
		}
		return evs, nil
	})
	if err != nil {
		t.Fatalf("Update(%s): %v", ref, err)
	}
	return task
}

func events(t *testing.T, s store.Store, ref string) []store.Event {
	t.Helper()
	evs, err := s.Events(context.Background(), ref)
	if err != nil {
		t.Fatalf("Events(%s): %v", ref, err)
	}
	return evs
}

func testCreateAndResolve(t *testing.T, s store.Store) {
	ctx := context.Background()
	task := create(t, s, "add-parser")
	if len(task.ID) != 26 || !strings.HasPrefix(task.Short, store.TaskPrefix) ||
		task.Short != store.TaskPrefix+strings.ToLower(task.ID[22:]) {
		t.Fatalf("id %q short %q", task.ID, task.Short)
	}
	if task.Slug != "add-parser" || task.Title != "Title of add-parser" || task.Body != "body" ||
		task.Branch != "task/add-parser" || task.State != "todo" || task.Workflow != basic ||
		task.Fields["size"] != "s" || task.Created.IsZero() || !task.Updated.Equal(task.Created) {
		t.Fatalf("created task = %+v", task)
	}
	for _, ref := range []string{task.ID, strings.ToLower(task.ID), task.Short, task.Slug} {
		got, err := s.Task(ctx, ref)
		if err != nil || !reflect.DeepEqual(got, task) {
			t.Fatalf("Task(%q) = %+v, %v; want %+v", ref, got, err, task)
		}
	}
	evs := events(t, s, task.Slug)
	if len(evs) != 1 || evs[0].Kind != store.KindCreated || evs[0].Seq != 1 ||
		evs[0].ID != task.ID || evs[0].V != store.Version || evs[0].Actor == "" {
		t.Fatalf("events = %+v", evs)
	}
}

func testAmbiguous(t *testing.T, s store.Store) {
	a := create(t, s, "same")
	b := create(t, s, "same")
	_, err := s.Task(context.Background(), "same")
	var amb *store.AmbiguousError
	if !errors.Is(err, store.ErrAmbiguous) || !errors.As(err, &amb) {
		t.Fatalf("Task(same) err = %v, want ErrAmbiguous", err)
	}
	if len(amb.Matches) != 2 || !contains(amb.Matches, a.ID) || !contains(amb.Matches, b.ID) {
		t.Fatalf("matches = %v, want %s and %s", amb.Matches, a.ID, b.ID)
	}
	if _, err := s.Update(context.Background(), "same", nil); !errors.Is(err, store.ErrAmbiguous) {
		t.Fatalf("Update(same) err = %v, want ErrAmbiguous", err)
	}
	for _, want := range []store.Task{a, b} {
		if got, err := s.Task(context.Background(), want.Short); err != nil || got.ID != want.ID {
			t.Fatalf("Task(%s) = %s, %v", want.Short, got.ID, err)
		}
	}
}

func testNotFound(t *testing.T, s store.Store) {
	ctx := context.Background()
	create(t, s, "real")
	if _, err := s.Task(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Task err = %v", err)
	}
	if _, err := s.Update(ctx, "nope", nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Update err = %v", err)
	}
	if _, err := s.Events(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Events err = %v", err)
	}
	if _, err := s.Findings(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Findings err = %v", err)
	}
	if _, err := s.AddNote(ctx, store.Note{Task: "nope", Kind: "decision", Text: "x"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("AddNote err = %v", err)
	}
	if _, err := s.Notes(ctx, store.NoteFilter{Task: "nope"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Notes err = %v", err)
	}
}

func testUpdateAppendsInOrder(t *testing.T, s store.Store) {
	task := create(t, s, "work")
	got := update(t, s, task.Short,
		[]string{store.KindTransition, store.KindField, store.KindCount},
		store.TransitionData{From: "todo", To: "doing", Sha: "abc"},
		store.FieldData{Name: "title", Value: "New title"},
		store.CountData{Counter: "round", By: 1},
	)
	if got.State != "doing" || got.Previous != "todo" || got.StateSha != "abc" ||
		got.Title != "New title" || got.Counters["round"] != 1 {
		t.Fatalf("updated task = %+v", got)
	}
	update(t, s, task.ID, []string{store.KindCount}, store.CountData{Counter: "round", By: 2})

	evs := events(t, s, task.Slug)
	if len(evs) != 5 {
		t.Fatalf("len(events) = %d, want 5", len(evs))
	}
	for i, e := range evs {
		if e.Seq != i+1 || e.V != store.Version || e.Time.IsZero() || e.Actor == "" || len(e.ID) != 26 {
			t.Fatalf("event %d = %+v", i, e)
		}
		if i > 0 && e.ID <= evs[i-1].ID {
			t.Fatalf("event ids out of order: %s then %s", evs[i-1].ID, e.ID)
		}
	}
	again, err := s.Task(context.Background(), task.ID)
	if err != nil || again.Counters["round"] != 3 {
		t.Fatalf("Task after updates = %+v, %v", again, err)
	}
}

func testUpdateRefusalWritesNothing(t *testing.T, s store.Store) {
	ctx := context.Background()
	task := create(t, s, "guarded")
	before := events(t, s, task.ID)
	refused := errors.New("gate says no")
	_, err := s.Update(ctx, task.ID, func(store.Task, []store.Finding) ([]store.Event, error) {
		return nil, refused
	})
	if !errors.Is(err, refused) {
		t.Fatalf("Update err = %v, want the callback's error", err)
	}
	// An event the store cannot append also writes nothing, even after a
	// good one in the same batch.
	_, err = s.Update(ctx, task.ID, func(store.Task, []store.Finding) ([]store.Event, error) {
		good, _ := store.NewEvent(store.KindCount, store.CountData{Counter: "round", By: 1})
		bad, _ := store.NewEvent(store.KindFindingStatus, store.FindingStatusData{Finding: "f-none", Status: store.StatusFixed})
		return []store.Event{good, bad}, nil
	})
	if err == nil {
		t.Fatal("Update with a bad event succeeded")
	}
	if after := events(t, s, task.ID); !reflect.DeepEqual(after, before) {
		t.Fatalf("a refused update wrote events: %+v", after)
	}
}

func testFindings(t *testing.T, s store.Store) {
	ctx := context.Background()
	task := create(t, s, "reviewed")
	update(t, s, task.ID, []string{store.KindCount}, store.CountData{Counter: "round", By: 1})
	got := update(t, s, task.ID, []string{store.KindFinding, store.KindFinding},
		store.FindingData{Severity: "high", Summary: "Nil  deref", Path: "a.go:3"},
		store.FindingData{Severity: "low", Summary: "naming"},
	)
	if got.OpenFindings != 2 {
		t.Fatalf("OpenFindings = %d, want 2", got.OpenFindings)
	}
	fs, err := s.Findings(ctx, task.Slug)
	if err != nil || len(fs) != 2 {
		t.Fatalf("Findings = %+v, %v", fs, err)
	}
	f := fs[0]
	if len(f.ID) != 26 || !strings.HasPrefix(f.Short, store.FindingPrefix) || f.Short == fs[1].Short ||
		f.Task != task.ID || f.Status != store.StatusOpen || f.Severity != "high" ||
		f.Key != store.DefaultKey("high", "a.go:3", "Nil  deref") || f.Counters["round"] != 1 {
		t.Fatalf("finding = %+v", f)
	}

	var seen []store.Finding
	got, err = s.Update(ctx, task.ID, func(_ store.Task, fs []store.Finding) ([]store.Event, error) {
		seen = fs
		e, err := store.NewEvent(store.KindFindingStatus, store.FindingStatusData{Finding: f.Short, Status: store.StatusFixed})
		return []store.Event{e}, err
	})
	if err != nil || len(seen) != 2 || got.OpenFindings != 1 {
		t.Fatalf("status update: open %d, callback saw %d findings, err %v", got.OpenFindings, len(seen), err)
	}
	fs, _ = s.Findings(ctx, task.ID)
	if fs[0].Status != store.StatusFixed || fs[1].Status != store.StatusOpen {
		t.Fatalf("statuses = %s, %s", fs[0].Status, fs[1].Status)
	}
	if open, ok := store.FindOpenByKey(fs, store.DefaultKey("high", "a.go:9", "nil deref")); ok {
		t.Fatalf("fixed finding still matched as open: %+v", open)
	}
}

func testNotes(t *testing.T, s store.Store) {
	ctx := context.Background()
	task := create(t, s, "noted")
	repo, err := s.AddNote(ctx, store.Note{Kind: "history", Text: "phase 1 landed"})
	if err != nil {
		t.Fatalf("AddNote(repo): %v", err)
	}
	onTask, err := s.AddNote(ctx, store.Note{Task: task.Slug, Kind: "decision", Text: "use ULIDs"})
	if err != nil {
		t.Fatalf("AddNote(task): %v", err)
	}
	if repo.Task != "" || len(repo.ID) != 26 || !strings.HasPrefix(repo.Short, store.NotePrefix) || repo.Created.IsZero() {
		t.Fatalf("repo note = %+v", repo)
	}
	if onTask.Task != task.ID || onTask.Kind != "decision" || onTask.Text != "use ULIDs" {
		t.Fatalf("task note = %+v", onTask)
	}
	if _, err := s.AddNote(ctx, store.Note{Kind: "decision"}); err == nil {
		t.Fatal("AddNote with no text succeeded")
	}

	all, err := s.Notes(ctx, store.NoteFilter{})
	if err != nil || len(all) != 2 || all[0].ID != onTask.ID || all[1].ID != repo.ID {
		t.Fatalf("Notes() = %+v, %v; want task note then repo note", all, err)
	}
	if got, _ := s.Notes(ctx, store.NoteFilter{Task: task.Short}); len(got) != 1 || got[0].ID != onTask.ID {
		t.Fatalf("Notes(task) = %+v", got)
	}
	if got, _ := s.Notes(ctx, store.NoteFilter{Kind: "history"}); len(got) != 1 || got[0].ID != repo.ID {
		t.Fatalf("Notes(kind) = %+v", got)
	}
	if got, _ := s.Notes(ctx, store.NoteFilter{Limit: 1}); len(got) != 1 || got[0].ID != onTask.ID {
		t.Fatalf("Notes(limit 1) = %+v", got)
	}
	if evs := events(t, s, task.ID); evs[len(evs)-1].Kind != store.KindNote {
		t.Fatalf("task note not in the task's log: %+v", evs)
	}
}

func testTasksNewestFirst(t *testing.T, s store.Store) {
	ctx := context.Background()
	a := create(t, s, "first")
	b := create(t, s, "second")
	c := create(t, s, "third")
	update(t, s, b.ID, []string{store.KindTransition}, store.TransitionData{From: "todo", To: "done"})

	all, err := s.Tasks(ctx, store.TaskFilter{})
	if err != nil || len(all) != 3 || all[0].ID != c.ID || all[1].ID != b.ID || all[2].ID != a.ID {
		t.Fatalf("Tasks() = %v, %v; want third, second, first", ids(all), err)
	}
	todo, err := s.Tasks(ctx, store.TaskFilter{States: []string{"todo"}})
	if err != nil || len(todo) != 2 || todo[0].ID != c.ID || todo[1].ID != a.ID {
		t.Fatalf("Tasks(todo) = %v, %v", ids(todo), err)
	}
}

func ids(tasks []store.Task) []string {
	out := make([]string, len(tasks))
	for i, t := range tasks {
		out[i] = t.Slug
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

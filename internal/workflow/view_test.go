package workflow

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/AindriuB/rig/internal/store"
)

func TestViewUnlinkedGolden(t *testing.T) {
	w := fixture(t)
	task := store.Task{
		ID: "01ABC", Short: "abc", Slug: "s", Title: "T", Body: "B",
		State: "planned", Branch: "task/abc",
		Created: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Updated: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC),
	}
	b, err := json.Marshal(w.View(task, ""))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"01ABC","short":"abc","slug":"s","title":"T","body":"B","fields":{},"workflow":{"name":"","version":0},"state":"planned","recordedState":"planned","stateSha":"","shas":{},"branch":"task/abc","counters":{},"openFindings":0,"created":"2026-01-02T03:04:05Z","updated":"2026-01-02T03:04:06Z"}`
	if string(b) != want {
		t.Fatalf("unlinked view changed:\n got %s\nwant %s", b, want)
	}
}

func linkedTask() store.Task {
	return store.Task{
		ID: "01ABC", Short: "abc", Title: "Same", Body: "Body",
		Fields: map[string]any{"labels": []string{"x", "y"}, "prio": "high"},
		Parent: "01PARENT",
		External: &store.External{
			Kind: "jira", Host: "h", Project: "P", Key: "P-1", URL: "u", Type: "Story",
			Owned:    []string{"title", "body", "labels", "prio"},
			SyncedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Rev: "7",
			// As read back from JSON: lists are []any.
			Snapshot: map[string]any{"title": "Same", "body": "Body", "labels": []any{"x", "y"}, "prio": "high"},
		},
	}
}

func driftOf(t *testing.T, task store.Task) []string {
	t.Helper()
	v := fixture(t).View(task, "")
	if v.External == nil {
		t.Fatal("no external in the view")
	}
	return v.External.Drift
}

func TestViewLinked(t *testing.T) {
	v := fixture(t).View(linkedTask(), "")
	if v.Parent != "01PARENT" || v.External == nil || v.External.Key != "P-1" || v.External.Rev != "7" ||
		v.External.Type != "Story" || v.External.Project != "P" || v.External.URL != "u" || v.External.Host != "h" {
		t.Fatalf("view = %+v %+v", v, v.External)
	}
	b, _ := json.Marshal(v)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	ext := m["external"].(map[string]any)
	for _, k := range []string{"kind", "host", "project", "key", "url", "type", "owned", "syncedAt", "rev", "drift"} {
		if _, ok := ext[k]; !ok {
			t.Errorf("external has no %q: %v", k, ext)
		}
	}
	if _, ok := ext["snapshot"]; ok {
		t.Error("external exposes the snapshot")
	}
	if m["parent"] != "01PARENT" {
		t.Errorf("parent = %v", m["parent"])
	}
}

func TestViewDrift(t *testing.T) {
	if d := driftOf(t, linkedTask()); len(d) != 0 {
		t.Errorf("unchanged task drifts: %v", d)
	}
	b, _ := json.Marshal(fixture(t).View(linkedTask(), ""))
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if d, ok := m["external"].(map[string]any)["drift"].([]any); !ok || len(d) != 0 {
		t.Errorf("drift JSON = %v, want []", m["external"])
	}

	task := linkedTask()
	task.Title = "Changed"
	if d := driftOf(t, task); !slices.Equal(d, []string{"title"}) {
		t.Errorf("changed title drift = %v", d)
	}

	task = linkedTask()
	task.Fields = map[string]any{"labels": []string{"x", "z"}, "prio": "high"}
	task.Body = "other"
	if d := driftOf(t, task); !slices.Equal(d, []string{"body", "labels"}) {
		t.Errorf("changed list drift = %v", d)
	}

	task = linkedTask()
	task.Fields = map[string]any{"labels": []string{"x", "y"}}
	if d := driftOf(t, task); !slices.Equal(d, []string{"prio"}) {
		t.Errorf("removed field drift = %v", d)
	}

	task = linkedTask()
	task.Fields["prio"] = "high"
	delete(task.External.Snapshot, "prio")
	if d := driftOf(t, task); !slices.Equal(d, []string{"prio"}) {
		t.Errorf("field only local drift = %v", d)
	}

	task = linkedTask()
	task.Fields["extra"] = "not owned"
	if d := driftOf(t, task); len(d) != 0 {
		t.Errorf("unowned field drifts: %v", d)
	}
}

package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// AC2: a log written before phase 3 folds to the same Task as before, and
// its JSON gains no field.
func TestFoldOldLogHasNoLinkFields(t *testing.T) {
	task, _, _, skipped := Fold(sampleLog(t))
	if skipped != 0 || task.Parent != "" || task.External != nil {
		t.Fatalf("skipped %d parent %q external %+v", skipped, task.Parent, task.External)
	}
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"parent"`, `"external"`} {
		if strings.Contains(string(raw), key) {
			t.Fatalf("old task JSON has %s: %s", key, raw)
		}
	}
}

var ext = ExternalData{
	Kind: "github", Host: "github.com", Project: "o/r", Key: "o/r#42", URL: "u",
	Type: "issue", Owned: []string{"title", "body"}, ParentKey: "o/r#1",
}

var snap = TrackerSyncData{At: t0, Rev: "r7", Values: map[string]any{"title": "T"}}

func createdLog(t *testing.T, d CreatedData) []Event {
	return []Event{ev(t, 1, KindCreated, d)}
}

// A created event carrying parent, external and snapshot folds exactly as the
// separate events do.
func TestFoldCreatedEqualsSeparateEvents(t *testing.T) {
	base := CreatedData{Short: "t-0001", Slug: "x", Workflow: WorkflowRef{Name: "basic", Version: 1}, State: "todo"}
	one := base
	one.Parent, one.External, one.Snapshot = "P1", &ext, &snap
	a, _, _, skipped := Fold(createdLog(t, one))
	if skipped != 0 {
		t.Fatalf("skipped = %d", skipped)
	}
	log := createdLog(t, base)
	log = append(log, ev(t, 1, KindParent, ParentData{Task: "P1"}), ev(t, 1, KindExternal, ext), ev(t, 1, KindTrackerSync, snap))
	for i := range log[1:] {
		log[i+1].Seq = i + 2
		log[i+1].ID = ev(t, i+2, "", nil).ID
		log[i+1].Time = log[0].Time
	}
	b, _, _, skipped := Fold(log)
	if skipped != 0 {
		t.Fatalf("separate: skipped = %d", skipped)
	}
	b.Updated = a.Updated
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("created %+v\nseparate %+v", a, b)
	}
	e := a.External
	if a.Parent != "P1" || e.Kind != "github" || e.Key != "o/r#42" || e.ParentKey != "o/r#1" ||
		!reflect.DeepEqual(e.Owned, ext.Owned) || e.Rev != "r7" || !e.SyncedAt.Equal(t0) ||
		e.Snapshot["title"] != "T" {
		t.Fatalf("task = %+v external %+v", a, e)
	}
}

func TestFoldParentLastWinsAndClears(t *testing.T) {
	log := createdLog(t, CreatedData{Short: "t-1", Slug: "x", State: "todo"})
	log = append(log, ev(t, 2, KindParent, ParentData{Task: "A"}), ev(t, 3, KindParent, ParentData{Task: "B"}))
	if task, _, _, _ := Fold(log); task.Parent != "B" {
		t.Fatalf("parent = %q", task.Parent)
	}
	log = append(log, ev(t, 4, KindParent, ParentData{Task: ""}))
	if task, _, _, _ := Fold(log); task.Parent != "" {
		t.Fatalf("parent = %q after clear", task.Parent)
	}
}

func TestFoldTrackerSyncNeedsLink(t *testing.T) {
	log := append(createdLog(t, CreatedData{Short: "t-1", Slug: "x", State: "todo"}), ev(t, 2, KindTrackerSync, snap))
	if _, _, _, skipped := Fold(log); skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}
}

func TestExpandBranch(t *testing.T) {
	got := ExpandBranch("feat/{key}-{slug}-{short}-{id}", "t-ab12", "add-it", "01ID", "PROJ_1/x#42")
	if want := "feat/proj-1-x-42-add-it-t-ab12-01ID"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// With no key, {key} is the short id.
	for tmpl, want := range map[string]string{
		"rig/{key}":     "rig/t-ab12",
		"{key}-{slug}":  "t-ab12-add-it",
		"{key}/{short}": "t-ab12/t-ab12",
	} {
		if got := ExpandBranch(tmpl, "t-ab12", "add-it", "01ID", ""); got != want {
			t.Fatalf("no key: ExpandBranch(%q) = %q, want %q", tmpl, got, want)
		}
	}
}

func TestValidBranch(t *testing.T) {
	for _, name := range []string{"rig/t-ab12", "t-ab12-add-it", "feat/ab-7/x", "a.b", "a@b"} {
		if !ValidBranch(name) {
			t.Errorf("ValidBranch(%q) = false", name)
		}
	}
	for _, name := range []string{
		"", "@", "HEAD", "rig/", "/x", "a//b", "-slug", "a..b", "a.", "a/.b", "a.lock", "a/b.lock/c",
		"a@{b", "a b", "a~b", "a^b", "a:b", "a?b", "a*b", "a[b", `a\b`, "a\x7fb", "a\tb",
	} {
		if ValidBranch(name) {
			t.Errorf("ValidBranch(%q) = true", name)
		}
	}
}

func TestOptionsCreatedUnlinkedBranch(t *testing.T) {
	o := Options{Now: func() time.Time { return t0 }}
	e, err := o.Created(NewTask{Slug: "s", Initial: "todo", BranchTemplate: "rig/{key}"}, func(string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	var d CreatedData
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if d.Branch != "rig/"+d.Short {
		t.Fatalf("branch = %q short %q", d.Branch, d.Short)
	}
	if _, err := o.Created(NewTask{Slug: "s", Initial: "todo", BranchTemplate: "rig/{slug}.lock"}, func(string) bool { return false }); err == nil {
		t.Fatal("invalid branch accepted")
	}
}

func TestOptionsCreatedStampsBranch(t *testing.T) {
	o := Options{Now: func() time.Time { return t0 }}
	e, err := o.Created(NewTask{
		Slug: "s", Initial: "todo", BranchTemplate: "{key}/{short}", Branch: "ignored",
		External: &External{Kind: "jira", Key: "AB-7"},
	}, func(string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	var d CreatedData
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if d.Branch != "ab-7/"+d.Short {
		t.Fatalf("branch = %q short %q", d.Branch, d.Short)
	}
	if _, err := o.Created(NewTask{Slug: "s", Initial: "todo", External: &External{Kind: "jira"}}, func(string) bool { return false }); err == nil {
		t.Fatal("link without key accepted")
	}
}

// AC4: Resolve matches an external key; a key equal to another slug is
// ambiguous.
func TestResolveExternalKey(t *testing.T) {
	a := Task{ID: "A", Short: "t-a", Slug: "one", External: &External{Kind: "jira", Key: "AB-7"}}
	b := Task{ID: "B", Short: "t-b", Slug: "ab-7"}
	c := Task{ID: "C", Short: "t-c", Slug: "three", External: &External{Kind: "jira", Key: "ab-7"}}
	if got, err := Resolve("ab-7", []Task{a}); err != nil || got.ID != "A" {
		t.Fatalf("Resolve = %+v, %v", got, err)
	}
	if got, err := Resolve("AB-7", []Task{a, {ID: "Z", Slug: "z"}}); err != nil || got.ID != "A" {
		t.Fatalf("Resolve exact = %+v, %v", got, err)
	}
	if _, err := Resolve("ab-7", []Task{a, b}); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("slug clash err = %v", err)
	}
	if _, err := Resolve("ab-7", []Task{a, c}); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("two keys err = %v", err)
	}
	own := Task{ID: "D", Slug: "xy-1", External: &External{Kind: "jira", Key: "XY-1"}}
	if _, err := Resolve("xy-1", []Task{own}); err != nil {
		t.Fatalf("slug equal to own key err = %v", err)
	}
}

// AC5: cycle and self are refused, clear works.
func TestCheckParent(t *testing.T) {
	tasks := []Task{{ID: "A"}, {ID: "B", Parent: "A"}, {ID: "C", Parent: "B"}}
	for _, tc := range []struct {
		id, parent string
		ok         bool
	}{
		{"C", "", true}, {"A", "A", false}, {"A", "C", false}, {"A", "B", false},
		{"C", "A", true}, {"B", "C", false}, {"C", "ghost", false}, {"D", "C", true},
	} {
		err := CheckParent(tc.id, tc.parent, tasks)
		if (err == nil) != tc.ok {
			t.Errorf("CheckParent(%s, %q) = %v, want ok=%v", tc.id, tc.parent, err, tc.ok)
		}
	}
	loop := []Task{{ID: "X", Parent: "Y"}, {ID: "Y", Parent: "X"}}
	if err := CheckParent("Z", "X", loop); err != nil {
		t.Fatalf("existing loop elsewhere: %v", err)
	}
}

package jira

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/tracker"
)

func levels(t *testing.T, a *Adapter) map[string]int {
	t.Helper()
	types, err := a.IssueTypes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]int{}
	for _, it := range types {
		if it.Level == nil {
			t.Fatalf("%s has no level", it.Name)
		}
		m[it.Name] = *it.Level
	}
	return m
}

// AC1
func TestIssueTypesLevels(t *testing.T) {
	for _, v := range []int{3, 2} {
		got := levels(t, adapter(t, newStub(t, v), nil))
		want := map[string]int{"Epic": 1, "Task": 0, "Sub-task": -1}
		if len(got) != 3 || got["Epic"] != 1 || got["Task"] != 0 || got["Sub-task"] != -1 {
			t.Errorf("v%d levels = %v, want %v", v, got, want)
		}
	}
}

func fieldsOf(t *testing.T, s *stub, n int) map[string]any {
	t.Helper()
	if len(s.created) != n+1 {
		t.Fatalf("created = %d requests, want %d", len(s.created), n+1)
	}
	return s.created[n]["fields"].(map[string]any)
}

// AC2, AC4, AC5
func TestCreateCloud(t *testing.T) {
	s := newStub(t, 3)
	a := adapter(t, s, func(c *Config) { c.AcceptanceField = "customfield_10100" })
	is, err := a.Create(ctx, tracker.NewIssue{Type: "Task", Title: "T", Body: "Hello **x**", Acceptance: []string{"a", "b"}, Labels: []string{"l1"}, ParentKey: "RIG-2"})
	if err != nil {
		t.Fatal(err)
	}
	if is.Key == "" || is.Rev == "" || !strings.HasSuffix(is.URL, "/browse/"+is.Key) {
		t.Errorf("issue = %+v", is)
	}
	f := fieldsOf(t, s, 0)
	keys := []string{}
	for k := range f {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := []string{"customfield_10100", "description", "issuetype", "labels", "parent", "project", "summary"}; !slices.Equal(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
	if f["parent"].(map[string]any)["key"] != "RIG-2" {
		t.Errorf("parent = %v", f["parent"])
	}
	if d := f["description"].(map[string]any); d["type"] != "doc" {
		t.Errorf("description = %v", d)
	}
	if f["issuetype"].(map[string]any)["name"] != "Task" {
		t.Errorf("issuetype = %v", f["issuetype"])
	}
	if b, _ := json.Marshal(f["customfield_10100"]); !strings.Contains(string(b), "bulletList") {
		t.Errorf("acceptance = %s", b)
	}
}

// AC4
func TestCreateNoAcceptanceField(t *testing.T) {
	for _, v := range []int{3, 2} {
		s := newStub(t, v)
		a := adapter(t, s, func(c *Config) { c.EpicField = "customfield_10014" })
		if _, err := a.Create(ctx, tracker.NewIssue{Type: "Task", Title: "T", Acceptance: []string{"SECRETACCEPT"}}); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(s.created[0])
		if strings.Contains(string(b), "SECRETACCEPT") {
			t.Errorf("v%d request carries acceptance: %s", v, b)
		}
		f := fieldsOf(t, s, 0)
		if _, ok := f["description"]; ok || len(f) != 3 {
			t.Errorf("v%d fields = %v", v, f)
		}
	}
}

// AC3
func TestCreateServer(t *testing.T) {
	s := newStub(t, 2)
	a := adapter(t, s, func(c *Config) { c.EpicField = "customfield_10014"; c.AcceptanceField = "customfield_10100" })
	if _, err := a.Create(ctx, tracker.NewIssue{Type: "Task", Title: "T", Body: "# H", Acceptance: []string{"a"}, ParentKey: "RIG-1"}); err != nil {
		t.Fatal(err)
	}
	f := fieldsOf(t, s, 0)
	if f["customfield_10014"] != "RIG-1" {
		t.Errorf("epic link = %v", f["customfield_10014"])
	}
	if _, ok := f["parent"]; ok {
		t.Error("epic child sent parent")
	}
	if f["description"] != "h1. H" || f["customfield_10100"] != "* a" {
		t.Errorf("description = %q acceptance = %q", f["description"], f["customfield_10100"])
	}
	if _, err := a.Create(ctx, tracker.NewIssue{Type: "Sub-task", Title: "S", ParentKey: "RIG-3"}); err != nil {
		t.Fatal(err)
	}
	f = fieldsOf(t, s, 1)
	if f["parent"].(map[string]any)["key"] != "RIG-3" {
		t.Errorf("parent = %v", f["parent"])
	}
	if _, ok := f["customfield_10014"]; ok {
		t.Error("sub-task sent epic link")
	}
}

// AC3: a Server/DC Sub-task under an epic uses fields.parent, not Epic Link.
func TestCreateServerSubtaskUnderEpic(t *testing.T) {
	s := newStub(t, 2)
	a := adapter(t, s, func(c *Config) { c.EpicField = "customfield_10014" })
	if _, err := a.Create(ctx, tracker.NewIssue{Type: "Sub-task", Title: "S", ParentKey: "RIG-1"}); err != nil {
		t.Fatal(err)
	}
	f := fieldsOf(t, s, 0)
	if f["parent"].(map[string]any)["key"] != "RIG-1" {
		t.Errorf("parent = %v", f["parent"])
	}
	if _, ok := f["customfield_10014"]; ok {
		t.Error("sub-task sent epic link")
	}
}

func TestCreateServerNoEpicField(t *testing.T) {
	s := newStub(t, 2)
	s.noEpicField = true
	a := adapter(t, s, nil)
	_, err := a.Create(ctx, tracker.NewIssue{Type: "Task", Title: "T", ParentKey: "RIG-1"})
	if err == nil || !strings.Contains(err.Error(), "tracker.jira.epicField") {
		t.Fatalf("err = %v", err)
	}
	if s.posted != 0 || len(s.created) != 0 {
		t.Error("wrote before failing")
	}
}

func TestCreateErrorsHideToken(t *testing.T) {
	s := newStub(t, 3)
	a := adapter(t, s, nil)
	_, err := a.Create(ctx, tracker.NewIssue{Type: "Nope", Title: "T"})
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("err = %v", err)
	}
	if errors.Is(err, tracker.ErrUnsupported) {
		t.Error("unexpected unsupported")
	}
}

// AC3: discovered by name when epicField is unset.
func TestCreateServerDiscoversEpicField(t *testing.T) {
	s := newStub(t, 2)
	a := adapter(t, s, nil)
	if _, err := a.Create(ctx, tracker.NewIssue{Type: "Task", Title: "T", ParentKey: "RIG-1"}); err != nil {
		t.Fatal(err)
	}
	if f := fieldsOf(t, s, 0); f["customfield_10014"] != "RIG-1" {
		t.Errorf("fields = %v", f)
	}
}

// AC2: on Cloud an epic parent goes in fields.parent, with no epic field.
func TestCreateCloudEpicParent(t *testing.T) {
	s := newStub(t, 3)
	a := adapter(t, s, nil)
	if _, err := a.Create(ctx, tracker.NewIssue{Type: "Task", Title: "T", ParentKey: "RIG-1"}); err != nil {
		t.Fatal(err)
	}
	f := fieldsOf(t, s, 0)
	if f["parent"].(map[string]any)["key"] != "RIG-1" || len(f) != 4 {
		t.Errorf("fields = %v", f)
	}
}

// AC7
func TestCreateReadBackFails(t *testing.T) {
	s := newStub(t, 3)
	s.failGet = "RIG-101"
	a := adapter(t, s, nil)
	is, err := a.Create(ctx, tracker.NewIssue{Type: "Task", Title: "T"})
	if err == nil || !strings.Contains(err.Error(), "RIG-101") {
		t.Fatalf("err = %v", err)
	}
	if is.Key != "RIG-101" || !strings.HasSuffix(is.URL, "/browse/RIG-101") {
		t.Errorf("issue = %+v", is)
	}
}

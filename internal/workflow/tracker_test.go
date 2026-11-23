package workflow

import (
	"slices"
	"strings"
	"testing"
)

// trackerBase is a workflow with a tracker key that breaks no rule.
const trackerBase = `{
  "name": "w", "version": 1, "initial": "a",
  "states": {"a": {}, "b": {}, "c": {"final": true}},
  "transitions": [{"from": "a", "to": "b"}, {"from": "b", "to": "c"}],
  "fields": {
    "acc": {"type": "list"}, "tags": {"type": "list"}, "prio": {"type": "string"},
    "files": {"type": "paths", "owns": true}, "note": {"type": "text"}
  },
  "findings": {"severities": ["hi"]},
  "tracker": {
    "intake": {"states": ["a", "b"]},
    "map": {"title": "title", "body": "description", "acc": "acceptance", "tags": "labels", "prio": "priority"},
    "report": {"status": {"b": "In Review", "c": "Done"}}
  }
}`

func TestTrackerValidate(t *testing.T) {
	base, err := Parse([]byte(trackerBase))
	if err != nil {
		t.Fatal(err)
	}
	if ps := base.Validate(); len(ps) != 0 {
		t.Fatalf("valid tracker has problems: %v", ps)
	}
	cases := []struct {
		name     string
		old, new string
		path     string
	}{
		{"intake undeclared", `"states": ["a", "b"]`, `"states": ["a", "z"]`, "tracker.intake.states[1]"},
		{"intake final", `"states": ["a", "b"]`, `"states": ["a", "c"]`, "tracker.intake.states[1]"},
		{"report undeclared", `"b": "In Review"`, `"z": "In Review"`, "tracker.report.status.z"},
		{"report empty", `"b": "In Review"`, `"b": ""`, "tracker.report.status.b"},
		{"map key undeclared", `"prio": "priority"`, `"nope": "priority"`, "tracker.map.nope"},
		{"map owns field", `"prio": "priority"`, `"files": "priority"`, "tracker.map.files"},
		{"bad attribute", `"prio": "priority"`, `"prio": "colour"`, "tracker.map.prio"},
		{"labels from text field", `"prio": "priority"`, `"note": "labels"`, "tracker.map.note"},
		{"acceptance from body", `"body": "description"`, `"body": "acceptance"`, "tracker.map.body"},
		{"labels from title", `"title": "title"`, `"title": "labels"`, "tracker.map.title"},
		{"duplicate attribute", `"prio": "priority"`, `"note": "title"`, "tracker.map.title"},
	}
	for _, c := range cases {
		src := strings.Replace(trackerBase, c.old, c.new, 1)
		if src == trackerBase {
			t.Fatalf("%s: replacement did not apply", c.name)
		}
		w, err := Parse([]byte(src))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		found := false
		for _, p := range w.Validate() {
			found = found || p.Path == c.path
		}
		if !found {
			t.Errorf("%s: no problem at %s; got %v", c.name, c.path, w.Validate())
		}
	}
	// Passing counterparts of the rules above.
	for name, pair := range map[string][2]string{
		"labels from list":  {`"prio": "priority"`, `"prio": "priority", "note": "type"`},
		"intake just a":     {`"states": ["a", "b"]`, `"states": ["a"]`},
		"mapped list label": {`"tags": "labels"`, `"tags": "acceptance", "acc": "labels"`},
		"no map at all":     {`"map": {"title": "title", "body": "description", "acc": "acceptance", "tags": "labels", "prio": "priority"},`, ``},
		"empty report":      {`"report": {"status": {"b": "In Review", "c": "Done"}}`, `"report": {}`},
		"title as assignee": {`"title": "title"`, `"title": "assignee"`},
	} {
		w, err := Parse([]byte(strings.Replace(trackerBase, pair[0], pair[1], 1)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if ps := w.Validate(); len(ps) != 0 {
			t.Errorf("%s: problems %v", name, ps)
		}
	}
}

func TestTrackerUnknownKeyRefused(t *testing.T) {
	for _, src := range []string{
		`{"tracker": {"bogus": 1}}`,
		`{"tracker": {"intake": {"state": []}}}`,
		`{"tracker": {"report": {"stat": {}}}}`,
		`{"tracker": {"report": {"status": {"a": 3}}}}`,
	} {
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("Parse accepted %s", src)
		}
	}
}

func TestTrackerAccessors(t *testing.T) {
	w, err := Parse([]byte(trackerBase))
	if err != nil {
		t.Fatal(err)
	}
	if got := w.IntakeStates(); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("IntakeStates = %v", got)
	}
	if got := w.OwnedFields(); !slices.Equal(got, []string{"acc", "body", "prio", "tags", "title"}) {
		t.Errorf("OwnedFields = %v", got)
	}
	if a, ok := w.AttributeFor("acc"); a != "acceptance" || !ok {
		t.Errorf("AttributeFor(acc) = %q %v", a, ok)
	}
	if a, ok := w.AttributeFor("note"); a != "" || ok {
		t.Errorf("AttributeFor(note) = %q %v", a, ok)
	}
	if s, ok := w.ReportStatus("c"); s != "Done" || !ok {
		t.Errorf("ReportStatus(c) = %q %v", s, ok)
	}
	if s, ok := w.ReportStatus("a"); s != "" || ok {
		t.Errorf("ReportStatus(a) = %q %v", s, ok)
	}
}

func TestTrackerDefaults(t *testing.T) {
	for name, src := range map[string]string{
		"no tracker": `{"initial": "a"}`,
		"no intake":  `{"initial": "a", "tracker": {"report": {}}}`,
		"empty list": `{"initial": "a", "tracker": {"intake": {"states": []}}}`,
		"no map":     `{"initial": "a", "tracker": {"intake": {}}}`,
	} {
		w, err := Parse([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		if got := w.IntakeStates(); !slices.Equal(got, []string{"a"}) {
			t.Errorf("%s: IntakeStates = %v", name, got)
		}
		if got := w.OwnedFields(); !slices.Equal(got, []string{"body", "title"}) {
			t.Errorf("%s: OwnedFields = %v", name, got)
		}
		if a, ok := w.AttributeFor("body"); a != "description" || !ok {
			t.Errorf("%s: AttributeFor(body) = %q %v", name, a, ok)
		}
		if a, ok := w.AttributeFor("title"); a != "title" || !ok {
			t.Errorf("%s: AttributeFor(title) = %q %v", name, a, ok)
		}
		if s, ok := w.ReportStatus("a"); s != "" || ok {
			t.Errorf("%s: ReportStatus = %q %v", name, s, ok)
		}
	}
}

func TestValidateWithoutTrackerUnchanged(t *testing.T) {
	if ps := fixture(t).Validate(); len(ps) != 0 {
		t.Fatalf("fixture problems: %v", ps)
	}
}

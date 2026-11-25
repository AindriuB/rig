//go:build !windows

package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/tracker"
	"github.com/AindriuB/rig/internal/tracker/trackertest"
)

var ctx = context.Background()

func newTracker(t *testing.T, hierarchy bool) (*Tracker, string) {
	t.Helper()
	dir := fakeEnv(t, hierarchy)
	tr, err := New(Options{Project: fakeProject, Host: "gitlab.example"})
	if err != nil {
		t.Fatal(err)
	}
	return tr, dir
}

// AC1: the contract passes with the work-item hierarchy on and off.
func TestContract(t *testing.T) {
	for _, hier := range []bool{true, false} {
		name := "hierarchy off"
		if hier {
			name = "hierarchy on"
		}
		t.Run(name, func(t *testing.T) {
			trackertest.Run(t, func(t *testing.T) (tracker.Tracker, trackertest.Seed) {
				tr, _ := newTracker(t, hier)
				return tr, trackertest.Seed{Key: "3", Status: "opened", NextStatus: "closed",
					ParentKey: "1", ChildKey: "2", Label: "rig", MissingKey: "99"}
			})
			tr, _ := newTracker(t, hier)
			caps, err := tr.Capabilities(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if caps.Parent != hier || caps.Children != hier {
				t.Errorf("caps = %+v, want Parent and Children %v", caps, hier)
			}
			if _, err := tr.Children(ctx, "1"); errors.Is(err, tracker.ErrUnsupported) == hier {
				t.Errorf("Children err = %v with hierarchy %v", err, hier)
			}
		})
	}
}

func TestIssueFields(t *testing.T) {
	tr, _ := newTracker(t, true)
	is, err := tr.Issue(ctx, "2")
	if err != nil {
		t.Fatal(err)
	}
	if is.Status != "opened" || is.Type != "issue" || is.Rev == "" || is.Parent == nil || is.Parent.Key != "1" {
		t.Errorf("issue = %+v", is)
	}
	c, err := tr.Comment(ctx, "2", tracker.Comment{Body: "hi"})
	if err != nil || !strings.HasPrefix(c.URL, issueURL(2)+"#note_") {
		t.Errorf("comment = %+v, %v", c, err)
	}
}

func TestSetStatusNamesTheStates(t *testing.T) {
	tr, _ := newTracker(t, false)
	_, err := tr.SetStatus(ctx, "1", "in progress")
	if !errors.Is(err, tracker.ErrNoTransition) || !strings.Contains(err.Error(), "opened") || !strings.Contains(err.Error(), "closed") {
		t.Errorf("err = %v", err)
	}
	res, err := tr.SetStatus(ctx, "1", "open")
	if err != nil || res.Changed {
		t.Errorf("open on an open issue = %+v, %v", res, err)
	}
}

func TestAuthFailure(t *testing.T) {
	tr, dir := newTracker(t, false)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"noauth":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Capabilities(ctx); !errors.Is(err, tracker.ErrAuth) {
		t.Errorf("Capabilities = %v, want ErrAuth", err)
	}
}

// AC2: hostile text reaches glab only as one -f argv element or stdin JSON.
func TestHostileTextIsOneArgument(t *testing.T) {
	hostile := []string{"a'b\"c", "@/etc/passwd", "$(x)", "line1\nline2", "&per_page=1", "../../x?y=1#z"}
	for _, h := range hostile {
		tr, dir := newTracker(t, true)
		if _, err := tr.Search(ctx, tracker.Query{Text: h, Labels: []string{h, "b"}}); err != nil {
			t.Fatalf("Search(%q): %v", h, err)
		}
		if _, err := tr.Comment(ctx, "1", tracker.Comment{Body: h}); err != nil {
			t.Fatalf("Comment(%q): %v", h, err)
		}
		if _, err := tr.SetStatus(ctx, "1", h); err == nil {
			t.Fatalf("SetStatus(%q) succeeded", h)
		}
		seen := 0
		for _, c := range readCalls(t, dir) {
			for i, a := range c.Args {
				switch {
				case strings.Contains(a, h) && !strings.Contains(h, "\n"):
					// Only as the value of a -f field.
					if i == 0 || c.Args[i-1] != "-f" || !(strings.HasPrefix(a, "search=") || strings.HasPrefix(a, "labels=") || strings.HasPrefix(a, "body=")) {
						t.Errorf("hostile %q in argv %q at %d (%q)", h, a, i, c.Args)
					}
					seen++
				case strings.Contains(h, "\n") && strings.Contains(a, "line2"):
					if c.Args[i-1] != "-f" {
						t.Errorf("hostile %q in argv %q", h, a)
					}
					seen++
				}
				if strings.HasPrefix(a, "-F") || a == "--field" || a == "--raw-field" {
					t.Errorf("glab call uses %q: %q", a, c.Args)
				}
			}
			if c.Stdin != "" {
				var v map[string]any
				if err := json.Unmarshal([]byte(c.Stdin), &v); err != nil {
					t.Errorf("stdin is not JSON: %q", c.Stdin)
				}
				if strings.Contains(c.Stdin, "per_page=1") {
					t.Errorf("hostile text in a GraphQL body: %q", c.Stdin)
				}
			}
		}
		if seen == 0 {
			t.Errorf("hostile %q never reached glab", h)
		}
	}
}

// AC3: malformed keys never start glab.
func TestMalformedKeysNeverStartGlab(t *testing.T) {
	tr, dir := newTracker(t, true)
	for _, k := range []string{"g/p#1", "01", "0", "1x", "-1", "+1", "1 ", "99999999999999999999", "٣"} {
		if _, err := tr.Issue(ctx, k); err == nil {
			t.Errorf("Issue(%q) succeeded", k)
		}
		if _, err := tr.Children(ctx, k); err == nil {
			t.Errorf("Children(%q) succeeded", k)
		}
		if _, err := tr.Comment(ctx, k, tracker.Comment{Body: "x"}); err == nil {
			t.Errorf("Comment(%q) succeeded", k)
		}
		if _, err := tr.SetStatus(ctx, k, "closed"); err == nil {
			t.Errorf("SetStatus(%q) succeeded", k)
		}
	}
	if calls := readCalls(t, dir); len(calls) != 0 {
		t.Errorf("glab was started: %v", calls)
	}
}

// AC4: RIG_JIRA_TOKEN in the parent environment never reaches glab.
func TestTokenNotInherited(t *testing.T) {
	t.Setenv("RIG_JIRA_TOKEN", "secret")
	tr, dir := newTracker(t, true)
	if _, err := tr.Capabilities(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Issue(ctx, "1"); err != nil {
		t.Fatal(err)
	}
	calls := readCalls(t, dir)
	if len(calls) == 0 {
		t.Fatal("no calls")
	}
	for _, c := range calls {
		if c.TokenSeen {
			t.Errorf("glab saw RIG_JIRA_TOKEN: %q", c.Args)
		}
	}
}

// Real glab exits 1 on a GraphQL errors body; that means "no hierarchy".
func TestGraphQLErrorExitMeansNoHierarchy(t *testing.T) {
	tr, dir := newTracker(t, true)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"gqlfail":true,"issues":[{"iid":1,"web_url":"https://gitlab.example/grp/sub/proj/-/issues/1","title":"t","state":"opened","issue_type":"issue"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	caps, err := tr.Capabilities(ctx)
	if err != nil || caps.Parent || caps.Children {
		t.Errorf("Capabilities = %+v, %v; want no hierarchy and no error", caps, err)
	}
	if _, err := tr.Issue(ctx, "1"); err != nil {
		t.Errorf("Issue = %v", err)
	}
}

func TestWorkItemRefBelongsToProject(t *testing.T) {
	cases := []struct {
		base, url string
		ok        bool
	}{
		{"https://h/grp/sub/proj", "https://h/grp/sub/proj/-/issues/7", true},
		{"https://h/grp/sub/proj", "https://h/GRP/sub/proj/-/work_items/7", true},
		{"https://h/grp/sub/proj", "https://h/grp/other/-/issues/7", false},
		{"https://h/grp/sub/proj", "https://h/grp/sub/proj/-/issues/7/extra", false},
		{"https://h/grp/sub/proj", "https://h/groups/grp/-/epics/7", false},
		{"https://h/acme/groups/app", "https://h/acme/groups/app/-/issues/7", true},
		// A relative URL root is part of the learned base.
		{"https://h/gitlab/grp/proj", "https://h/gitlab/grp/proj/-/issues/7", true},
		// Another project whose path merely ends with this project's path.
		{"https://h/grp/proj", "https://h/other/grp/proj/-/issues/7", false},
		{"https://h/gitlab/grp/proj", "https://h/other/gitlab/grp/proj/-/issues/7", false},
		{"https://h/grp/proj", "https://evil/grp/proj/-/issues/7", false},
		{"https://h/grp/proj", "http://h/grp/proj/-/issues/7", false},
	}
	for _, c := range cases {
		tr := &Tracker{base: c.base}
		_, ok := tr.workItem(workItemRef{IID: "7", WebURL: c.url})
		if ok != c.ok {
			t.Errorf("%s %s: ok = %v, want %v", c.base, c.url, ok, c.ok)
		}
	}
}

// 36-1: GitLab under a relative URL root keeps its parent and children, and a
// foreign project whose path ends with this one's is still dropped.
func TestFamilyUnderRelativeURLRoot(t *testing.T) {
	tr, dir := newTracker(t, true)
	root := "https://example.com/gitlab/" + fakeProject
	st := fakeState{Hierarchy: true, BaseURL: root, Issues: []fakeIssue{
		{IID: 1, WebURL: root + "/-/issues/1", Title: "p", State: "opened", IssueType: "issue"},
		{IID: 2, WebURL: root + "/-/issues/2", Title: "kept", State: "opened", IssueType: "issue", Parent: 1},
		{IID: 3, WebURL: "https://example.com/other/gitlab/" + fakeProject + "/-/issues/3", Title: "foreign", State: "opened", IssueType: "issue", Parent: 1},
	}}
	b, _ := json.Marshal(st)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	kids, err := tr.Children(context.Background(), "1")
	if err != nil || len(kids) != 1 || kids[0].Key != "2" {
		t.Errorf("Children = %+v, %v; want only issue 2", kids, err)
	}
	iss, err := tr.Issue(context.Background(), "2")
	if err != nil || iss.Parent == nil || iss.Parent.Key != "1" {
		t.Errorf("Issue(2).Parent = %+v, %v; want 1", iss.Parent, err)
	}
}

func setState(t *testing.T, dir string, mutate func(*fakeState)) {
	t.Helper()
	var st fakeState
	b, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	_ = json.Unmarshal(b, &st)
	mutate(&st)
	b, _ = json.Marshal(st)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func level(l *int) int {
	if l == nil {
		return -1
	}
	return *l
}

// AC1: levels, and Epic only where the group reports it.
func TestIssueTypesLevels(t *testing.T) {
	tr, dir := newTracker(t, true)
	setState(t, dir, func(s *fakeState) { s.Epics = true })
	types, err := tr.IssueTypes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, it := range types {
		got[it.Name] = level(it.Level)
	}
	want := map[string]int{"Epic": 2, "Issue": 1, "Task": 0, "Incident": -1}
	if len(got) != len(want) {
		t.Errorf("types = %v, want %v", got, want)
	}
	for k, v := range want {
		if g, ok := got[k]; !ok || g != v {
			t.Errorf("type %s level = %v (present %v), want %d", k, g, ok, v)
		}
	}
	for _, mode := range []func(*fakeState){
		func(s *fakeState) { s.Epics = false },                 // tier has no epics
		func(s *fakeState) { s.Group = false; s.Epics = true }, // not a group
	} {
		setState(t, dir, mode)
		types, err := tr.IssueTypes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range types {
			if it.Name == "Epic" {
				t.Errorf("Epic offered when unavailable")
			}
		}
	}
}

func lastCreate(t *testing.T, dir string) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, c := range readCalls(t, dir) {
		if strings.Contains(c.Stdin, "RigCreate") {
			var req struct {
				Variables struct{ Input map[string]any }
			}
			if err := json.Unmarshal([]byte(c.Stdin), &req); err != nil {
				t.Fatal(err)
			}
			found = append(found, req.Variables.Input)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d create writes, want 1", len(found))
	}
	return found[0]
}

// AC2, AC4, AC5: one write carrying only the agreed fields.
func TestCreateWithParent(t *testing.T) {
	tr, dir := newTracker(t, true)
	made, err := tr.Create(ctx, tracker.NewIssue{Type: "task", Title: "t", Body: "body", Acceptance: []string{"a"}, Labels: []string{"rig"}, ParentKey: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if made.Key == "" || made.URL == "" || made.Rev == "" {
		t.Errorf("made = %+v", made)
	}
	in := lastCreate(t, dir)
	for k := range in {
		switch k {
		case "namespacePath", "workItemTypeId", "title", "descriptionWidget", "labelsWidget", "hierarchyWidget":
		default:
			t.Errorf("request carries unagreed field %q", k)
		}
	}
	if in["workItemTypeId"] != "gid://gitlab/WorkItems::Type/5" || in["title"] != "t" {
		t.Errorf("input = %v", in)
	}
	if hw, _ := in["hierarchyWidget"].(map[string]any); hw["parentId"] != "gid://gitlab/WorkItem/1" || len(hw) != 1 {
		t.Errorf("hierarchyWidget = %v", in["hierarchyWidget"])
	}
	if dw, _ := in["descriptionWidget"].(map[string]any); !strings.Contains(dw["description"].(string), "body") || !strings.Contains(dw["description"].(string), "- [ ] a") {
		t.Errorf("descriptionWidget = %v", in["descriptionWidget"])
	}
	got, err := tr.Issue(ctx, made.Key)
	if err != nil || got.Parent == nil || got.Parent.Key != "1" {
		t.Errorf("created issue = %+v, %v", got, err)
	}
}

// AC3: an unavailable Epic is ErrUnsupported and nothing is written.
func TestCreateUnavailableEpic(t *testing.T) {
	tr, dir := newTracker(t, true)
	_, err := tr.Create(ctx, tracker.NewIssue{Type: "Epic", Title: "e"})
	if !errors.Is(err, tracker.ErrUnsupported) || !strings.Contains(err.Error(), "Epic") {
		t.Errorf("err = %v", err)
	}
	for _, c := range readCalls(t, dir) {
		if strings.Contains(c.Stdin, "RigCreate") {
			t.Errorf("write made: %s", c.Stdin)
		}
	}
	setState(t, dir, func(s *fakeState) { s.Epics = true })
	if _, err := tr.Create(ctx, tracker.NewIssue{Type: "Epic", Title: "e"}); err != nil {
		t.Errorf("Epic with a group: %v", err)
	}
}

func TestCreateUnknownLabelWritesNothing(t *testing.T) {
	tr, dir := newTracker(t, true)
	if _, err := tr.Create(ctx, tracker.NewIssue{Type: "Task", Title: "t", Labels: []string{"no-such-label"}}); !errors.Is(err, tracker.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
	for _, c := range readCalls(t, dir) {
		if strings.Contains(c.Stdin, "RigCreate") {
			t.Errorf("write made")
		}
	}
}

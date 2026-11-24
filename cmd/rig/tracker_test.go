package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/secret"
	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/tracker"
	"github.com/AindriuB/rig/internal/tracker/fake"
	jiratracker "github.com/AindriuB/rig/internal/tracker/jira"
)

// trkEnv is a loginEnv whose tracker adapter is a stand-in. It records the
// specs the app resolved.
type trkEnv struct {
	*loginEnv
	tr    tracker.Tracker
	specs []trackerSpec
}

func newTrkEnv(t *testing.T, cfg map[string]any) *trkEnv {
	t.Helper()
	e := &trkEnv{loginEnv: newLoginEnv(t)}
	t.Setenv("HOME", e.home)
	t.Setenv("USERPROFILE", e.home)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	if cfg != nil {
		e.globalConfig(map[string]any{"tracker": cfg})
	}
	e.a.now = tick()
	e.a.tracker = func(_ context.Context, spec trackerSpec) (tracker.Tracker, error) {
		e.specs = append(e.specs, spec)
		return e.tr, nil
	}
	return e
}

func (e *trkEnv) json(v any, args ...string) int {
	e.t.Helper()
	code := e.run(append(args, "--json")...)
	if v != nil && e.out.Len() > 0 {
		if err := json.Unmarshal(e.out.Bytes(), v); err != nil {
			e.t.Fatalf("output is not JSON: %v\n%s", err, e.out.String())
		}
	}
	return code
}

func (e *trkEnv) st() store.Store {
	e.t.Helper()
	st, err := e.a.openStore(ctx, e.repo)
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

func ghCfg() map[string]any { return map[string]any{"kind": "github", "project": "acme/rig"} }

// counting counts every call that reaches the tracker.
type counting struct {
	tracker.Tracker
	n int
}

func (c *counting) Capabilities(ctx context.Context) (tracker.Caps, error) {
	c.n++
	return c.Tracker.Capabilities(ctx)
}
func (c *counting) Issue(ctx context.Context, k string) (tracker.Issue, error) {
	c.n++
	return c.Tracker.Issue(ctx, k)
}
func (c *counting) Search(ctx context.Context, q tracker.Query) ([]tracker.IssueRef, error) {
	c.n++
	return c.Tracker.Search(ctx, q)
}
func (c *counting) Children(ctx context.Context, k string) ([]tracker.IssueRef, error) {
	c.n++
	return c.Tracker.Children(ctx, k)
}

var allCaps = tracker.Caps{Parent: true, Children: true, Types: true, Search: true}

func issue(key, title string) tracker.Issue {
	return tracker.Issue{Key: key, URL: "https://t.example/" + key, Title: title, Description: "about " + title, Type: "Task", Status: "Open"}
}

// AC1: a local or unset tracker is exit 2 for every subcommand, and no
// adapter is built.
func TestTrackerLocalIsExit2(t *testing.T) {
	for _, cfg := range []map[string]any{nil, {"kind": "local"}} {
		e := newTrkEnv(t, cfg)
		e.tr = fake.New(allCaps, nil, nil)
		for _, args := range [][]string{
			{"tracker", "pull", "X-1"}, {"tracker", "refresh", "--all"}, {"tracker", "show", "X-1"}, {"tracker", "status"},
		} {
			if code := e.run(args...); code != 2 || !strings.Contains(e.err.String(), "no tracker") {
				t.Fatalf("%v: exit %d, stderr %q", args, code, e.err.String())
			}
		}
		if len(e.specs) != 0 {
			t.Fatal("an adapter was built for a local tracker")
		}
	}
}

func TestTrackerExecNeedsTrustedCommand(t *testing.T) {
	e := newTrkEnv(t, map[string]any{"kind": "exec"})
	if code := e.run("tracker", "show", "X-1"); code != 2 || !strings.Contains(e.err.String(), "tracker.command") {
		t.Fatalf("exit %d, stderr %q", code, e.err.String())
	}
	prog := filepath.Join(t.TempDir(), "prog")
	e = newTrkEnv(t, map[string]any{"kind": "exec", "command": []string{prog, "--x"}})
	spec, err := e.a.trackerSpec(ctx, e.repo)
	if err != nil || strings.Join(spec.Exec, " ") != prog+" --x" || spec.Dir == "" {
		t.Fatalf("spec %+v, %v", spec, err)
	}
}

// 38-1 (round 2): status for an exec tracker with no trusted command says
// trusted:false, matching its login error.
func TestTrackerStatusUntrustedExec(t *testing.T) {
	e := newTrkEnv(t, map[string]any{"kind": "exec"})
	var out struct {
		Trusted bool
		Login   struct{ OK bool }
	}
	if code := e.json(&out, "tracker", "status"); code != 0 || out.Trusted || out.Login.OK {
		t.Fatalf("exit %d, %+v\n%s", code, out, e.out.String())
	}
}

func TestTrackerJiraLogin(t *testing.T) {
	cfg := map[string]any{"kind": "jira", "url": "https://acme.example.com", "project": "RIG", "jira": map[string]any{"api": "v2", "epicField": "customfield_10014", "acceptanceField": "customfield_10020"}}
	e := newTrkEnv(t, cfg)
	if _, err := e.a.trackerSpec(ctx, e.repo); err == nil || !strings.Contains(err.Error(), "rig login jira") {
		t.Fatalf("no login: %v", err)
	}
	if code := e.run("tracker", "show", "RIG-1"); code != 2 || !strings.Contains(e.err.String(), "rig login jira") {
		t.Fatalf("exit %d, stderr %q", code, e.err.String())
	}
	e.stores[storeKeychain].Set(ctx, "jira:acme.example.com", secret.Secret{User: "me@example.com", Token: "stored"})
	spec, err := e.a.trackerSpec(ctx, e.repo)
	if err != nil || spec.Jira.User != "me@example.com" || spec.Jira.Token != "stored" || spec.Jira.API != "v2" ||
		spec.Jira.Project != "RIG" || spec.Jira.EpicField != "customfield_10014" || spec.Jira.AcceptanceField != "customfield_10020" ||
		spec.Jira.Site != "https://acme.example.com" || spec.Link != (trackerLink{"jira", "acme.example.com", ""}) {
		t.Fatalf("spec %+v, %v", spec, err)
	}
	// RIG_JIRA_TOKEN beats the stored token.
	e.envVars[jiraTokenEnv] = "from-env"
	if spec, err = e.a.trackerSpec(ctx, e.repo); err != nil || spec.Jira.Token != "from-env" {
		t.Fatalf("env: %+v, %v", spec, err)
	}
	// A secret with no user is a PAT.
	delete(e.envVars, jiraTokenEnv)
	e.stores[storeKeychain].Set(ctx, "jira:acme.example.com", secret.Secret{Token: "pat"})
	if spec, err = e.a.trackerSpec(ctx, e.repo); err != nil || spec.Jira.User != "" || spec.Jira.Token != "pat" {
		t.Fatalf("pat: %+v, %v", spec, err)
	}
}

func TestTrackerRepoFromConfigAndRemote(t *testing.T) {
	e := newTrkEnv(t, ghCfg())
	spec, err := e.a.trackerSpec(ctx, e.repo)
	if err != nil || spec.GitHub.Repo != "acme/rig" || spec.GitHub.Host != "" || filepath.Clean(spec.GitHub.Dir) != filepath.Clean(e.repo) ||
		spec.Link != (trackerLink{"github", "github.com", "acme/rig"}) {
		t.Fatalf("project from config: %+v, %v", spec, err)
	}
	e = newTrkEnv(t, map[string]any{"kind": "github"})
	if spec, err = e.a.trackerSpec(ctx, e.repo); err != nil || spec.GitHub.Repo != "acme/rig" {
		t.Fatalf("project from remote: %+v, %v", spec, err)
	}
	e = newTrkEnv(t, nil)
	e.globalConfig(map[string]any{"forge": map[string]any{"host": "gitlab.example.com"}, "tracker": map[string]any{"kind": "gitlab"}})
	gitc(t, e.repo, "remote", "set-url", "origin", "git@gitlab.example.com:grp/sub/proj.git")
	if spec, err = e.a.trackerSpec(ctx, e.repo); err != nil || spec.GitLab.Project != "grp/sub/proj" || spec.GitLab.Host != "gitlab.example.com" ||
		spec.Link != (trackerLink{"gitlab", "gitlab.example.com", "grp/sub/proj"}) {
		t.Fatalf("gitlab from remote: %+v, %v", spec, err)
	}
	e = newTrkEnv(t, map[string]any{"kind": "github", "url": "https://ghe.example.com", "project": "o/r"})
	if spec, err = e.a.trackerSpec(ctx, e.repo); err != nil || spec.GitHub.Host != "ghe.example.com" {
		t.Fatalf("host from tracker.url: %+v, %v", spec, err)
	}
}

// pulled decodes `rig tracker pull --json`.
type pulled struct {
	Items []struct {
		Task struct {
			ID, Short, Slug, Title, Body, State, Branch string
			Fields                                      map[string]any
			Parent                                      string
			External                                    *struct {
				Kind, Host, Key, ParentKey string
				Owned, Drift               []string
			}
		}
		Key      string
		Existing bool
		Parent   string
		Warnings []string
	}
	Skipped int
}

// snapshotOf returns the snapshot of the task's link.
func (e *trkEnv) snapshotOf(ref string) map[string]any {
	e.t.Helper()
	task, err := e.st().Task(ctx, ref)
	if err != nil || task.External == nil {
		e.t.Fatalf("task %s: %v", ref, err)
	}
	return task.External.Snapshot
}

// AC2: pull creates a linked task with the mapped fields and a snapshot; a
// second pull returns it as existing.
func TestTrackerPullCreatesAndRepeats(t *testing.T) {
	e := newTrkEnv(t, ghCfg())
	e.tr = fake.New(allCaps, []tracker.Issue{issue("12", "Fix the Login Page!")}, nil)
	var out pulled
	if code := e.json(&out, "tracker", "pull", "12"); code != 0 {
		t.Fatalf("exit %d: %s", code, e.err.String())
	}
	if len(out.Items) != 1 {
		t.Fatalf("%+v", out)
	}
	it := out.Items[0]
	x := it.Task.External
	if it.Key != "12" || it.Existing || it.Task.Title != "Fix the Login Page!" || it.Task.Body != "about Fix the Login Page!" ||
		it.Task.Slug != "12-fix-the-login-page" || x == nil || x.Kind != "github" || x.Host != "github.com" || x.Key != "12" ||
		strings.Join(x.Owned, ",") != "body,title" || len(x.Drift) != 0 || out.Skipped != 0 {
		t.Fatalf("%+v", out)
	}
	if ts := e.snapshotOf(it.Task.Short); ts["title"] != "Fix the Login Page!" || ts["body"] != "about Fix the Login Page!" {
		t.Fatalf("snapshot %v", ts)
	}
	e.err.Reset()
	var again pulled
	if code := e.json(&again, "tracker", "pull", "12"); code != 0 || !again.Items[0].Existing || again.Items[0].Task.ID != it.Task.ID {
		t.Fatalf("second pull: exit %d %+v", code, again)
	}
	if !strings.Contains(e.err.String(), "already task") {
		t.Fatalf("stderr %q", e.err.String())
	}
	if all, _ := e.st().Tasks(ctx, store.TaskFilter{}); len(all) != 1 {
		t.Fatalf("%d tasks", len(all))
	}
}

// scripted is a tracker that returns exactly what a test hands it, keys
// included, without the fake's key check.
type scripted struct {
	tracker.Tracker
	issues   map[string]tracker.Issue
	search   []tracker.IssueRef
	children map[string][]tracker.IssueRef
	errs     map[string]error // returned by Issue for a key
}

func (s *scripted) Capabilities(context.Context) (tracker.Caps, error) { return allCaps, nil }
func (s *scripted) Issue(_ context.Context, k string) (tracker.Issue, error) {
	if err := s.errs[k]; err != nil {
		return tracker.Issue{}, err
	}
	if is, ok := s.issues[k]; ok {
		return is, nil
	}
	return tracker.Issue{}, tracker.ErrNotFound
}
func (s *scripted) Search(context.Context, tracker.Query) ([]tracker.IssueRef, error) {
	return s.search, nil
}
func (s *scripted) Children(_ context.Context, k string) ([]tracker.IssueRef, error) {
	return s.children[k], nil
}

// AC3: a malformed key from the tracker, in an Issue, a Parent, a search
// result or a children list, never reaches a slug, branch or store event.
func TestTrackerPullRejectsMalformedKeys(t *testing.T) {
	for _, bad := range []string{"../x", "a/b#1", ""} {
		for _, where := range []string{"issue", "parent", "search", "children"} {
			e := newTrkEnv(t, ghCfg())
			good := issue("11", "Good one")
			s := &scripted{issues: map[string]tracker.Issue{"10": issue("10", "Root"), "11": good}}
			args := []string{"tracker", "pull", "10"}
			switch where {
			case "issue":
				is := issue(bad, "Bad key")
				s.issues["10"] = is
			case "parent":
				is := issue("10", "Bad parent")
				is.Parent = &tracker.IssueRef{Key: bad}
				s.issues["10"] = is
			case "search":
				s.search = []tracker.IssueRef{{Key: bad}, {Key: "11"}}
				args = []string{"tracker", "pull", "--query", "q"}
			case "children":
				s.children = map[string][]tracker.IssueRef{"10": {{Key: bad}, {Key: "11"}}}
				args = append(args, "--children")
			}
			e.tr = s
			var out pulled
			if code := e.json(&out, args...); code != 2 {
				t.Fatalf("%s %q: exit %d, want 2", where, bad, code)
			}
			if out.Skipped < 1 {
				t.Fatalf("%s %q: skipped %d", where, bad, out.Skipped)
			}
			all, _ := e.st().Tasks(ctx, store.TaskFilter{})
			for _, tk := range all {
				if strings.ContainsAny(tk.Slug+tk.External.Key, "/#") || strings.Contains(tk.Slug+tk.Branch, "..") || tk.External.Key == "" {
					t.Fatalf("%s %q: task %+v carries the bad key", where, bad, tk)
				}
				evs, _ := e.st().Events(ctx, tk.ID)
				for _, ev := range evs {
					if bad != "" && strings.Contains(string(ev.Data), bad) {
						t.Fatalf("%s %q: event %s carries the bad key", where, bad, ev.Data)
					}
				}
			}
			if where == "issue" || where == "parent" {
				if len(all) != 0 {
					t.Fatalf("%s %q: %d tasks created", where, bad, len(all))
				}
			} else if len(all) < 1 || (where == "children" && len(all) != 2) {
				t.Fatalf("%s %q: %d tasks", where, bad, len(all))
			}
		}
	}
}

// AC4: a state outside the intake states is exit 1 before any tracker call.
func TestTrackerPullBadStateIsExit1(t *testing.T) {
	e := newTrkEnv(t, ghCfg())
	c := &counting{Tracker: fake.New(allCaps, []tracker.Issue{issue("12", "T")}, nil)}
	e.tr = c
	for _, args := range [][]string{
		{"tracker", "pull", "12", "--state", "nowhere"}, {"tracker", "pull", "--query", "q", "--state", "done"},
	} {
		if code := e.run(args...); code != 1 || !strings.Contains(e.err.String(), "intake state") {
			t.Fatalf("%v: exit %d, stderr %q", args, code, e.err.String())
		}
	}
	if c.n != 0 {
		t.Fatalf("%d tracker calls", c.n)
	}
	if all, _ := e.st().Tasks(ctx, store.TaskFilter{}); len(all) != 0 {
		t.Fatal("a task was created")
	}
}

func chain() []tracker.Issue {
	a, b, c := issue("1", "Top"), issue("2", "Middle"), issue("3", "Bottom")
	b.Parent = &tracker.IssueRef{Key: "1"}
	c.Parent = &tracker.IssueRef{Key: "2"}
	return []tracker.Issue{a, b, c}
}

// AC5: --children walks breadth first to the depth; an unsupported tracker is
// a warning; a parent in the store is linked, otherwise its key is kept.
func TestTrackerPullChildren(t *testing.T) {
	unsupported := tracker.Caps{Parent: true}
	e := newTrkEnv(t, ghCfg())
	e.tr = fake.New(unsupported, chain(), nil)
	var out pulled
	if code := e.json(&out, "tracker", "pull", "1", "--children"); code != 0 || len(out.Items) != 1 ||
		len(out.Items[0].Warnings) != 1 || !strings.Contains(out.Items[0].Warnings[0], "children") {
		t.Fatalf("unsupported: exit %d %+v", code, out)
	}

	e = newTrkEnv(t, ghCfg())
	e.tr = fake.New(allCaps, chain(), nil)
	out = pulled{}
	if code := e.json(&out, "tracker", "pull", "1", "--children"); code != 0 || len(out.Items) != 2 || out.Items[1].Key != "2" {
		t.Fatalf("depth 1: exit %d %+v", code, out)
	}
	if out.Items[1].Parent != out.Items[0].Task.Short || out.Items[1].Task.Parent != out.Items[0].Task.ID {
		t.Fatalf("child not linked to its parent task: %+v", out.Items[1])
	}
	out = pulled{}
	if code := e.json(&out, "tracker", "pull", "1", "--children=2"); code != 0 || len(out.Items) != 3 || out.Items[2].Key != "3" ||
		!out.Items[0].Existing || !out.Items[1].Existing || out.Items[2].Existing || out.Items[2].Parent != out.Items[1].Task.Short {
		t.Fatalf("depth 2: exit %d %+v", code, out)
	}

	// A parent with no task keeps its key.
	e = newTrkEnv(t, ghCfg())
	e.tr = fake.New(allCaps, chain(), nil)
	out = pulled{}
	if code := e.json(&out, "tracker", "pull", "3"); code != 0 || out.Items[0].Parent != "" {
		t.Fatalf("exit %d %+v", code, out)
	}
	tk, _ := e.st().Task(ctx, out.Items[0].Task.Short)
	if tk.Parent != "" || tk.External.ParentKey != "2" {
		t.Fatalf("parent %q, parentKey %q", tk.Parent, tk.External.ParentKey)
	}
}

func TestTrackerPullSources(t *testing.T) {
	e := newTrkEnv(t, ghCfg())
	a, b := issue("1", "A"), issue("2", "B")
	a.Labels = []string{"rig"}
	e.tr = fake.New(allCaps, []tracker.Issue{a, b}, nil)
	for _, args := range [][]string{
		{"tracker", "pull"}, {"tracker", "pull", "1", "--label", "rig"}, {"tracker", "pull", "--query", "q", "--label", "rig"},
	} {
		if code := e.run(args...); code != 2 {
			t.Fatalf("%v: exit %d, want 2", args, code)
		}
	}
	var out pulled
	if code := e.json(&out, "tracker", "pull", "--label", "rig"); code != 0 || len(out.Items) != 1 || out.Items[0].Key != "1" {
		t.Fatalf("label: exit %d %+v", code, out)
	}
	if code := e.run("tracker", "pull", "9"); code != 2 || !strings.Contains(e.err.String(), "not found") {
		t.Fatalf("missing key: exit %d %q", code, e.err.String())
	}
	// A tracker with no search is a warning, not a failure.
	e = newTrkEnv(t, ghCfg())
	e.tr = fake.New(tracker.Caps{}, nil, nil)
	if code := e.run("tracker", "pull", "--query", "q"); code != 0 || !strings.Contains(e.err.String(), "cannot search") {
		t.Fatalf("exit %d %q", code, e.err.String())
	}
}

// A workflow with a tracker map: the mapped fields are owned and filled from
// the issue, and the snapshot holds them.
func TestTrackerPullMapsOwnedFields(t *testing.T) {
	wf := filepath.Join(t.TempDir(), "mapped.json")
	if err := os.WriteFile(wf, []byte(`{
  "name": "mapped", "version": 1, "initial": "design", "branch": "rig/{short}-{slug}",
  "states": {"design": {"action": "design"}, "ready": {"action": "start"}, "done": {"final": true}},
  "transitions": [{"from": "design", "to": "ready"}, {"from": "ready", "to": "done"}],
  "fields": {"prio": {"type": "string"}, "tags": {"type": "list"}, "ac": {"type": "list"}, "owner": {"type": "string"}},
  "tracker": {"intake": {"states": ["design", "ready"]},
    "map": {"title": "title", "body": "description", "prio": "priority", "tags": "labels", "ac": "acceptance"}}
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	e := newTrkEnv(t, ghCfg())
	e.envVars["RIG_WORKFLOW"] = wf
	is := issue("5", "Five")
	is.Priority, is.Labels, is.Acceptance = "High", []string{"x", "y"}, []string{"it works"}
	e.tr = &scripted{issues: map[string]tracker.Issue{"5": is}}
	var out pulled
	if code := e.json(&out, "tracker", "pull", "5", "--state", "ready"); code != 0 {
		t.Fatalf("exit %d: %s", code, e.err.String())
	}
	it := out.Items[0].Task
	if it.State != "ready" || it.Fields["prio"] != "High" || len(it.Fields["tags"].([]any)) != 2 || it.Fields["ac"].([]any)[0] != "it works" ||
		!strings.HasPrefix(it.Branch, "rig/t-") || len(it.External.Drift) != 0 || strings.Join(it.External.Owned, ",") != "ac,body,prio,tags,title" {
		t.Fatalf("%+v", it)
	}
	if _, ok := it.Fields["owner"]; ok {
		t.Fatal("an unmapped field was filled")
	}
	// Without --state the first intake state is used.
	e.tr.(*scripted).issues["6"] = issue("6", "Six")
	out = pulled{}
	if code := e.json(&out, "tracker", "pull", "6"); code != 0 || out.Items[0].Task.State != "design" {
		t.Fatalf("exit %d %+v", code, out)
	}
}

type refreshRow struct {
	Task, Key, Status string
	Changed           []string
	Replaced          []struct {
		Field string
		Lost  any
	}
	Error string
}

type refreshed struct{ Tasks []refreshRow }

func (r refreshed) by(key string) refreshRow {
	for _, x := range r.Tasks {
		if x.Key == key {
			return x
		}
	}
	return refreshRow{}
}

func (e *trkEnv) eventCount(ref string) int {
	e.t.Helper()
	evs, err := e.st().Events(ctx, ref)
	if err != nil {
		e.t.Fatal(err)
	}
	return len(evs)
}

// AC6: refresh reports unchanged, updated, replaced, gone and error; a failed
// task does not stop the rest; --dry-run prints the same and writes nothing.
func TestTrackerRefresh(t *testing.T) {
	e := newTrkEnv(t, ghCfg())
	s := &scripted{issues: map[string]tracker.Issue{}, errs: map[string]error{}}
	keys := []string{"1", "2", "3", "4", "5", "6"}
	for _, k := range keys {
		s.issues[k] = issue(k, "Issue "+k)
	}
	e.tr = s
	short := map[string]string{}
	for _, k := range keys {
		var out pulled
		if code := e.json(&out, "tracker", "pull", k); code != 0 {
			t.Fatalf("pull %s: exit %d", k, code)
		}
		short[k] = out.Items[0].Task.Short
	}
	// 2: the tracker retitles it. 3: retitled on the tracker after a local edit.
	// 4: deleted. 5: the tracker fails. 6: the body changes and the revision moves.
	is := s.issues["2"]
	is.Title, is.Rev = "Issue 2, renamed", "r2"
	s.issues["2"] = is
	is = s.issues["3"]
	is.Title, is.Rev = "Issue 3, renamed", "r3"
	s.issues["3"] = is
	if _, err := e.st().Update(ctx, short["3"], func(store.Task, []store.Finding) ([]store.Event, error) {
		ev, err := store.NewEvent(store.KindField, store.FieldData{Name: "title", Value: "my own title"})
		return []store.Event{ev}, err
	}); err != nil {
		t.Fatal(err)
	}
	delete(s.issues, "4")
	s.errs["5"] = errors.New("tracker is down")
	is = s.issues["6"]
	is.Description, is.Rev = "new body", "r6"
	s.issues["6"] = is

	before := map[string]int{}
	for k, sh := range short {
		before[k] = e.eventCount(sh)
	}

	var dry refreshed
	if code := e.json(&dry, "tracker", "refresh", "--all", "--dry-run"); code != 2 {
		t.Fatalf("dry-run: exit %d, want 2 (one task failed)", code)
	}
	dryOut := e.out.String()
	for k, sh := range short {
		if e.eventCount(sh) != before[k] {
			t.Fatalf("--dry-run wrote to task %s", k)
		}
	}
	var out refreshed
	if code := e.json(&out, "tracker", "refresh", "--all"); code != 2 || e.out.String() != dryOut {
		t.Fatalf("exit %d; output differs from the dry run:\n%s\n%s", code, dryOut, e.out.String())
	}
	if len(out.Tasks) != 6 {
		t.Fatalf("%d tasks", len(out.Tasks))
	}
	if r := out.by("1"); r.Status != "unchanged" || len(r.Changed) != 0 || len(r.Replaced) != 0 {
		t.Fatalf("1: %+v", r)
	}
	if r := out.by("2"); r.Status != "updated" || strings.Join(r.Changed, ",") != "title" || len(r.Replaced) != 0 {
		t.Fatalf("2: %+v", r)
	}
	if r := out.by("3"); r.Status != "updated" || len(r.Replaced) != 1 || r.Replaced[0].Field != "title" || r.Replaced[0].Lost != "my own title" {
		t.Fatalf("3: %+v", r)
	}
	if r := out.by("4"); r.Status != "gone" {
		t.Fatalf("4: %+v", r)
	}
	if r := out.by("5"); r.Status != "error" || !strings.Contains(r.Error, "tracker is down") {
		t.Fatalf("5: %+v", r)
	}
	if r := out.by("6"); r.Status != "updated" || strings.Join(r.Changed, ",") != "body" {
		t.Fatalf("6: %+v", r)
	}
	// What was written: field events and one tracker-sync, in one update.
	for k, want := range map[string]int{"1": 0, "2": 2, "3": 2, "4": 0, "5": 0, "6": 2} {
		if got := e.eventCount(short[k]) - before[k]; got != want {
			t.Fatalf("task %s: %d new events, want %d", k, got, want)
		}
	}
	tk, _ := e.st().Task(ctx, short["3"])
	if tk.Title != "Issue 3, renamed" || tk.External.Rev != "r3" || tk.External.Snapshot["title"] != "Issue 3, renamed" {
		t.Fatalf("task 3 after refresh: %+v", tk)
	}
	// A second refresh has nothing left to do.
	var again refreshed
	e.json(&again, "tracker", "refresh", "--all")
	for _, k := range []string{"1", "2", "3", "6"} {
		if r := again.by(k); r.Status != "unchanged" {
			t.Fatalf("second refresh, %s: %+v", k, r)
		}
	}
	if got := e.eventCount(short["2"]) - before["2"]; got != 2 {
		t.Fatalf("second refresh wrote to task 2: %d", got)
	}

	// One task by reference; a task with no link is exit 2.
	if code := e.run("tracker", "refresh", short["1"]); code != 0 || !strings.Contains(e.out.String(), "unchanged") {
		t.Fatalf("one task: exit %d %q", code, e.out.String())
	}
	plain, _ := e.st().CreateTask(ctx, store.NewTask{Slug: "plain", Title: "Plain", Initial: "open", Workflow: store.WorkflowRef{Name: "basic", Version: 1}})
	if code := e.run("tracker", "refresh", plain.Short); code != 2 {
		t.Fatalf("unlinked: exit %d", code)
	}
	if code := e.run("tracker", "refresh"); code != 2 {
		t.Fatalf("no target: exit %d", code)
	}
}

// AC7: show prints the Issue and writes nothing; status prints
// {kind, trusted, capabilities, login}, and a rejected login is login.ok=false
// with exit 0.
func TestTrackerShowAndStatus(t *testing.T) {
	e := newTrkEnv(t, ghCfg())
	is := issue("7", "Seven")
	is.Labels = []string{"a"}
	is.Parent = &tracker.IssueRef{Key: "1", Title: "Top"}
	f := fake.New(allCaps, []tracker.Issue{is}, nil)
	e.tr = f
	var raw map[string]any
	if code := e.json(&raw, "tracker", "show", "7"); code != 0 {
		t.Fatalf("exit %d %s", code, e.err.String())
	}
	for _, k := range []string{"key", "url", "title", "description", "acceptance", "priority", "assignee", "labels", "type", "status", "parent", "rev"} {
		if _, ok := raw[k]; !ok {
			t.Fatalf("show --json has no %q: %v", k, raw)
		}
	}
	if raw["key"] != "7" || raw["title"] != "Seven" || len(raw) != 12 {
		t.Fatalf("%v", raw)
	}
	if code := e.run("tracker", "show", "7"); code != 0 || !strings.Contains(e.out.String(), "Seven") {
		t.Fatalf("text: exit %d %q", code, e.out.String())
	}
	if all, _ := e.st().Tasks(ctx, store.TaskFilter{}); len(all) != 0 {
		t.Fatal("show wrote a task")
	}
	if code := e.run("tracker", "show", "99"); code != 2 {
		t.Fatalf("not found: exit %d", code)
	}
	if code := e.run("tracker", "show", "a/b"); code != 2 {
		t.Fatalf("bad key: exit %d", code)
	}

	var st struct {
		Kind         string
		Trusted      bool
		Capabilities *tracker.Caps
		Login        struct {
			OK    bool
			Error string
		}
	}
	if code := e.json(&st, "tracker", "status"); code != 0 || st.Kind != "github" || !st.Trusted || st.Capabilities == nil ||
		!st.Capabilities.Search || !st.Login.OK {
		t.Fatalf("exit %d %+v", code, st)
	}
	f.Err = tracker.ErrAuth
	st.Login.OK, st.Capabilities = true, &tracker.Caps{}
	if code := e.json(&st, "tracker", "status"); code != 0 || st.Login.OK || st.Login.Error == "" || st.Capabilities != nil {
		t.Fatalf("auth: exit %d %+v", code, st)
	}
	f.Err = errors.New("boom")
	if code := e.run("tracker", "status"); code != 2 {
		t.Fatalf("other error: exit %d", code)
	}

	// No Jira login is reported, not failed.
	e = newTrkEnv(t, map[string]any{"kind": "jira", "url": "https://acme.example.com", "project": "RIG"})
	st.Login.OK = true
	if code := e.json(&st, "tracker", "status"); code != 0 || st.Kind != "jira" || st.Login.OK || !strings.Contains(st.Login.Error, "rig login jira") {
		t.Fatalf("no login: exit %d %+v", code, st)
	}
}

// 051 item 4: status reports the configured project when the tracker can
// check it, and says nothing new when it cannot. A missing project does not
// change the exit code.
func TestTrackerStatusProject(t *testing.T) {
	for _, c := range []struct {
		name string
		info *tracker.ProjectInfo
		line string
	}{
		{"ok", &tracker.ProjectInfo{Key: "RIG", Exists: true}, "project: RIG ok\n"},
		{"empty", &tracker.ProjectInfo{Key: "RIG", Exists: true, Empty: true}, "project: RIG ok (no issues yet)\n"},
		{"missing", &tracker.ProjectInfo{Key: "RIG"}, "project: RIG not found\n"},
		{"cannot", nil, ""},
	} {
		e := newTrkEnv(t, ghCfg())
		f := fake.New(allCaps, nil, nil)
		f.Project = c.info
		e.tr = f
		if code := e.run("tracker", "status"); code != 0 {
			t.Fatalf("%s: exit %d", c.name, code)
		}
		if got := e.out.String(); strings.Contains(got, "project:") != (c.line != "") || !strings.Contains(got, c.line) {
			t.Errorf("%s: %q", c.name, got)
		}
		var st struct{ Project *tracker.ProjectInfo }
		if code := e.json(&st, "tracker", "status"); code != 0 || (st.Project == nil) != (c.info == nil) || (c.info != nil && *st.Project != *c.info) {
			t.Errorf("%s: json exit %d %+v", c.name, code, st.Project)
		}
	}
}

// A key the tracker moved is an error on that task with the way out.
func TestTrackerRefreshMovedKey(t *testing.T) {
	e := newTrkEnv(t, ghCfg())
	s := &scripted{issues: map[string]tracker.Issue{"1": issue("1", "One")}, errs: map[string]error{}}
	e.tr = s
	if code := e.run("tracker", "pull", "1"); code != 0 {
		t.Fatal(code)
	}
	s.issues["1"] = issue("9", "One")
	var out refreshed
	if code := e.json(&out, "tracker", "refresh", "--all"); code != 2 || out.Tasks[0].Status != "error" || !strings.Contains(out.Tasks[0].Error, "rig task link") {
		t.Fatalf("exit %d %+v", code, out)
	}
}

// F1: a task linked to another project is not refreshed from this tracker.
func TestTrackerRefreshWrongProject(t *testing.T) {
	cfg := map[string]any{"kind": "github", "project": "acme/a"}
	e := newTrkEnv(t, cfg)
	s := &scripted{issues: map[string]tracker.Issue{"1": issue("1", "One")}, errs: map[string]error{}}
	e.tr = s
	var pull pulled
	if code := e.json(&pull, "tracker", "pull", "1"); code != 0 {
		t.Fatal(code)
	}
	sh := pull.Items[0].Task.Short
	is := s.issues["1"]
	is.Title, is.Rev = "Renamed", "r2"
	s.issues["1"] = is
	e.globalConfig(map[string]any{"tracker": map[string]any{"kind": "github", "project": "acme/b"}})
	n := e.eventCount(sh)
	var out refreshed
	code := e.json(&out, "tracker", "refresh", "--all")
	if code != 2 || out.Tasks[0].Status != "error" || !strings.Contains(out.Tasks[0].Error, "acme/a") || !strings.Contains(out.Tasks[0].Error, "acme/b") {
		t.Fatalf("exit %d %+v", code, out)
	}
	if e.eventCount(sh) != n {
		t.Fatal("a mismatched refresh wrote to the task")
	}
}

// F2: a typed moved-key error names the new key in the way out.
func TestTrackerRefreshMovedKeyTyped(t *testing.T) {
	e := newTrkEnv(t, ghCfg())
	s := &scripted{issues: map[string]tracker.Issue{"1": issue("1", "One")}, errs: map[string]error{}}
	e.tr = s
	var pull pulled
	if code := e.json(&pull, "tracker", "pull", "1"); code != 0 {
		t.Fatal(code)
	}
	sh := pull.Items[0].Task.Short
	s.errs["1"] = fmt.Errorf("wrapped: %w", &jiratracker.MovedError{Asked: "1", Got: "RIG-9"})
	var out refreshed
	e.json(&out, "tracker", "refresh", "--all")
	if want := "rig task link " + sh + " RIG-9"; !strings.Contains(out.Tasks[0].Error, want) {
		t.Fatalf("error %q lacks %q", out.Tasks[0].Error, want)
	}
	// A message that merely says "moved" gets no advice.
	s.errs["1"] = errors.New("the file moved")
	e.out.Reset()
	e.json(&out, "tracker", "refresh", "--all")
	if strings.Contains(out.Tasks[0].Error, "re-link") {
		t.Fatalf("error %q", out.Tasks[0].Error)
	}
}

// F3: RIG_JIRA_TOKEN alone is a PAT only on the v2 API.
func TestTrackerJiraEnvTokenNeedsUserOnCloud(t *testing.T) {
	cfg := map[string]any{"kind": "jira", "url": "https://acme.example.com", "project": "RIG"}
	e := newTrkEnv(t, cfg)
	e.envVars[jiraTokenEnv] = "from-env"
	_, err := e.a.trackerSpec(ctx, e.repo)
	if err == nil || !strings.Contains(err.Error(), "RIG_JIRA_TOKEN is set but no user is stored; run rig login jira") {
		t.Fatalf("v3: %v", err)
	}
	if code := e.run("tracker", "show", "RIG-1"); code != 2 {
		t.Fatalf("exit %d", code)
	}
	cfg["jira"] = map[string]any{"api": "v2"}
	e = newTrkEnv(t, cfg)
	e.envVars[jiraTokenEnv] = "from-env"
	spec, err := e.a.trackerSpec(ctx, e.repo)
	if err != nil || spec.Jira.User != "" || spec.Jira.Token != "from-env" {
		t.Fatalf("v2: %+v, %v", spec, err)
	}
}

// F5 and 38-1: an origin host that is neither the public forge host nor the
// configured forge host (an SSH alias, or a self-hosted server nobody named)
// is refused rather than guessed, so no issue is read from the wrong server.
func TestTrackerRepoHostUnknownRefused(t *testing.T) {
	for _, c := range []struct{ kind, host string }{
		{"github", "work-alias"}, {"github", "github-work"}, {"github", "github.com-work"},
		{"github", "github.acme.example"}, {"gitlab", "gitlab.example.com"},
	} {
		e := newTrkEnv(t, map[string]any{"kind": c.kind})
		gitc(t, e.repo, "remote", "set-url", "origin", "git@"+c.host+":acme/rig.git")
		_, err := e.a.trackerSpec(ctx, e.repo)
		if err == nil || !strings.Contains(err.Error(), c.host) || !strings.Contains(err.Error(), "tracker.url") {
			t.Fatalf("%s %s: want a refusal naming the host and tracker.url, got %v", c.kind, c.host, err)
		}
	}
}

// An alias origin works once tracker.url names the host.
func TestTrackerRepoHostAliasWithURL(t *testing.T) {
	e := newTrkEnv(t, map[string]any{"kind": "github", "url": "https://github.com"})
	gitc(t, e.repo, "remote", "set-url", "origin", "git@github-work:acme/rig.git")
	spec, err := e.a.trackerSpec(ctx, e.repo)
	if err != nil || spec.Link != (trackerLink{"github", "github.com", "acme/rig"}) || spec.GitHub.Host != "" {
		t.Fatalf("spec %+v, %v", spec, err)
	}
}

func TestTrackerRepoHostConfiguredForgeHost(t *testing.T) {
	e := newTrkEnv(t, nil)
	e.globalConfig(map[string]any{
		"forge":   map[string]any{"host": "git.corp.example"},
		"tracker": map[string]any{"kind": "gitlab"},
	})
	gitc(t, e.repo, "remote", "set-url", "origin", "git@git.corp.example:acme/rig.git")
	spec, err := e.a.trackerSpec(ctx, e.repo)
	if err != nil || spec.Link != (trackerLink{"gitlab", "git.corp.example", "acme/rig"}) || spec.GitLab.Host != "git.corp.example" {
		t.Fatalf("spec %+v, %v", spec, err)
	}
}

// A forge.host with a port matches an origin on that host and port; the
// host keeps its port for the adapter.
func TestTrackerRepoHostForgeHostWithPort(t *testing.T) {
	e := newTrkEnv(t, nil)
	e.globalConfig(map[string]any{
		"forge":   map[string]any{"kind": "gitlab", "host": "git.corp.example:8443"},
		"tracker": map[string]any{"kind": "gitlab"},
	})
	gitc(t, e.repo, "remote", "set-url", "origin", "https://git.corp.example:8443/acme/rig.git")
	spec, err := e.a.trackerSpec(ctx, e.repo)
	if err != nil || spec.GitLab.Host != "git.corp.example:8443" || spec.Link.Host != "git.corp.example:8443" {
		t.Fatalf("spec %+v, %v", spec, err)
	}
}

// forge.host counts for the tracker only when forge.kind is the tracker's kind.
func TestTrackerRepoHostForgeHostOtherKind(t *testing.T) {
	e := newTrkEnv(t, nil)
	e.globalConfig(map[string]any{
		"forge":   map[string]any{"kind": "gitlab", "host": "git.corp.example"},
		"tracker": map[string]any{"kind": "github", "project": "acme/rig"},
	})
	gitc(t, e.repo, "remote", "remove", "origin")
	spec, err := e.a.trackerSpec(ctx, e.repo)
	if err != nil || spec.GitHub.Host != "" || spec.Link.Host != "github.com" {
		t.Fatalf("spec %+v, %v", spec, err)
	}
}

// With forge.kind unset, a forge.host that names the other forge is not this
// tracker's host, with or without an origin on it.
func TestTrackerRepoHostForgeHostOfOtherForge(t *testing.T) {
	e := newTrkEnv(t, nil)
	e.globalConfig(map[string]any{
		"forge":   map[string]any{"host": "github.com"},
		"tracker": map[string]any{"kind": "gitlab", "project": "acme/rig"},
	})
	gitc(t, e.repo, "remote", "remove", "origin")
	if spec, err := e.a.trackerSpec(ctx, e.repo); err != nil || spec.GitLab.Host != "" || spec.Link.Host != "gitlab.com" {
		t.Fatalf("no origin: spec %+v, %v", spec, err)
	}
	e = newTrkEnv(t, nil)
	e.globalConfig(map[string]any{
		"forge":   map[string]any{"kind": "gitlab", "host": "git.corp.example"},
		"tracker": map[string]any{"kind": "github"},
	})
	gitc(t, e.repo, "remote", "set-url", "origin", "git@git.corp.example:acme/rig.git")
	if _, err := e.a.trackerSpec(ctx, e.repo); err == nil || !strings.Contains(err.Error(), "tracker.url") {
		t.Fatalf("origin on another kind's forge.host: %v", err)
	}
}

type parentRefresh struct {
	Tasks []struct {
		Task, Key, Status string
		Changed, Warnings []string
	}
}

// F4: refresh links a task to its parent once the parent's issue is a task.
func TestTrackerRefreshResolvesParent(t *testing.T) {
	e := newTrkEnv(t, ghCfg())
	e.tr = &scripted{issues: map[string]tracker.Issue{}, errs: map[string]error{}}
	s := e.tr.(*scripted)
	for _, is := range chain()[:2] {
		s.issues[is.Key] = is
	}
	var child pulled
	if code := e.json(&child, "tracker", "pull", "2"); code != 0 {
		t.Fatal(code)
	}
	var parent pulled
	if code := e.json(&parent, "tracker", "pull", "1"); code != 0 {
		t.Fatal(code)
	}
	cs, ps := child.Items[0].Task.Short, parent.Items[0].Task
	task := func(ref string) store.Task {
		tk, err := e.st().Task(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		return tk
	}
	if task(cs).Parent != "" {
		t.Fatal("child has a parent before the refresh")
	}
	var dry parentRefresh
	e.out.Reset()
	e.json(&dry, "tracker", "refresh", cs, "--dry-run")
	if task(cs).Parent != "" {
		t.Fatal("--dry-run set the parent")
	}
	var out parentRefresh
	e.out.Reset()
	if code := e.json(&out, "tracker", "refresh", cs); code != 0 {
		t.Fatalf("exit %d %s", code, e.err.String())
	}
	if got := task(cs).Parent; got != ps.ID {
		t.Fatalf("parent = %q, want %q", got, ps.ID)
	}
	if !slices.Contains(out.Tasks[0].Changed, "parent") {
		t.Fatalf("%+v", out)
	}
}

// F4: a parent that would make a cycle is a warning, not a parent.
func TestTrackerRefreshParentCycle(t *testing.T) {
	e := newTrkEnv(t, ghCfg())
	s := &scripted{issues: map[string]tracker.Issue{}, errs: map[string]error{}}
	e.tr = s
	for _, is := range chain()[:2] {
		s.issues[is.Key] = is
	}
	var child, parent pulled
	e.json(&child, "tracker", "pull", "2")
	e.out.Reset()
	e.json(&parent, "tracker", "pull", "1")
	cs, ps := child.Items[0].Task, parent.Items[0].Task
	// The parent task is made a child of the child task by hand.
	if _, err := e.st().Update(ctx, ps.Short, func(store.Task, []store.Finding) ([]store.Event, error) {
		ev, err := store.NewEvent(store.KindParent, store.ParentData{Task: cs.ID})
		return []store.Event{ev}, err
	}); err != nil {
		t.Fatal(err)
	}
	var out parentRefresh
	e.out.Reset()
	if code := e.json(&out, "tracker", "refresh", cs.Short); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if tk, _ := e.st().Task(ctx, cs.Short); tk.Parent != "" {
		t.Fatalf("a cycle was linked: %q", tk.Parent)
	}
	if len(out.Tasks[0].Warnings) != 1 || !strings.Contains(out.Tasks[0].Warnings[0], "cycle") {
		t.Fatalf("%+v", out)
	}
	// Plain-text output shows the warning too.
	e.out.Reset()
	if code := e.run("tracker", "refresh", cs.Short); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(e.out.String(), "warning: ") || !strings.Contains(e.out.String(), "cycle") {
		t.Fatalf("text output has no cycle warning:\n%s", e.out.String())
	}
}

// Round 2: issue 7 in acme/a and issue 7 in acme/b are two tasks.
func TestTrackerPullSameKeyOtherProject(t *testing.T) {
	e := newTrkEnv(t, map[string]any{"kind": "github", "project": "acme/a"})
	e.tr = fake.New(allCaps, []tracker.Issue{issue("7", "Seven")}, nil)
	var a, b, again pulled
	if code := e.json(&a, "tracker", "pull", "7"); code != 0 {
		t.Fatal(code)
	}
	e.globalConfig(map[string]any{"tracker": map[string]any{"kind": "github", "project": "ACME/B"}})
	e.out.Reset()
	if code := e.json(&b, "tracker", "pull", "7"); code != 0 || b.Items[0].Existing || b.Items[0].Task.ID == a.Items[0].Task.ID {
		t.Fatalf("other project: exit %d %+v", code, b)
	}
	// The same project in another case is the same link.
	e.globalConfig(map[string]any{"tracker": map[string]any{"kind": "github", "project": "Acme/A"}})
	e.out.Reset()
	if code := e.json(&again, "tracker", "pull", "7"); code != 0 || !again.Items[0].Existing || again.Items[0].Task.ID != a.Items[0].Task.ID {
		t.Fatalf("same project, other case: exit %d %+v", code, again)
	}
	if all, _ := e.st().Tasks(ctx, store.TaskFilter{}); len(all) != 2 {
		t.Fatalf("%d tasks, want 2", len(all))
	}
}

// Round 2: a parent in another project is not this task's parent.
func TestTrackerPullParentSameProjectOnly(t *testing.T) {
	e := newTrkEnv(t, map[string]any{"kind": "github", "project": "acme/b"})
	e.tr = fake.New(allCaps, []tracker.Issue{issue("1", "Parent")}, nil)
	var p pulled
	if code := e.json(&p, "tracker", "pull", "1"); code != 0 {
		t.Fatal(code)
	}
	e.globalConfig(map[string]any{"tracker": map[string]any{"kind": "github", "project": "acme/a"}})
	child := issue("2", "Child")
	child.Parent = &tracker.IssueRef{Key: "1"}
	e.tr = fake.New(allCaps, []tracker.Issue{child}, nil)
	e.out.Reset()
	var c pulled
	if code := e.json(&c, "tracker", "pull", "2"); code != 0 || c.Items[0].Task.Parent != "" || c.Items[0].Parent != "" {
		t.Fatalf("exit %d %+v", code, c)
	}
}

// Round 2: a Jira link carries no project, even when tracker.project is set.
func TestTrackerJiraLinkHasNoProject(t *testing.T) {
	e := newTrkEnv(t, map[string]any{"kind": "jira", "url": "https://acme.example.com", "project": "RIG"})
	e.stores[storeKeychain].Set(ctx, "jira:acme.example.com", secret.Secret{User: "me@example.com", Token: "t"})
	spec, err := e.a.trackerSpec(ctx, e.repo)
	if err != nil || spec.Link.Project != "" {
		t.Fatalf("%+v, %v", spec.Link, err)
	}
}

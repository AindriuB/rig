package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/secret"
	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/tracker"
	"github.com/AindriuB/rig/internal/tracker/fake"
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
	e = newTrkEnv(t, map[string]any{"kind": "exec", "command": []string{"/usr/bin/true", "--x"}})
	spec, err := e.a.trackerSpec(ctx, e.repo)
	if err != nil || strings.Join(spec.Exec, " ") != "/usr/bin/true --x" || spec.Dir == "" {
		t.Fatalf("spec %+v, %v", spec, err)
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
		spec.Jira.Site != "https://acme.example.com" || spec.Link != (trackerLink{"jira", "acme.example.com", "RIG"}) {
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
	if err != nil || spec.GitHub.Repo != "acme/rig" || spec.GitHub.Host != "" || spec.GitHub.Dir != e.repo ||
		spec.Link != (trackerLink{"github", "github.com", "acme/rig"}) {
		t.Fatalf("project from config: %+v, %v", spec, err)
	}
	e = newTrkEnv(t, map[string]any{"kind": "github"})
	if spec, err = e.a.trackerSpec(ctx, e.repo); err != nil || spec.GitHub.Repo != "acme/rig" {
		t.Fatalf("project from remote: %+v, %v", spec, err)
	}
	e = newTrkEnv(t, map[string]any{"kind": "gitlab"})
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

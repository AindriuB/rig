package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/git"
	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/store/gitref"
	"github.com/AindriuB/rig/internal/tracker"
	"github.com/AindriuB/rig/internal/tracker/fake"
)

// hookEnv is a real repository under a workflow whose hooks are the test
// binary, which appends each event it is given to out.
type hookEnv struct {
	t    *testing.T
	repo string
	out  string
	env  map[string]string
	// tracker builds the tracker for rig tracker commands; nil is the default.
	tracker trackerBuilder
}

// newHookEnv writes a workflow with the given hooks (JSON objects; "run" is
// filled in with the test binary and its arguments when it is a list of
// arguments only) and a repository to run it in.
func newHookEnv(t *testing.T, hooks func(exe, repo string) string) *hookEnv {
	t.Helper()
	repo, _ := newRepo(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wf := `{
  "name": "hk", "version": 1, "initial": "open", "branch": "task/{short}-{slug}",
  "states": { "open": { "action": "start" }, "stopped": { "action": "decide", "who": "human" } },
  "transitions": [
    { "from": "open", "to": "stopped", "takes": "reason", "count": "rounds" },
    { "from": "stopped", "to": "open" }
  ],
  "fields": { "owner": { "type": "string" }, "type": { "type": "string" } },
  "tracker": { "report": { "status": { "open": "In Progress" } }, "push": { "levels": ["Epic", "Story", "Subtask"], "types": ["Bug"] } },
  "findings": { "severities": ["P1"] },
  "counters": ["rounds"],
  "hooks": [` + hooks(exe, repo) + `]
}`
	path := filepath.Join(t.TempDir(), "hk.json")
	if err := os.WriteFile(path, []byte(wf), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "events.jsonl")
	t.Setenv("RIG_CMD_HELPER", out)
	t.Setenv("RIG_CMD_EXIT", "0")
	t.Setenv("RIG_WORKFLOW", path)
	return &hookEnv{t: t, repo: repo, out: out, env: map[string]string{"RIG_WORKFLOW": path, "RIG_ACTOR": "tester"}}
}

func (e *hookEnv) rig(args ...string) result {
	e.t.Helper()
	var out, errOut bytes.Buffer
	a := &app{stdout: &out, stderr: &errOut, cwd: e.repo, now: tick(), tracker: e.tracker,
		getenv: func(k string) string { return e.env[k] }}
	a.store = a.openStore
	code := a.run(context.Background(), append(args, "--repo", e.repo))
	return result{code, out.String(), errOut.String()}
}

func (e *hookEnv) mustRig(args ...string) result {
	e.t.Helper()
	r := e.rig(args...)
	if r.code != 0 {
		e.t.Fatalf("rig %v: %+v", args, r)
	}
	return r
}

// delivered is the payloads the hooks got, in order.
func (e *hookEnv) delivered() []map[string]any {
	e.t.Helper()
	b, err := os.ReadFile(e.out)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		e.t.Fatal(err)
	}
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			e.t.Fatalf("payload %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func (e *hookEnv) events(ref string) []store.Event {
	e.t.Helper()
	st, err := gitref.Open(git.Repo{Dir: e.repo}, store.Options{})
	if err != nil {
		e.t.Fatal(err)
	}
	evs, err := st.Events(context.Background(), ref)
	if err != nil {
		e.t.Fatal(err)
	}
	return evs
}

func (e *hookEnv) newTask(slug string) string {
	e.t.Helper()
	r := e.mustRig("task", "new", "--slug", slug, "--title", "T", "--json")
	return decodeObj(e.t, r.out)["short"].(string)
}

func kindHooks(kinds ...string) func(string, string) string {
	return func(exe, _ string) string {
		var hs []string
		for _, k := range kinds {
			argv, _ := json.Marshal([]string{exe})
			hs = append(hs, `{"name":"h-`+strings.ReplaceAll(k, "*", "any")+`","on":"kind:`+k+`","run":`+string(argv)+`,"env":["RIG_CMD_HELPER","RIG_CMD_EXIT"]}`)
		}
		return strings.Join(hs, ",")
	}
}

func kindOf(m map[string]any) string { return m["event"].(map[string]any)["kind"].(string) }

// AC1, AC3: a move runs the transition hook, then the count hook, once each,
// with the logged event ids and the task folded to each event.
func TestHooksRunPerMoveEvent(t *testing.T) {
	e := newHookEnv(t, kindHooks("count", "transition"))
	short := e.newTask("m")
	e.mustRig("task", "move", short, "stopped", "--reason", "r")
	got := e.delivered()
	if len(got) != 2 || kindOf(got[0]) != "transition" || kindOf(got[1]) != "count" {
		t.Fatalf("delivered: %v", got)
	}
	log := e.events(short)
	n := len(log)
	if got[0]["event"].(map[string]any)["id"] != log[n-2].ID || got[1]["event"].(map[string]any)["id"] != log[n-1].ID {
		t.Fatalf("ids %v do not match the log tail %s, %s", got, log[n-2].ID, log[n-1].ID)
	}
	t0, t1 := got[0]["task"].(map[string]any), got[1]["task"].(map[string]any)
	if t0["recordedState"] != "stopped" || t0["counters"].(map[string]any)["rounds"] != nil && t0["counters"].(map[string]any)["rounds"].(float64) != 0 {
		t.Fatalf("transition hook saw %v", t0)
	}
	if t1["counters"].(map[string]any)["rounds"].(float64) != 1 {
		t.Fatalf("count hook saw %v", t1)
	}
}

// AC2: each single-task write runs a matching hook; tracker commands run none.
func TestHooksRunForEveryWriteCommand(t *testing.T) {
	e := newHookEnv(t, kindHooks("*"))
	short := e.newTask("w")
	if len(e.delivered()) == 0 {
		t.Fatal("task new ran no hook")
	}
	steps := [][]string{
		{"task", "set", short, "--field", "owner=bob"},
		{"finding", "add", short, "--severity", "P1", "--summary", "s"},
		{"note", "add", short, "--kind", "decision", "--text", "t"},
		{"note", "add", "--kind", "decision", "--text", "repo"},
	}
	for _, args := range steps {
		before := len(e.delivered())
		e.mustRig(args...)
		got := e.delivered()
		if len(got) <= before {
			t.Fatalf("%v ran no hook", args)
		}
		// A repository note's payload has no task.
		if args[0] == "note" && len(args) > 2 && args[2] == "--kind" {
			if last := got[len(got)-1]; kindOf(last) != "note" || last["task"] != nil {
				t.Fatalf("repository note payload: %v", last)
			}
		}
	}
	// finding set
	r := e.mustRig("finding", "list", short, "--json")
	var fl struct{ Findings []struct{ ID string } }
	if err := json.Unmarshal([]byte(r.out), &fl); err != nil || len(fl.Findings) == 0 {
		var arr []struct{ ID string }
		if err2 := json.Unmarshal([]byte(r.out), &arr); err2 != nil || len(arr) == 0 {
			t.Fatalf("finding list: %s", r.out)
		}
		fl.Findings = arr
	}
	before := len(e.delivered())
	e.mustRig("finding", "set", short, fl.Findings[0].ID, "--status", "fixed")
	if len(e.delivered()) <= before {
		t.Fatal("finding set ran no hook")
	}
}

func ptr(n int) *int { return &n }

// trackerEnv points e at a github tracker (a fake) through a throwaway HOME.
func (e *hookEnv) trackerEnv(tr tracker.Tracker) {
	e.t.Helper()
	home := e.t.TempDir()
	e.t.Setenv("HOME", home)
	e.t.Setenv("USERPROFILE", home)
	e.t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	e.t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	e.env["HOME"], e.env["USERPROFILE"] = home, home
	if err := os.MkdirAll(filepath.Join(home, ".rig"), 0o700); err != nil {
		e.t.Fatal(err)
	}
	cfg := `{"tracker":{"kind":"github","project":"acme/rig"}}`
	if err := os.WriteFile(filepath.Join(home, ".rig", "config.json"), []byte(cfg), 0o600); err != nil {
		e.t.Fatal(err)
	}
	gitc(e.t, e.repo, "remote", "set-url", "origin", "git@github.com:acme/rig.git")
	e.tracker = func(context.Context, trackerSpec) (tracker.Tracker, error) { return tr, nil }
}

// AC2: rig tracker pull, refresh, report and push write through the plain
// store, so they run no hook; rig task link runs one.
func TestHooksTrackerRunsNone(t *testing.T) {
	e := newHookEnv(t, kindHooks("*"))
	caps := tracker.Caps{Parent: true, Children: true, Types: true, Search: true, Comment: true, SetStatus: true, Create: true}
	mk := func(key, title string) tracker.Issue {
		return tracker.Issue{Key: key, URL: "https://t.example/" + key, Title: title, Description: "about " + title, Type: "Task", Status: "Open"}
	}
	fk := fake.New(caps, []tracker.Issue{mk("12", "First"), mk("13", "Second")}, []string{"In Progress"})
	fk.Types = []tracker.IssueType{{Name: "Epic", Level: ptr(3)}, {Name: "Story", Level: ptr(2)}, {Name: "Subtask", Level: ptr(1)}, {Name: "Bug"}}
	e.trackerEnv(fk)
	local := e.newTask("pushme")
	e.mustRig("task", "set", local, "--field", "type=Bug")
	steps := [][]string{
		{"tracker", "pull", "12"},
		{"tracker", "refresh", "--all"},
		{"tracker", "report", "12-first", "--comment", "summary"},
		{"tracker", "push", local},
	}
	for _, args := range steps {
		if args[1] == "refresh" {
			// Change the remote issue so refresh has something to write.
			if _, err := fk.SetStatus(context.Background(), "12", "In Progress"); err != nil {
				t.Fatal(err)
			}
		}
		n := len(e.delivered())
		before := 0
		if args[1] == "refresh" {
			before = len(e.events("12-first"))
		}
		r := e.rig(args...)
		if r.code != 0 {
			t.Fatalf("%v: %+v", args, r)
		}
		if args[1] == "refresh" && len(e.events("12-first")) <= before {
			t.Fatalf("refresh wrote nothing: %+v", r)
		}
		if len(e.delivered()) != n {
			t.Fatalf("%v ran a hook: %v", args, e.delivered()[n:])
		}
	}
	// The tracker commands did write: pull made the task and its log grew.
	if got := e.events("12-first"); len(got) == 0 {
		t.Fatal("pull wrote no task")
	}
	// task link goes through the hook-running store.
	short := e.newTask("lk")
	n := len(e.delivered())
	e.mustRig("task", "link", short, "github:13")
	if len(e.delivered()) <= n {
		t.Fatal("task link ran no hook")
	}
}

// AC4: a write inside a hook records its cause.
func TestHookWriteRecordsCause(t *testing.T) {
	e := newHookEnv(t, kindHooks())
	short := e.newTask("c")
	log := e.events(short)
	e.env["RIG_HOOK_EVENT"], e.env["RIG_HOOK_NAME"], e.env["RIG_HOOK_TASK"] = log[0].ID, "hk", short
	e.mustRig("task", "set", short, "--field", "owner=a")
	log = e.events(short)
	last := log[len(log)-1]
	if last.Cause == nil || *last.Cause != (store.Cause{Event: log[0].ID, Hook: "hk", Depth: 1}) {
		t.Fatalf("cause %+v", last.Cause)
	}
	// Naming that event, whose cause has depth 1, gives depth 2; then 3.
	e.env["RIG_HOOK_EVENT"] = last.ID
	e.mustRig("task", "set", short, "--field", "owner=b")
	log = e.events(short)
	if c := log[len(log)-1].Cause; c == nil || c.Depth != 2 {
		t.Fatalf("cause %+v", c)
	}
	e.env["RIG_HOOK_EVENT"] = log[len(log)-1].ID
	e.mustRig("task", "set", short, "--field", "owner=c")
	log = e.events(short)
	if c := log[len(log)-1].Cause; c == nil || c.Depth != 3 {
		t.Fatalf("cause %+v", c)
	}
	// An id that is in no log is depth 1.
	e.env["RIG_HOOK_EVENT"] = "01NOSUCHEVENT"
	e.mustRig("task", "set", short, "--field", "owner=d")
	log = e.events(short)
	if c := log[len(log)-1].Cause; c == nil || c.Depth != 1 {
		t.Fatalf("cause %+v", c)
	}
}

// AC5: at depth 3 the write lands, no hook runs, one stderr line says why.
func TestHookDepthGuard(t *testing.T) {
	e := newHookEnv(t, kindHooks("*"))
	short := e.newTask("g")
	n := len(e.delivered())
	e.env["RIG_HOOK_EVENT"], e.env["RIG_HOOK_TASK"] = "01X", short
	// Make the named event one at depth 2.
	st, _ := gitref.Open(git.Repo{Dir: e.repo}, store.Options{Cause: &store.Cause{Event: "01X", Hook: "h", Depth: 2}})
	_, evs, err := st.UpdateEvents(context.Background(), short, func(store.Task, []store.Finding) ([]store.Event, error) {
		ev, err := store.NewEvent(store.KindField, store.FieldData{Name: "owner", Value: "z"})
		return []store.Event{ev}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	e.env["RIG_HOOK_EVENT"] = evs[0].ID
	r := e.mustRig("task", "set", short, "--field", "owner=q")
	if len(e.delivered()) != n {
		t.Fatal("a hook ran at depth 3")
	}
	log := e.events(short)
	if log[len(log)-1].Cause.Depth != 3 || !strings.Contains(r.errOut, "depth 3") || !strings.Contains(r.errOut, log[len(log)-1].ID) || strings.Count(r.errOut, "\n") != 1 {
		t.Fatalf("stderr %q, cause %+v", r.errOut, log[len(log)-1].Cause)
	}
}

// AC6: hooks that move the task back and forth stop after three writes.
func TestHookChainStops(t *testing.T) {
	t.Setenv("RIG_HOOK_REENTER", "1")
	e := newHookEnv(t, func(exe, repo string) string {
		mv := func(on, to string) string {
			argv, _ := json.Marshal([]string{exe, "task", "move", "{short}", to, "--reason", "x", "--repo", repo})
			return `{"on":"` + on + `","run":` + string(argv) + `,"env":["RIG_HOOK_REENTER","RIG_WORKFLOW"]}`
		}
		return mv("enter:stopped", "open") + "," + mv("enter:open", "stopped")
	})
	short := e.newTask("ch")
	e.mustRig("task", "move", short, "stopped", "--reason", "go")
	caused := 0
	for _, ev := range e.events(short) {
		if ev.Cause != nil {
			caused++
		}
	}
	// open->stopped, then stopped->open (depth 1), open->stopped (2),
	// stopped->open (3); the count event of a move shares its cause.
	var transitions int
	for _, ev := range e.events(short) {
		if ev.Cause != nil && ev.Kind == store.KindTransition {
			transitions++
		}
	}
	if transitions != 3 {
		t.Fatalf("hook-caused transitions: %d (caused events %d)", transitions, caused)
	}
}

// AC7: a failing hook leaves the write and the exit code, and is reported.
func TestHookFailureReported(t *testing.T) {
	t.Setenv("RIG_CMD_EXIT", "0")
	e := newHookEnv(t, func(exe, _ string) string {
		argv, _ := json.Marshal([]string{exe})
		return `{"name":"bad","on":["transition","kind:count"],"run":` + string(argv) + `,"env":["RIG_CMD_HELPER","RIG_CMD_EXIT"]}`
	})
	t.Setenv("RIG_CMD_EXIT", "3")
	short := e.newTask("f")
	r := e.mustRig("task", "move", short, "stopped", "--reason", "r", "--json")
	if strings.Count(r.errOut, "rig task move: hook: ") != 2 {
		t.Fatalf("stderr %q", r.errOut)
	}
	he, _ := decodeObj(t, r.out)["hookErrors"].([]any)
	if len(he) != 2 || decodeObj(t, r.out)["state"] != "stopped" {
		t.Fatalf("out %s", r.out)
	}
}

// AC9: unnamed-hook warnings print once for a write that runs hooks.
func TestHookWarningOnce(t *testing.T) {
	e := newHookEnv(t, func(exe, _ string) string {
		argv, _ := json.Marshal([]string{exe})
		return `{"on":"kind:*","run":` + string(argv) + `,"env":["RIG_CMD_HELPER","RIG_CMD_EXIT"]}`
	})
	short := e.newTask("wn")
	r := e.mustRig("task", "move", short, "stopped", "--reason", "r")
	if strings.Count(r.errOut, "hook-1") != 1 {
		t.Fatalf("stderr %q", r.errOut)
	}
	v := e.rig("workflow", "validate")
	if v.code != 0 || !strings.Contains(v.errOut+v.out, "hook-1") {
		t.Fatalf("validate: %+v", v)
	}
}

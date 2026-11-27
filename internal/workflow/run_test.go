package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AindriuB/rig/internal/proc"
	"github.com/AindriuB/rig/internal/store"
)

// The test binary doubles as the program gates and hooks run: with
// RIG_WF_HELPER set it records its arguments and stdin to that file, then
// sleeps RIG_WF_SLEEP seconds and exits RIG_WF_EXIT, without running tests.
// This works the same on every OS, with no shell involved.
func TestMain(m *testing.M) {
	if out := os.Getenv("RIG_WF_HELPER"); out != "" {
		os.Exit(helper(out))
	}
	os.Exit(m.Run())
}

type helperRecord struct {
	Args  []string `json:"args"`
	Stdin string   `json:"stdin"`
	Env   []string `json:"env"`
}

func helper(out string) int {
	stdin, _ := io.ReadAll(os.Stdin)
	rec, _ := json.Marshal(helperRecord{Args: os.Args[1:], Stdin: string(stdin), Env: os.Environ()})
	// Append, one record per line, so several hooks can share the file.
	f, err := os.OpenFile(out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 99
	}
	_, _ = f.Write(append(rec, '\n'))
	_ = f.Close()
	if s, _ := strconv.Atoi(os.Getenv("RIG_WF_SLEEP")); s > 0 {
		time.Sleep(time.Duration(s) * time.Second)
	}
	code, _ := strconv.Atoi(os.Getenv("RIG_WF_EXIT"))
	if code != 0 {
		os.Stderr.WriteString("helper failed on purpose\n")
	}
	return code
}

// helperRunner returns a ProcRunner whose programs record to the returned
// file, and the program name to put first in an argument vector.
func helperRunner(t *testing.T, env ...string) (ProcRunner, string, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "records.jsonl")
	return ProcRunner{Env: append([]string{"RIG_WF_HELPER=" + out}, env...)}, exe, out
}

func records(t *testing.T, path string) []helperRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []helperRecord
	dec := json.NewDecoder(bytes.NewReader(data))
	for dec.More() {
		var r helperRecord
		if err := dec.Decode(&r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// hostile is a placeholder value that a shell would split or act on.
const hostile = `two words; echo pwned && $(id) "q" 'q' | >x`

func TestCommandGateRunsArgv(t *testing.T) {
	w := fixture(t)
	r, exe, out := helperRunner(t)
	var g Gate
	_ = g.UnmarshalJSON(mustJSON(t, map[string][]string{"command": {exe, "{reason}", "{short}", "x{short}"}}))
	task := store.Task{ID: "01X", Short: "t-1", Slug: "s", State: "reviewed"}
	res := w.Check(ctx, Env{Runner: r}, task, nil, Transition{From: StateList{"reviewed"}, Gates: []Gate{g}}, Args{Reason: hostile})
	if !res[0].OK {
		t.Fatalf("result = %+v", res[0])
	}
	recs := records(t, out)
	if len(recs) != 1 {
		t.Fatalf("records = %+v", recs)
	}
	if a := recs[0].Args; len(a) != 3 || a[0] != hostile || a[1] != "t-1" || a[2] != "x{short}" {
		t.Fatalf("args = %q", a)
	}
	var got store.Task
	if err := json.Unmarshal([]byte(recs[0].Stdin), &got); err != nil || got.Short != "t-1" {
		t.Fatalf("stdin = %s (%v)", recs[0].Stdin, err)
	}

	// A non-zero exit fails the gate, with the program's stderr.
	r.Env = append(r.Env, "RIG_WF_EXIT=3")
	res = w.Check(ctx, Env{Runner: r}, task, nil, Transition{From: StateList{"reviewed"}, Gates: []Gate{g}}, Args{})
	if res[0].OK || !strings.Contains(res[0].Message, "exited 3") || !strings.Contains(res[0].Message, "helper failed on purpose") {
		t.Fatalf("failing gate = %+v", res[0])
	}
}

func TestHooks(t *testing.T) {
	r, exe, out := helperRunner(t)
	w := &Workflow{Hooks: []Hook{
		{On: StateList{"leave:reviewed"}, Run: []string{exe, "leave", "{state}"}},
		{On: StateList{"enter:stopped"}, Run: []string{exe, "enter", "{short}", "{reason}", "{sha}", "{id}", "{slug}"}},
		{On: StateList{"enter:verified"}, Run: []string{exe, "not-run"}},
		{On: StateList{"transition"}, Run: []string{filepath.Join(t.TempDir(), "no-such-program")}},
		{On: StateList{"transition"}, Run: []string{exe, "after-a-failure"}},
	}}
	ev, err := store.NewEvent(store.KindTransition, store.TransitionData{From: "reviewed", To: "stopped", Reason: hostile})
	if err != nil {
		t.Fatal(err)
	}
	task := store.Task{ID: "01X", Short: "t-1", Slug: "s", State: "stopped", Reason: hostile}
	errs := w.RunHooks(ctx, r, ev, task, "")
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "hook 3") {
		t.Fatalf("errs = %v", errs)
	}
	recs := records(t, out)
	if len(recs) != 3 {
		t.Fatalf("records = %+v", recs)
	}
	if a := recs[0].Args; len(a) != 2 || a[0] != "leave" || a[1] != "stopped" {
		t.Fatalf("leave args = %q", a)
	}
	want := []string{"enter", "t-1", hostile, "", "01X", "s"}
	if a := recs[1].Args; len(a) != len(want) || a[2] != hostile || a[3] != "" || a[4] != "01X" {
		t.Fatalf("enter args = %q, want %q", a, want)
	}
	if recs[2].Args[0] != "after-a-failure" {
		t.Fatalf("third hook = %q", recs[2].Args)
	}
	var in struct {
		Event store.Event `json:"event"`
		Task  TaskView    `json:"task"`
	}
	if err := json.Unmarshal([]byte(recs[1].Stdin), &in); err != nil || in.Event.Kind != store.KindTransition || in.Task.Short != "t-1" {
		t.Fatalf("hook stdin = %s (%v)", recs[1].Stdin, err)
	}

	if in.Task.State != "stopped" || in.Task.RecordedState != "stopped" || in.Task.Reason != hostile {
		t.Fatalf("hook task = %+v", in.Task)
	}

	// The hook's task is the recorded state; effective is at the head given.
	bound := &Workflow{States: map[string]State{"ready": {ShaBound: true}}, OnNewCommit: "implementing",
		Hooks: []Hook{{On: StateList{"enter:ready"}, Run: []string{"h"}}}}
	fr := &fakeRunner{}
	rev, _ := store.NewEvent(store.KindTransition, store.TransitionData{From: "ci_green", To: "ready"})
	ready := store.Task{State: "ready", Shas: map[string]string{"ready": "s1"}}
	if errs := bound.RunHooks(ctx, fr, rev, ready, "s2"); errs != nil {
		t.Fatal(errs)
	}
	if !strings.Contains(fr.stdin, `"state":"ready","recordedState":"ready"`) || !strings.Contains(fr.stdin, `"effective":"implementing"`) || !strings.Contains(fr.stdin, `"headSha":"s2"`) {
		t.Fatalf("hook stdin = %s", fr.stdin)
	}

	// Events other than transitions run no hooks.
	note, _ := store.NewEvent(store.KindNote, store.NoteData{Text: "x"})
	if errs := w.RunHooks(ctx, r, note, task, ""); errs != nil {
		t.Fatalf("note event: %v", errs)
	}
}

func TestLimit(t *testing.T) {
	if Limit != 30*time.Second {
		t.Fatalf("Limit = %s", Limit)
	}
	old := limit
	limit = 300 * time.Millisecond
	defer func() { limit = old }()

	r, exe, _ := helperRunner(t, "RIG_WF_SLEEP=20")
	w := &Workflow{Hooks: []Hook{{On: StateList{"transition"}, Run: []string{exe}}}}
	ev, _ := store.NewEvent(store.KindTransition, store.TransitionData{From: "a", To: "b"})
	start := time.Now()
	errs := w.RunHooks(ctx, r, ev, store.Task{}, "")
	if len(errs) != 1 || !errors.Is(errs[0], proc.ErrTimeout) {
		t.Fatalf("errs = %v", errs)
	}
	var g Gate
	_ = g.UnmarshalJSON(mustJSON(t, map[string][]string{"command": {exe}}))
	res := fixture(t).Check(ctx, Env{Runner: r}, store.Task{}, nil, Transition{Gates: []Gate{g}}, Args{})
	if res[0].OK || !strings.Contains(res[0].Message, "timed out") {
		t.Fatalf("gate = %+v", res[0])
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Fatalf("took %s; the limit did not stop the programs", d)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func hookWF(hooks ...Hook) *Workflow {
	return &Workflow{States: map[string]State{"a": {}, "done": {Final: true}}, Hooks: hooks}
}

func TestRunHooksPerEvent(t *testing.T) {
	r, exe, out := helperRunner(t)
	h := func(name string, on ...string) Hook { return Hook{Name: name, On: on, Run: []string{exe, name}} }
	w := hookWF(h("cnt", "kind:count"), h("all", "kind:*"), h("fin", "final"),
		h("both", "enter:done", "final"), h("tr", "transition"), h("q", "kind:*"))
	w.Hooks[5].Mode = ModeQueued
	task := store.Task{ID: "01X", Short: "t-1", State: "done"}
	tev, _ := store.NewEvent(store.KindTransition, store.TransitionData{From: "a", To: "done"})
	cev, _ := store.NewEvent(store.KindCount, store.CountData{Counter: "n", By: 1})
	for _, ev := range []store.Event{tev, cev} {
		if errs := w.RunHooks(ctx, r, ev, task, ""); errs != nil {
			t.Fatal(errs)
		}
	}
	var got []string
	for _, rec := range records(t, out) {
		got = append(got, rec.Args[0])
	}
	if want := "all fin both tr cnt all"; strings.Join(got, " ") != want {
		t.Fatalf("ran %v, want %s", got, want)
	}
	// Not final: final does not fire.
	task.State = "a"
	tev2, _ := store.NewEvent(store.KindTransition, store.TransitionData{From: "done", To: "a"})
	_ = os.Remove(out)
	if errs := w.RunHooks(ctx, r, tev2, task, ""); errs != nil {
		t.Fatal(errs)
	}
	got = nil
	for _, rec := range records(t, out) {
		got = append(got, rec.Args[0])
	}
	if want := "all tr"; strings.Join(got, " ") != want {
		t.Fatalf("ran %v, want %s", got, want)
	}
}

func TestRunHooksStateAndRepoNote(t *testing.T) {
	r, exe, out := helperRunner(t)
	w := hookWF(Hook{Name: "n", On: StateList{"kind:note"}, Run: []string{exe, "{state}"}})
	task := store.Task{ID: "01X", Short: "t-1", State: "a"}
	note, _ := store.NewEvent(store.KindNote, store.NoteData{Text: "x"})
	if errs := w.RunHooks(ctx, r, note, task, ""); errs != nil {
		t.Fatal(errs)
	}
	if errs := w.RunRepoHooks(ctx, r, note); errs != nil {
		t.Fatal(errs)
	}
	recs := records(t, out)
	if len(recs) != 2 || recs[0].Args[0] != "a" || recs[1].Args[0] != "" {
		t.Fatalf("records = %+v", recs)
	}
	var in map[string]json.RawMessage
	if err := json.Unmarshal([]byte(recs[0].Stdin), &in); err != nil || string(in["format"]) != "1" || string(in["effective"]) != `"a"` {
		t.Fatalf("task stdin = %s (%v)", recs[0].Stdin, err)
	}
	var v TaskView
	if err := json.Unmarshal(in["task"], &v); err != nil || v.Short != "t-1" {
		t.Fatalf("task = %s", in["task"])
	}
	in = nil
	if err := json.Unmarshal([]byte(recs[1].Stdin), &in); err != nil || string(in["task"]) != "null" {
		t.Fatalf("repo stdin = %s (%v)", recs[1].Stdin, err)
	}
	if _, ok := in["effective"]; ok {
		t.Fatalf("repo stdin has effective: %s", recs[1].Stdin)
	}
	for _, e := range recs[1].Env {
		if strings.HasPrefix(e, "RIG_HOOK_TASK=") {
			t.Fatalf("repo note has %s", e)
		}
	}
}

func TestHookEnvironment(t *testing.T) {
	t.Setenv("GH_TOKEN", "secret")
	t.Setenv("FOO_BAR", "1")
	t.Setenv("RIG_ACTOR", "someone")
	r, exe, out := helperRunner(t)
	w := hookWF(
		Hook{Name: "plain", On: StateList{"kind:note"}, Run: []string{exe}},
		Hook{Name: "wide", On: StateList{"kind:note"}, Run: []string{exe}, Env: []string{"FOO_*"}})
	task := store.Task{ID: "01X", Short: "t-1", State: "a"}
	note, _ := store.NewEvent(store.KindNote, store.NoteData{Text: "x"})
	if errs := w.RunHooks(ctx, r, note, task, ""); errs != nil {
		t.Fatal(errs)
	}
	recs := records(t, out)
	has := func(env []string, prefix string) string {
		for _, e := range env {
			if strings.HasPrefix(e, prefix) {
				return e
			}
		}
		return ""
	}
	if has(recs[0].Env, "PATH=") == "" || has(recs[0].Env, "GH_TOKEN=") != "" || has(recs[0].Env, "FOO_BAR=") != "" {
		t.Fatalf("plain env = %v", recs[0].Env)
	}
	if has(recs[1].Env, "FOO_BAR=") == "" || has(recs[1].Env, "GH_TOKEN=") != "" {
		t.Fatalf("wide env = %v", recs[1].Env)
	}
	for i, name := range []string{"plain", "wide"} {
		env := recs[i].Env
		if has(env, "RIG_HOOK_NAME=") != "RIG_HOOK_NAME="+name || has(env, "RIG_HOOK_EVENT=") != "RIG_HOOK_EVENT="+note.ID ||
			has(env, "RIG_HOOK_TASK=") != "RIG_HOOK_TASK=01X" || has(env, "RIG_ACTOR=") != "RIG_ACTOR=hook:"+name {
			t.Fatalf("%s identity env = %v", name, env)
		}
	}
}

func TestHookTimeoutAndQueued(t *testing.T) {
	r, exe, out := helperRunner(t, "RIG_WF_SLEEP=10")
	w := hookWF(
		Hook{Name: "slow", On: StateList{"kind:note"}, Run: []string{exe}, Timeout: "1s"},
		Hook{Name: "later", On: StateList{"kind:note"}, Run: []string{exe, "queued"}, Mode: ModeQueued})
	note, _ := store.NewEvent(store.KindNote, store.NoteData{Text: "x"})
	start := time.Now()
	errs := w.RunHooks(ctx, r, note, store.Task{ID: "1"}, "")
	if len(errs) != 1 || !errors.Is(errs[0], proc.ErrTimeout) {
		t.Fatalf("errs = %v", errs)
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Fatalf("took %s", d)
	}
	for _, rec := range records(t, out) {
		if len(rec.Args) > 0 && rec.Args[0] == "queued" {
			t.Fatal("queued hook ran inline")
		}
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/store/memstore"
)

// wfApp is an app on a memstore whose RIG_WORKFLOW is wf, with its output
// captured. cwd is the directory relative file arguments resolve against.
func wfApp(t *testing.T, st store.Store, wf, cwd string) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	repo, _ := newRepo(t)
	if cwd == "" {
		cwd = repo
	}
	var out, errOut bytes.Buffer
	a := &app{
		stdout: &out, stderr: &errOut, cwd: cwd,
		getenv: func(k string) string {
			if k == "RIG_WORKFLOW" {
				return wf
			}
			return ""
		},
		store: func(context.Context, string) (store.Store, error) { return st, nil },
	}
	return a, &out, &errOut
}

func writeWF(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWorkflowShowSource(t *testing.T) {
	st := memstore.New(store.Options{})
	type src struct {
		Preset string `json:"preset"`
		Path   string `json:"path"`
		Env    bool   `json:"env"`
	}
	show := func(wf string) (src, map[string]any, string) {
		t.Helper()
		a, out, errOut := wfApp(t, st, wf, "")
		if code := a.run(ctx, []string{"workflow", "show", "--json"}); code != 0 {
			t.Fatalf("show %q: exit %d: %s", wf, code, errOut)
		}
		var got struct {
			Source   src            `json:"source"`
			Workflow map[string]any `json:"workflow"`
		}
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return got.Source, got.Workflow, out.String()
	}

	s, wf, _ := show("basic")
	if s.Preset != "basic" || s.Path != "" || !s.Env {
		t.Fatalf("preset source = %+v", s)
	}
	if wf["name"] != "basic" || wf["initial"] == nil || wf["states"] == nil || wf["transitions"] == nil {
		t.Fatalf("workflow = %v", wf)
	}

	file := testWorkflow(t)
	s, wf, _ = show(file)
	if s.Path != file || s.Preset != "" || wf["name"] != "t" {
		t.Fatalf("file source = %+v, %v", s, wf["name"])
	}

	// Text output names name, version, source, states and gates.
	a, out, _ := wfApp(t, st, file, "")
	if code := a.run(ctx, []string{"workflow", "show"}); code != 0 {
		t.Fatalf("text show: exit %d", code)
	}
	for _, want := range []string{"name: t", "version: 1", "source: " + file + " (from RIG_WORKFLOW)", "bound  action=finish who=agent final=false shaBound=true", "done  action=- who=agent final=true", "shaIsHead"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("text show lacks %q:\n%s", want, out)
		}
	}

	// Without RIG_WORKFLOW the default preset shows, not from the env.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	a, out, _ = wfApp(t, st, "", "")
	if code := a.run(ctx, []string{"workflow", "show", "--json"}); code != 0 {
		t.Fatalf("default show: exit %d", code)
	}
	if strings.Contains(out.String(), `"env"`) || !strings.Contains(out.String(), `"preset": "basic"`) {
		t.Fatalf("default source: %s", out)
	}
}

func TestWorkflowUnknownFlagsAndSubcommands(t *testing.T) {
	st := memstore.New(store.Options{})
	for _, args := range [][]string{
		{"workflow"}, {"workflow", "frob"}, {"workflow", "show", "--bogus"},
		{"workflow", "validate", "--bogus"}, {"workflow", "validate", "a", "b"}, {"workflow", "show", "x"},
	} {
		a, _, _ := wfApp(t, st, "basic", "")
		if code := a.run(ctx, args); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

type validateOut struct {
	Valid    bool `json:"valid"`
	Problems []struct {
		Path    string `json:"path"`
		Message string `json:"message"`
	} `json:"problems"`
	UnknownStates []struct {
		Task  string `json:"task"`
		Short string `json:"short"`
		State string `json:"state"`
	} `json:"unknownStates"`
}

func TestWorkflowValidateExitCodes(t *testing.T) {
	dir := t.TempDir()
	good := testWorkflow(t)
	bad := writeWF(t, dir, "bad.json", `{"name":"bad","version":1,"initial":"nowhere","states":{"a":{}},"transitions":[]}`)
	notJSON := writeWF(t, dir, "nj.json", `this is not json`)
	st := memstore.New(store.Options{})

	run := func(wf, cwd string, args ...string) (int, string, string) {
		a, out, errOut := wfApp(t, st, wf, cwd)
		code := a.run(ctx, append([]string{"workflow", "validate"}, args...))
		return code, out.String(), errOut.String()
	}

	// 0: the file given, and the one in effect.
	if code, out, _ := run("basic", "", good); code != 0 || !strings.Contains(out, "valid") {
		t.Fatalf("good file: %d %s", code, out)
	}
	if code, _, _ := run(good, ""); code != 0 {
		t.Fatalf("in effect: %d", code)
	}

	// A relative path resolves against the cwd.
	if code, _, e := run("basic", dir, "bad.json"); code != 1 {
		t.Fatalf("relative bad file: %d %s", code, e)
	}

	// 1: invalid, with problems as path: message.
	code, out, _ := run("basic", "", bad, "--json")
	if code != 1 {
		t.Fatalf("bad file: exit %d", code)
	}
	var v validateOut
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if v.Valid || len(v.Problems) == 0 || v.Problems[0].Path == "" || v.Problems[0].Message == "" || v.UnknownStates == nil {
		t.Fatalf("bad file json = %+v", v)
	}
	if code, out, _ := run("basic", "", bad); code != 1 || !strings.Contains(out, "initial: ") {
		t.Fatalf("bad file text: %d %s", code, out)
	}
	// An invalid workflow in effect is also exit 1, not 2.
	if code, _, _ := run(bad, ""); code != 1 {
		t.Fatalf("invalid in effect: %d", code)
	}

	// 2: unreadable, not JSON.
	if code, _, e := run("basic", "", filepath.Join(dir, "missing.json")); code != 2 || e == "" {
		t.Fatalf("missing: %d %s", code, e)
	}
	if code, _, _ := run("basic", "", notJSON); code != 2 {
		t.Fatalf("not json: %d", code)
	}
	if code, _, _ := run(notJSON, ""); code != 2 {
		t.Fatalf("not json in effect: %d", code)
	}
}

func TestWorkflowValidateUnknownStates(t *testing.T) {
	dir := t.TempDir()
	st := memstore.New(store.Options{Now: tick()})
	task, err := st.CreateTask(ctx, store.NewTask{Slug: "s", Title: "S", Initial: "ghost", Workflow: store.WorkflowRef{Name: "old", Version: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateTask(ctx, store.NewTask{Slug: "ok", Title: "OK", Initial: "open", Workflow: store.WorkflowRef{Name: "t", Version: 1}}); err != nil {
		t.Fatal(err)
	}
	good := testWorkflow(t)

	a, out, _ := wfApp(t, st, good, dir)
	code := a.run(ctx, []string{"workflow", "validate", "--json"})
	var v validateOut
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// An unknown state alone does not make the workflow invalid.
	if code != 0 || !v.Valid || len(v.Problems) != 0 || v.Problems == nil {
		t.Fatalf("exit %d, %+v", code, v)
	}
	if len(v.UnknownStates) != 1 || v.UnknownStates[0].Task != task.ID || v.UnknownStates[0].Short != task.Short || v.UnknownStates[0].State != "ghost" {
		t.Fatalf("unknownStates = %+v", v.UnknownStates)
	}

	a, out, _ = wfApp(t, st, good, dir)
	if code := a.run(ctx, []string{"workflow", "validate"}); code != 0 || !strings.Contains(out.String(), `"ghost"`) || !strings.Contains(out.String(), task.Short) {
		t.Fatalf("text: %d %s", code, out)
	}
}

// TestWorkflowEndToEnd runs both subcommands on the gitref store in a
// throwaway repository, under the review-loop workflow.
func TestWorkflowEndToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	repo, _ := newRepo(t)
	fixture, err := filepath.Abs(filepath.Join("..", "..", "internal", "workflow", "testdata", "review-loop.json"))
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"RIG_WORKFLOW": fixture}

	r := rig(t, repo, nil, env, "workflow", "show", "--json")
	if r.code != 0 {
		t.Fatalf("show: %+v", r)
	}
	var shown struct {
		Source   struct{ Path string }
		Workflow struct{ Name string }
	}
	if err := json.Unmarshal([]byte(r.out), &shown); err != nil || shown.Source.Path != fixture || shown.Workflow.Name == "" {
		t.Fatalf("show json: %v %s", err, r.out)
	}
	r = rig(t, repo, nil, env, "workflow", "validate", "--json")
	var v validateOut
	if r.code != 0 || json.Unmarshal([]byte(r.out), &v) != nil || !v.Valid || len(v.UnknownStates) != 0 {
		t.Fatalf("validate: %+v", r)
	}
	r = rig(t, repo, nil, env, "workflow", "validate", fixture)
	if r.code != 0 {
		t.Fatalf("validate file: %+v", r)
	}
}

func TestWorkflowValidateFileOutsideRepo(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	root := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(root))
	dir := filepath.Join(root, "plain")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	good := testWorkflow(t)
	bad := writeWF(t, dir, "bad.json", `{"name":"bad","version":1,"initial":"nowhere","states":{"a":{}},"transitions":[]}`)
	run := func(file string) (int, string) {
		var out, errOut bytes.Buffer
		// Built as main builds it, with the real store opener.
		a := &app{stdout: &out, stderr: &errOut, cwd: dir, getenv: func(string) string { return "" }}
		a.store = a.openStore
		return a.run(ctx, []string{"workflow", "validate", "--json", file}), out.String() + errOut.String()
	}
	code, out := run(good)
	var v validateOut
	if code != 0 || json.Unmarshal([]byte(out), &v) != nil || !v.Valid || len(v.UnknownStates) != 0 {
		t.Fatalf("valid file outside repo: %d %s", code, out)
	}
	code, out = run(bad)
	v = validateOut{}
	if code != 1 || json.Unmarshal([]byte(out), &v) != nil || v.Valid || len(v.Problems) == 0 {
		t.Fatalf("invalid file outside repo: %d %s", code, out)
	}
}

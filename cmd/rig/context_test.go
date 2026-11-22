package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/git"
	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/store/gitref"
	"github.com/AindriuB/rig/internal/store/memstore"
)

func loopWorkflow(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "internal", "workflow", "testdata", "claude-kit-loop.json"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// ctxRepo is a repository with tracked files under src/ and a binary file.
func ctxRepo(t *testing.T) string {
	t.Helper()
	repo, _ := newRepo(t)
	write := func(name string, data []byte) {
		p := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/a.go", []byte("package a\n\nSECRET-CONTENT\n"))
	write("src/b.go", []byte("package b"))
	write("src/blob.bin", []byte{0, 1, 2, '\n'})
	write("other/c.go", []byte("package c\n"))
	gitc(t, repo, "add", ".")
	gitc(t, repo, "commit", "-q", "-m", "add sources")
	write("src/a.go", []byte("package a\n\nSECRET-CONTENT\nmore\n"))
	gitc(t, repo, "commit", "-q", "-a", "-m", "grow a")
	return repo
}

// ctxApp runs against the claude-kit-loop workflow, with out and err captured.
func ctxApp(t *testing.T, repo string, st store.Store) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errOut bytes.Buffer
	wf := loopWorkflow(t)
	a := &app{
		stdout: &out, stderr: &errOut, cwd: repo,
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

func ctxTask(t *testing.T, st store.Store, slug, body string, owns ...string) store.Task {
	t.Helper()
	fields := map[string]any{"acceptance": []any{"it works"}}
	if owns != nil {
		l := make([]any, len(owns))
		for i, o := range owns {
			l[i] = o
		}
		fields["owns"] = l
	}
	task, err := st.CreateTask(ctx, store.NewTask{
		Slug: slug, Title: "Title of " + slug, Body: body, Fields: fields,
		Initial: "planned", Workflow: store.WorkflowRef{Name: "claude-kit-loop", Version: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func ctxEvent(t *testing.T, st store.Store, ref, kind string, data any) {
	t.Helper()
	ev, err := store.NewEvent(kind, data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Update(ctx, ref, func(store.Task, []store.Finding) ([]store.Event, error) {
		return []store.Event{ev}, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func ctxRun(t *testing.T, a *app, out *bytes.Buffer, args ...string) (int, string) {
	t.Helper()
	out.Reset()
	code := a.context(ctx, args)
	return code, out.String()
}

// AC1: flags, budget range, exit codes.
func TestContextFlags(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	task := ctxTask(t, st, "alpha", "body", "src/*.go")
	a, out, errOut := ctxApp(t, ctxRepo(t), st)
	for _, args := range [][]string{
		{task.Short, "--bogus"},
		{task.Short, "--budget", "199"},
		{task.Short, "--budget=20001"},
		{task.Short, "--budget", "lots"},
		{task.Short, "--budget"},
		{},
		{task.Short, "extra"},
		{"t-nope"},
	} {
		errOut.Reset()
		if code, _ := ctxRun(t, a, out, args...); code != 2 || errOut.Len() == 0 {
			t.Errorf("%v: exit %d, stderr %q; want 2 with a message", args, code, errOut)
		}
	}
	for _, b := range []string{"200", "20000"} {
		if code, _ := ctxRun(t, a, out, task.Short, "--budget", b); code != 0 {
			t.Errorf("--budget %s: exit %d", b, code)
		}
	}
}

// AC4: the --json field names, from docs/phase-2.md.
func TestContextJSONShape(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	task := ctxTask(t, st, "alpha", "do the thing", "src/*.go")
	a, out, _ := ctxApp(t, ctxRepo(t), st)
	code, text := ctxRun(t, a, out, "alpha", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &top); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(top))
	for k := range top {
		keys = append(keys, k)
	}
	if want := []string{"budget", "overBudget", "sections", "task", "used"}; !sameSet(keys, want) {
		t.Fatalf("top-level keys %v, want %v", keys, want)
	}
	var got struct {
		Task       string
		Budget     int
		Used       int
		OverBudget bool
		Sections   []map[string]json.RawMessage
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	if got.Task != task.Short || got.Budget != 1500 || got.OverBudget || got.Used <= 0 || got.Used > 1500 {
		t.Errorf("got %+v", got)
	}
	var names []string
	for _, s := range got.Sections {
		var n string
		_ = json.Unmarshal(s["name"], &n)
		names = append(names, n)
		ks := make([]string, 0, 3)
		for k := range s {
			ks = append(ks, k)
		}
		if !sameSet(ks, []string{"name", "items", "omitted"}) {
			t.Errorf("section %s keys %v", n, ks)
		}
		if string(s["items"]) == "null" {
			t.Errorf("section %s items is null, want []", n)
		}
	}
	if want := []string{"Header", "Body", "Findings", "Paths", "Overlap", "History", "Commits"}; !reflect.DeepEqual(names, want) {
		t.Errorf("sections %v, want %v", names, want)
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]bool{}
	for _, s := range a {
		m[s] = true
	}
	for _, s := range b {
		if !m[s] {
			return false
		}
	}
	return true
}

// AC2: every section's contents, in order, with no file contents.
func TestContextSections(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	task := ctxTask(t, st, "alpha", "do the thing\nsecond line", "src/*.go", "src/blob.bin")
	ctxEvent(t, st, task.ID, store.KindFinding, store.FindingData{Severity: "P3", Summary: "minor", Path: "src/a.go"})
	ctxEvent(t, st, task.ID, store.KindFinding, store.FindingData{Severity: "P1", Summary: "major"})
	ctxEvent(t, st, task.ID, store.KindFinding, store.FindingData{Severity: "NOTE", Summary: "fyi"})
	// Another open task on src/a.go, and a merged one with closed findings.
	other := ctxTask(t, st, "beta", "", "src/a.go")
	done := ctxTask(t, st, "gamma", "", "src/*.go")
	ctxEvent(t, st, done.ID, store.KindFinding, store.FindingData{Severity: "P2", Summary: "old issue", Path: "src/a.go"})
	fs, _ := st.Findings(ctx, done.ID)
	ctxEvent(t, st, done.ID, store.KindFindingStatus, store.FindingStatusData{Finding: fs[0].ID, Status: store.StatusFixed})
	ctxEvent(t, st, done.ID, store.KindFinding, store.FindingData{Severity: "P2", Summary: "other file", Path: "other/c.go"})
	fs, _ = st.Findings(ctx, done.ID)
	ctxEvent(t, st, done.ID, store.KindFindingStatus, store.FindingStatusData{Finding: fs[1].ID, Status: store.StatusFixed})
	if _, err := st.AddNote(ctx, store.Note{Task: done.ID, Kind: "decision", Text: "kept it\nsimple"}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"implementing", "reviewed", "verified", "pr_open", "ci_green", "ready", "merged"} {
		ctxEvent(t, st, done.ID, store.KindTransition, store.TransitionData{From: "x", To: s})
	}

	a, out, _ := ctxApp(t, ctxRepo(t), st)
	code, text := ctxRun(t, a, out, "alpha")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(text, "SECRET-CONTENT") {
		t.Fatal("file contents leaked")
	}
	f := func(s string) string { // the finding's short id
		fs, _ := st.Findings(ctx, task.ID)
		for _, x := range fs {
			if x.Summary == s {
				return x.Short
			}
		}
		t.Fatalf("no finding %q", s)
		return ""
	}
	want := "## Header\n" +
		task.Short + " alpha: Title of alpha\n" +
		"state: planned  action: implement  who: agent\n" +
		"branch: task/" + task.Short + "-alpha\n" +
		"counters: openFindings=3\n" +
		"## Body\n" +
		"do the thing\nsecond line\n" +
		"acceptance: it works\n" +
		"owns: src/*.go, src/blob.bin\n" +
		"## Findings\n" +
		f("major") + " P1 major\n" +
		f("minor") + " P3 minor (src/a.go)\n" +
		f("fyi") + " NOTE fyi\n" +
		"## Paths\n" +
		"src/a.go (4 lines)\n" +
		"src/b.go (1 lines)\n" +
		"src/blob.bin (0 lines)\n" +
		"## Overlap\n" +
		other.Short + " beta [planned] src/a.go\n" +
		"## History\n" +
		done.Short + " note " + noteShort(t, st, done.ID) + " decision: kept it simple\n" +
		done.Short + " finding " + findingShort(t, st, done.ID, "old issue") + " P2 fixed: old issue\n" +
		"## Commits\n" +
		gitc(t, a.cwd, "log", "-1", "--format=%h", "--", "src/a.go")[:7] + " grow a\n" +
		gitc(t, a.cwd, "log", "-1", "--format=%h", "--", "src/b.go")[:7] + " add sources\n"
	if text != want {
		t.Errorf("output:\n%s\nwant:\n%s", text, want)
	}
	// The same inputs give the same bytes.
	if _, again := ctxRun(t, a, out, "alpha"); again != text {
		t.Error("second run differs")
	}
}

func noteShort(t *testing.T, st store.Store, id string) string {
	t.Helper()
	ns, err := st.Notes(ctx, store.NoteFilter{Task: id})
	if err != nil || len(ns) != 1 {
		t.Fatal(ns, err)
	}
	return ns[0].Short
}

func findingShort(t *testing.T, st store.Store, id, summary string) string {
	t.Helper()
	fs, _ := st.Findings(ctx, id)
	for _, f := range fs {
		if f.Summary == summary {
			return f.Short
		}
	}
	t.Fatalf("no finding %q", summary)
	return ""
}

// AC3: a budget cuts sections with "… n more"; used stays within it; sections
// 1 and 2 over the budget print in full and set overBudget.
func TestContextBudget(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	task := ctxTask(t, st, "alpha", "short body", "src/*.go")
	for i := 0; i < 60; i++ {
		ctxEvent(t, st, task.ID, store.KindFinding, store.FindingData{Severity: "P2", Summary: strings.Repeat("word ", 10) + string(rune('a'+i%26)) + string(rune('a'+i/26))})
	}
	a, out, _ := ctxApp(t, ctxRepo(t), st)
	_, full := ctxRun(t, a, out, task.Short, "--budget", "20000")
	if strings.Contains(full, "more\n") {
		t.Fatal("nothing should be cut at 20000")
	}
	code, text := ctxRun(t, a, out, task.Short, "--budget", "200")
	if code != 0 || !strings.Contains(text, "… ") || !strings.HasSuffix(strings.Split(text, "## Paths")[0], " more\n") {
		t.Fatalf("budget 200:\n%s", text)
	}
	if n := (len(text) + 3) / 4; n > 200 {
		t.Errorf("printed %d tokens over a budget of 200", n)
	}
	_, js := ctxRun(t, a, out, task.Short, "--budget", "200", "--json")
	var o ctxOut
	if err := json.Unmarshal([]byte(js), &o); err != nil {
		t.Fatal(err)
	}
	if o.OverBudget || o.Used != (len(text)+3)/4 {
		t.Errorf("overBudget %v used %d; text is %d bytes", o.OverBudget, o.Used, len(text))
	}
	findings := o.Sections[2]
	if findings.Omitted == 0 || findings.Omitted+len(findings.Items) != 60 {
		t.Errorf("findings: %d shown, %d omitted", len(findings.Items), findings.Omitted)
	}

	big := ctxTask(t, st, "huge", strings.Repeat("0123456789abcdef\n", 100), "src/*.go")
	ctxEvent(t, st, big.ID, store.KindFinding, store.FindingData{Severity: "P1", Summary: "cannot fit"})
	code, text = ctxRun(t, a, out, big.Short, "--budget", "200")
	if code != 0 || !strings.Contains(text, strings.Repeat("0123456789abcdef\n", 100)) || !strings.Contains(text, "huge") {
		t.Fatalf("sections 1 and 2 must print in full:\n%s", text)
	}
	if strings.Contains(text, "## Findings") {
		t.Error("later sections must not print when 1 and 2 pass the budget")
	}
	_, js = ctxRun(t, a, out, big.Short, "--budget", "200", "--json")
	o = ctxOut{}
	if err := json.Unmarshal([]byte(js), &o); err != nil {
		t.Fatal(err)
	}
	if !o.OverBudget || o.Used != (len(text)+3)/4 || o.Used <= 200 {
		t.Errorf("overBudget %v used %d", o.OverBudget, o.Used)
	}
}

// A workflow with no owns field gets sections 1 to 3.
func TestContextNoOwnsField(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	task := newTask(t, st)
	a, _ := workApp(t, st)
	var buf bytes.Buffer
	a.stdout = &buf
	if code := a.context(ctx, []string{"--json", task.Short}); code != 0 {
		t.Fatal(code)
	}
	var o ctxOut
	if err := json.Unmarshal(buf.Bytes(), &o); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range o.Sections {
		names = append(names, s.Name)
	}
	if want := []string{"Header", "Body", "Findings"}; !reflect.DeepEqual(names, want) {
		t.Errorf("sections %v, want %v", names, want)
	}
}

// End to end on the git-ref store in a throwaway repository.
func TestContextGitref(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	repo := ctxRepo(t)
	gs, err := gitref.Open(git.Repo{Dir: repo}, store.Options{Actor: "t"})
	if err != nil {
		t.Fatal(err)
	}
	task := ctxTask(t, gs, "alpha", "hello", "src/*.go")
	r := rig(t, repo, nil, map[string]string{"RIG_WORKFLOW": loopWorkflow(t)}, "context", "alpha", "--json")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	var o ctxOut
	if err := json.Unmarshal([]byte(r.out), &o); err != nil {
		t.Fatal(err)
	}
	if o.Task != task.Short || len(o.Sections) != 7 || o.Sections[3].Name != "Paths" || len(o.Sections[3].Items) != 2 {
		t.Errorf("%+v", o)
	}
	if strings.Contains(r.out, "SECRET-CONTENT") {
		t.Error("file contents leaked")
	}
}

// Run from a subdirectory, the brief is the same as from the top.
func TestContextFromSubdirectory(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	ctxTask(t, st, "alpha", "body", "src/*.go")
	ctxTask(t, st, "beta", "", "src/a.go")
	repo := ctxRepo(t)
	a, out, _ := ctxApp(t, repo, st)
	code, top := ctxRun(t, a, out, "alpha")
	if code != 0 || !strings.Contains(top, "src/a.go (4 lines)") || !strings.Contains(top, "grow a") {
		t.Fatalf("top: %d\n%s", code, top)
	}
	a.cwd = filepath.Join(repo, "src")
	if code, sub := ctxRun(t, a, out, "alpha"); code != 0 || sub != top {
		t.Errorf("from src/: exit %d\n%s\nwant\n%s", code, sub, top)
	}
}

// Owned paths outside the repository are not matched, and another task's do
// not fail the brief.
func TestContextPathsOutsideRepo(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	ctxTask(t, st, "alpha", "body", "src/*.go", "../x", "/etc/passwd")
	ctxTask(t, st, "beta", "", "../x", "/etc/passwd", "src/a.go")
	a, out, _ := ctxApp(t, ctxRepo(t), st)
	code, text := ctxRun(t, a, out, "alpha")
	if code != 0 || !strings.Contains(text, "src/a.go (4 lines)") || !strings.Contains(text, "[planned] src/a.go") {
		t.Fatalf("exit %d\n%s", code, text)
	}
	if strings.Contains(text, "passwd (") {
		t.Error("an absolute path was matched")
	}
}

// A finding recorded with a path:line still matches the owned file.
func TestContextHistoryPathWithLine(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	ctxTask(t, st, "alpha", "", "src/a.go")
	done := ctxTask(t, st, "gamma", "", "src/*.go")
	ctxEvent(t, st, done.ID, store.KindFinding, store.FindingData{Severity: "P2", Summary: "lined", Path: "src/a.go:12"})
	fs, _ := st.Findings(ctx, done.ID)
	ctxEvent(t, st, done.ID, store.KindFindingStatus, store.FindingStatusData{Finding: fs[0].ID, Status: store.StatusFixed})
	a, out, _ := ctxApp(t, ctxRepo(t), st)
	code, text := ctxRun(t, a, out, "alpha")
	if code != 0 || !strings.Contains(text, "P2 fixed: lined") {
		t.Fatalf("exit %d, history missing the finding:\n%s", code, text)
	}
}

// With no local default branch the brief falls back to origin/<branch>.
func TestContextNoLocalDefaultBranch(t *testing.T) {
	repo := ctxRepo(t)
	gitc(t, repo, "push", "-q", "origin", "main")
	gitc(t, repo, "fetch", "-q", "origin")
	gitc(t, repo, "remote", "set-head", "origin", "main")
	gitc(t, repo, "checkout", "-q", "-b", "task/x")
	gitc(t, repo, "branch", "-q", "-D", "main")
	st := memstore.New(store.Options{Now: tick()})
	ctxTask(t, st, "alpha", "", "src/a.go")
	a, out, errOut := ctxApp(t, repo, st)
	code, text := ctxRun(t, a, out, "alpha")
	if code != 0 || !strings.Contains(text, " grow a\n") {
		t.Fatalf("exit %d (%s):\n%s", code, errOut, text)
	}
	// Neither a local nor a remote ref: Commits is empty, the brief still prints.
	gitc(t, repo, "update-ref", "-d", "refs/remotes/origin/main")
	if code, text := ctxRun(t, a, out, "alpha"); code != 0 || strings.Contains(text, "grow a") {
		t.Fatalf("exit %d:\n%s", code, text)
	}
}

// A wide owns glob must reach git log as the glob pathspecs, not as one
// argument per matched file.
func TestContextCommitsPassGlobsToLog(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell shim for git")
	}
	repo := ctxRepo(t)
	for i := 0; i < 200; i++ {
		p := filepath.Join(repo, "wide", fmt.Sprintf("file-%03d.txt", i))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitc(t, repo, "add", ".")
	gitc(t, repo, "commit", "-q", "-m", "add wide")

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	shim := t.TempDir()
	logFile := filepath.Join(shim, "log-args")
	script := "#!/bin/sh\n" +
		"case \" $* \" in *\" log \"*) echo $# >> '" + logFile + "';; esac\n" +
		"exec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))

	st := memstore.New(store.Options{Now: tick()})
	ctxTask(t, st, "alpha", "", "wide/**", "src/a.go")
	a, out, errOut := ctxApp(t, repo, st)
	code, text := ctxRun(t, a, out, "alpha")
	if code != 0 || !strings.Contains(text, " add wide\n") || !strings.Contains(text, " grow a\n") {
		t.Fatalf("exit %d (%s):\n%s", code, errOut, text)
	}
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("git log was not run: %v", err)
	}
	for _, f := range strings.Fields(string(data)) {
		if n, _ := strconv.Atoi(f); n > 20 {
			t.Errorf("git log got %d args; want the globs, not one per file", n)
		}
	}
}

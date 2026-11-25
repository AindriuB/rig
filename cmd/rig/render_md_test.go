package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/store/memstore"
	"github.com/AindriuB/rig/internal/workflow"
)

func goldenEq(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

// AC2: each alias, the conflicting and unknown cases, and the default.
func TestFormatFlags(t *testing.T) {
	cases := []struct {
		args []string
		want outFormat
		err  bool
	}{
		{nil, formatText, false},
		{[]string{"--format", "text"}, formatText, false},
		{[]string{"--format=markdown"}, formatMarkdown, false},
		{[]string{"--format", "json"}, formatJSON, false},
		{[]string{"--json"}, formatJSON, false},
		{[]string{"--markdown"}, formatMarkdown, false},
		{[]string{"--json", "--format", "json"}, formatJSON, false},
		{[]string{"--markdown", "--format", "markdown"}, formatMarkdown, false},
		{[]string{"--json", "--markdown"}, 0, true},
		{[]string{"--json", "--format", "markdown"}, 0, true},
		{[]string{"--format", "text", "--json"}, 0, true},
		{[]string{"--format", "yaml"}, 0, true},
	}
	for _, c := range cases {
		fs := newFlags()
		ff := addFormatFlags(fs)
		if _, err := fs.parse(c.args); err != nil {
			t.Fatal(err)
		}
		got, err := ff.resolve()
		if (err != nil) != c.err || (!c.err && got != c.want) {
			t.Errorf("%v: got %v, %v", c.args, got, err)
		}
	}
}

// A bad format is exit 2 with a message, in all three commands.
func TestFormatFlagExitCodes(t *testing.T) {
	e := newTaskEnv(t)
	e.newTask("fx")
	for _, args := range [][]string{
		{"task", "show", "fx", "--format", "yaml"},
		{"task", "show", "fx", "--json", "--markdown"},
		{"task", "show", "fx", "--format", "json", "--format", "markdown"},
		{"task", "list", "--format", "markdown", "--format=json"},
		{"context", "fx", "--format=text", "--format", "json"},
		{"task", "list", "--format", "yaml"},
		{"task", "list", "--json", "--format", "markdown"},
		{"context", "fx", "--format", "yaml"},
		{"context", "fx", "--markdown", "--json"},
	} {
		r := e.rig(args...)
		if r.code != 2 || !strings.Contains(r.errOut, "format") {
			t.Errorf("%v: %+v", args, r)
		}
	}
	for _, args := range [][]string{
		{"task", "show", "fx", "--json", "--format", "json"},
		{"task", "list", "--format", "text"},
		{"task", "show", "fx", "--format", "json", "--format=json"},
		{"task", "show", "fx", "--markdown"},
		{"task", "list", "--markdown"},
	} {
		if r := e.rig(args...); r.code != 0 {
			t.Errorf("%v: %+v", args, r)
		}
	}
}

// AC3: a body is a blockquote, so nothing in it escapes into the document.
func TestBodyContainment(t *testing.T) {
	bodies := []string{
		"# Evil",
		"Evil\n===", "Also\n---",
		"```\n# Evil\ntext",
		"<!--\n# Evil", "<pre>\n# Evil",
		"<div>\n```\n</div>\n\n# Evil",
		"- ```\n  x\n  ```\n\n# Evil",
		"intro\r# Evil\r",
	}
	// check asserts the lines from "## Body" up to next are all quoted and
	// that next, and the heading after it, stand at column 0.
	check := func(t *testing.T, md, next string) {
		t.Helper()
		lines := strings.Split(md, "\n")
		at := slices.Index(lines, "## Body")
		end := slices.Index(lines, next)
		if at < 0 || end < at {
			t.Fatalf("sections missing:\n%s", md)
		}
		for _, l := range lines[at+1 : end] {
			if l != "" && !strings.HasPrefix(l, ">") {
				t.Errorf("unquoted body line %q:\n%s", l, md)
			}
		}
		if strings.Contains(md, "\r") {
			t.Errorf("CR left in output: %q", md)
		}
	}
	for _, body := range bodies {
		v := fixedView()
		v.Body = body
		v.Fields = map[string]any{"owner": "ann"}
		check(t, renderShowMD(mdShow{View: v}), "## Fields")

		secs := []ctxSection{
			{Name: "Header", Items: []string{"abc slug: T"}},
			{Name: "Body", Items: strings.Split(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n"), "\n")},
			{Name: "Findings", Items: []string{"f1 P1 x"}},
		}
		check(t, renderContextMD(fitBudgetIn(markdownLayout, "abc", 20000, markdownSections(secs))), "## Findings")
	}
	if got := quoteBody("a\n\nb\r\nc\rd"); got != "> a\n>\n> b\n> c\n> d" {
		t.Errorf("quoteBody: %q", got)
	}
}

func fixedView() workflow.TaskView {
	return workflow.TaskView{
		ID: "01HZZZZZZZZZZZZZZZZZZZZZZZ", Short: "abc123", Slug: "slug", Title: "A title",
		State: "open", RecordedState: "open", Branch: "task/abc123-slug",
		Created: time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC),
		Updated: time.Date(2026, 11, 22, 11, 30, 0, 0, time.FixedZone("x", 3600)),
	}
}

// AC1: show, minimal and with every bullet.
func TestShowMarkdownGolden(t *testing.T) {
	goldenEq(t, "minimal", renderShowMD(mdShow{View: fixedView()}), `# A title

- id: 01HZZZZZZZZZZZZZZZZZZZZZZZ
- slug: slug
- state: open
- recorded state: open
- branch: task/abc123-slug
- created: 2026-11-22T10:00:00Z
- updated: 2026-11-22T10:30:00Z
`)

	v := fixedView()
	v.Title = "Two\nlines"
	v.State = "stopped"
	v.PR = 7
	v.HeadSha = "0123456789abcdef0123456789abcdef01234567"
	v.Reason = "blocked\non it"
	v.Parent = "01HPARENT"
	v.External = &workflow.ExternalView{Kind: "jira", Key: "ENG-1", URL: "https://x.example/ENG-1", Owned: []string{"title", "body"}, Drift: []string{"title"}}
	v.Body = "intro\n\n# Heading\n```\n# code\n```\n"
	v.Fields = map[string]any{"owner": "ann", "tags": []any{"b", "a"}}
	v.Counters = map[string]int{"rounds": 2, "a": 1}
	goldenEq(t, "full", renderShowMD(mdShow{View: v, ParentShort: "par123"}), `# Two lines

- id: 01HZZZZZZZZZZZZZZZZZZZZZZZ
- slug: slug
- state: stopped
- recorded state: open
- branch: task/abc123-slug
- pr: #7
- head: 0123456789abcdef0123456789abcdef01234567
- reason: blocked on it
- parent: par123
- external: jira ENG-1 https://x.example/ENG-1 owned: title, body drift: title
- created: 2026-11-22T10:00:00Z
- updated: 2026-11-22T10:30:00Z

## Body

> intro
>
> # Heading
> `+"```"+`
> # code
> `+"```"+`

## Fields

- owner: ann
- tags: `+renderValue([]any{"b", "a"})+`

## Counters

- a: 1
- rounds: 2
`)
}

// AC1: each requested section, and a requested-but-empty one prints none.
func TestShowMarkdownSections(t *testing.T) {
	empty := renderShowMD(mdShow{View: fixedView(), ShowFind: true, ShowNotes: true, ShowHistory: true})
	if !strings.HasSuffix(empty, "\n## Findings\n\nnone\n\n## Notes\n\nnone\n\n## History\n\nnone\n") {
		t.Errorf("empty sections:\n%s", empty)
	}
	at := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	got := renderShowMD(mdShow{
		View: fixedView(), ShowFind: true, ShowNotes: true, ShowHistory: true,
		Findings: []store.Finding{
			{Short: "f1", Severity: "P1", Status: "open", Summary: "bad\nthing", Path: "a.go:3"},
			{Short: "f2", Severity: "P2", Status: "fixed", Summary: "meh"},
		},
		Notes:   []store.Note{{Created: at, Kind: "decision", Text: "chose\nX"}},
		History: []mdHistory{{Time: at, From: "open", To: "bound", Sha: "0123456789abcdef0123", PR: 3, Reason: "r", Actor: "ann"}},
	})
	i := strings.Index(got, "\n## Findings")
	goldenEq(t, "sections", got[i:], `
## Findings

- f1 [P1] open: bad thing (a.go:3)
- f2 [P2] fixed: meh

## Notes

- 2026-11-22T12:00:00Z [decision] chose X

## History

- 2026-11-22T12:00:00Z open -> bound sha 0123456789ab pr #3 reason: r (ann)
`)
}

// AC1: list, empty, linked and parented.
func TestListMarkdownGolden(t *testing.T) {
	goldenEq(t, "empty", renderListMD(nil, nil), "# Tasks\n\nnone\n")
	a := workflow.TaskView{Short: "aaa111", State: "open", Title: "First", Slug: "first"}
	b := workflow.TaskView{Short: "bbb222", State: "done", Title: "Sec\nond", Slug: "second",
		External: &workflow.ExternalView{Kind: "jira", Key: "ENG-9"}, Parent: "PID"}
	c := workflow.TaskView{Short: "ccc333", State: "open", Title: "Third", Slug: "third", Parent: "GONE"}
	goldenEq(t, "list", renderListMD([]workflow.TaskView{c, b, a}, map[string]string{"PID": "aaa111"}), `# Tasks

- ccc333 [open] Third (third) ← GONE
- bbb222 [done] Sec ond (second) — ENG-9 ← aaa111
- aaa111 [open] First (first)
`)
}

// AC1: context sections and the omitted line.
func TestContextMarkdownGolden(t *testing.T) {
	secs := []ctxSection{
		{Name: "Header", Items: []string{"abc slug: T", "state: open"}},
		{Name: "Body", Items: []string{"# Big", "text"}},
		{Name: "Findings", Items: []string{"f1 P1 x", "f2 P1 y", "f3 P1 z"}},
		{Name: "Paths", Items: nil},
	}
	out := fitBudgetIn(markdownLayout, "abc", 20000, markdownSections(secs))
	goldenEq(t, "full", renderContextMD(out), `## Header

- abc slug: T
- state: open

## Body

> # Big
> text

## Findings

- f1 P1 x
- f2 P1 y
- f3 P1 z
`)
	small := markdownSections(secs)
	if strings.Contains(renderContextMD(fitBudgetIn(markdownLayout, "abc", 200, small)), "_…") {
		t.Fatal("200 tokens should fit all")
	}
	got := renderContextMD(fitBudgetIn(markdownLayout, "abc", 1, small)) // only the header is fixed
	if !strings.HasPrefix(got, "## Header\n\n- abc") || !strings.Contains(got, "## Findings\n\n_… 3 more_") || !strings.Contains(got, "## Body\n\n_… 2 more_") {
		t.Errorf("tiny budget:\n%s", got)
	}
}

// AC6: a cut body's "more" line is outside the blockquote.
func TestContextMarkdownBodyCut(t *testing.T) {
	var body []string
	for i := 0; i < 100; i++ {
		body = append(body, fmt.Sprintf("line %d", i))
	}
	secs := []ctxSection{
		{Name: "Header", Items: []string{"abc slug: T"}},
		{Name: "Body", Items: body},
	}
	got := renderContextMD(fitBudgetIn(markdownLayout, "abc", 200, markdownSections(secs)))
	i := strings.Index(got, "\n\n_… ")
	if i < 0 || !strings.HasSuffix(got, " more_\n") || !strings.Contains(got[:i], "\n> line 0") {
		t.Errorf("want a blank line before the more line:\n%s", got)
	}
	again := renderContextMD(fitBudgetIn(markdownLayout, "abc", 200, markdownSections(secs)))
	if again != got {
		t.Error("not deterministic")
	}
}

// A linked, parented task shows its parent and tracker rows in all three
// commands.
func TestMarkdownLinkedTask(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	parent := ctxTask(t, st, "par", "")
	child, err := st.CreateTask(ctx, store.NewTask{
		Slug: "kid", Title: "Kid", Body: "# Sneaky\nbody text", Fields: map[string]any{"acceptance": []any{"x"}},
		Initial: "planned", Workflow: store.WorkflowRef{Name: "review-loop", Version: 1},
		Parent:   parent.ID,
		External: &store.External{Kind: "jira", Key: "ENG-5", URL: "https://j.example/ENG-5", Owned: []string{"title"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, out, _ := ctxApp(t, ctxRepo(t), st)

	out.Reset()
	if code := a.taskShow(ctx, []string{child.Short, "--format", "markdown"}); code != 0 {
		t.Fatalf("show: %d", code)
	}
	show := out.String()
	for _, want := range []string{"# Kid\n", "- parent: " + parent.Short + "\n", "- external: jira ENG-5 https://j.example/ENG-5 owned: title", "\n## Body\n\n> # Sneaky\n> body text\n"} {
		if !strings.Contains(show, want) {
			t.Errorf("show lacks %q:\n%s", want, show)
		}
	}
	if !strings.HasSuffix(show, "\n") || strings.HasSuffix(show, "\n\n") {
		t.Errorf("trailing newline: %q", show[max(0, len(show)-10):])
	}

	out.Reset()
	if code := a.taskList(ctx, []string{"--markdown"}); code != 0 {
		t.Fatal(code)
	}
	list := out.String()
	if !strings.HasPrefix(list, "# Tasks\n\n") || !strings.Contains(list, "- "+child.Short+" [planned] Kid (kid) — ENG-5 ← "+parent.Short+"\n") {
		t.Errorf("list:\n%s", list)
	}

	code, md := ctxRun(t, a, out, child.Short, "--format", "markdown")
	if code != 0 || !strings.Contains(md, "- parent: "+parent.Short+"\n") || !strings.Contains(md, "- external: jira ENG-5 https://j.example/ENG-5 owned: title") ||
		!strings.Contains(md, "## Body\n\n> # Sneaky\n> body text\n") {
		t.Errorf("context:\n%s", md)
	}
}

// AC4: Markdown stays within the budget it is charged against, and the JSON
// accounting is what text accounting gives.
func TestContextMarkdownBudget(t *testing.T) {
	st := memstore.New(store.Options{Now: tick()})
	task := ctxTask(t, st, "alpha", "short body", "src/*.go")
	for i := 0; i < 60; i++ {
		ctxEvent(t, st, task.ID, store.KindFinding, store.FindingData{Severity: "P3", Summary: strings.Repeat("word ", 10) + string(rune('a'+i%26)) + string(rune('a'+i/26))})
	}
	a, out, _ := ctxApp(t, ctxRepo(t), st)
	for _, budget := range []string{"200", "300", "500", "1500"} {
		code, md := ctxRun(t, a, out, task.Short, "--budget", budget, "--format", "markdown")
		if code != 0 {
			t.Fatal(code)
		}
		n := 0
		for _, c := range budget {
			n = n*10 + int(c-'0')
		}
		if tokens(len(md)) > n {
			t.Errorf("budget %d: markdown is %d tokens", n, tokens(len(md)))
		}
		if budget == "200" && !strings.Contains(md, "_… ") {
			t.Errorf("budget 200 should omit lines:\n%s", md)
		}
		if strings.Contains(md, "\n\n\n") || !strings.HasSuffix(md, "\n") || strings.HasSuffix(md, "\n\n") {
			t.Errorf("blank lines or trailing newline wrong:\n%q", md)
		}
	}
	// JSON numbers are the text accounting, whatever the markdown does.
	_, js := ctxRun(t, a, out, task.Short, "--budget", "200", "--json")
	_, text := ctxRun(t, a, out, task.Short, "--budget", "200")
	var o ctxOut
	if err := json.Unmarshal([]byte(js), &o); err != nil {
		t.Fatal(err)
	}
	if o.Budget != 200 || o.Used != tokens(len(text)) {
		t.Errorf("json used %d budget %d, text is %d bytes", o.Used, o.Budget, len(text))
	}
}

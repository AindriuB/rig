package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AindriuB/workspace/cli/internal/forge"
	"github.com/AindriuB/workspace/cli/internal/forge/fake"
)

func prs() *fake.Forge {
	return &fake.Forge{PRs: []forge.PullRequest{
		{Number: 12, URL: "https://forge.example/pr/12", Title: "Add parser", State: forge.StateOpen,
			Head: "task/07-foo", HeadSHA: "abcdef0123456789", Base: "main", Mergeable: forge.MergeableNo, Draft: true,
			Checks: []forge.Check{
				{Name: "build", State: forge.CheckPass},
				{Name: "test", State: forge.CheckFail, URL: "https://ci/1"},
			}},
		{Number: 13, URL: "https://forge.example/pr/13", State: forge.StateOpen, Head: "task/08-bar",
			Title: strings.Repeat("é", 60), Mergeable: forge.MergeableYes},
		{Number: 9, State: forge.StateMerged, Head: "task/01-old", Title: "Old"},
	}}
}

func TestPRList(t *testing.T) {
	f := prs()
	r := kit(t, t.TempDir(), f, nil, "pr", "list", "--mine", "--limit", "15")
	want := "#12  red  task/07-foo  Add parser  conflict  draft\n" +
		"#13  none  task/08-bar  " + strings.Repeat("é", 50) + "\n"
	if r.code != 0 || r.out != want {
		t.Fatalf("got %q\nwant %q (%s)", r.out, want, r.errOut)
	}
	if f.Calls[0] != "ListPRs {State: Head: Mine:true Limit:15}" {
		t.Fatalf("calls = %q", f.Calls)
	}
	if r := kit(t, t.TempDir(), f, nil, "pr", "list", "--state", "merged"); r.out != "#9  none  task/01-old  Old\n" {
		t.Fatalf("merged: %+v", r)
	}
	var got []prOut
	r = kit(t, t.TempDir(), f, nil, "pr", "list", "--json", "--head=task/07-foo")
	if err := json.Unmarshal([]byte(r.out), &got); err != nil || len(got) != 1 || got[0].CI != forge.CIRed || got[0].Number != 12 {
		t.Fatalf("json: %v %+v", err, r)
	}
	for _, bad := range [][]string{{"--state", "all"}, {"--limit", "0"}, {"--web"}, {"extra"}, {"--limit"}} {
		if r := kit(t, t.TempDir(), f, nil, append([]string{"pr", "list"}, bad...)...); r.code != 2 {
			t.Errorf("pr list %v: exit %d", bad, r.code)
		}
	}
}

func TestPRViewAndChecks(t *testing.T) {
	repo, _ := newRepo(t)
	f := prs()
	for _, ref := range [][]string{{"12"}, {"--task", "07"}, {"task/07-foo"}} {
		r := kit(t, repo, f, nil, append([]string{"pr", "view"}, ref...)...)
		if r.code != 0 || !strings.HasPrefix(r.out, "#12  red  task/07-foo  Add parser  conflict  draft\n") ||
			!strings.Contains(r.out, "head abcdef012345 ") || !strings.Contains(r.out, "fail     test  https://ci/1") ||
			!strings.Contains(r.out, "1 passed or skipped") {
			t.Fatalf("pr view %v: %+v", ref, r)
		}
	}
	r := kit(t, repo, f, nil, "pr", "checks", "12")
	if r.code != 0 || r.out != "#12  red  task/07-foo\n  pass     build\n  fail     test  https://ci/1\n" {
		t.Fatalf("pr checks: %+v", r)
	}
	var got struct {
		CI     forge.CI
		Checks []forge.Check
	}
	r = kit(t, repo, f, nil, "pr", "checks", "12", "--json")
	if err := json.Unmarshal([]byte(r.out), &got); err != nil || got.CI != forge.CIRed || len(got.Checks) != 2 {
		t.Fatalf("checks json: %v %+v", err, r)
	}
	if r := kit(t, repo, f, nil, "pr", "view", "99"); r.code != 2 || !strings.Contains(r.errOut, "no pull request for 99") {
		t.Fatalf("missing PR: %+v", r)
	}
	if r := kit(t, repo, f, nil, "pr", "view", "12", "--repo", filepath.Join(repo, "nope")); r.code != 2 {
		t.Fatalf("bad --repo: %+v", r)
	}
	if r := kit(t, repo, f, nil, "pr", "view", "12", "--task", "07"); r.code != 2 {
		t.Fatalf("number and --task: %+v", r)
	}
	if r := kit(t, repo, f, nil, "pr"); r.code != 2 {
		t.Fatalf("bare pr: %+v", r)
	}
}

func TestPROpen(t *testing.T) {
	dir := t.TempDir()
	f := &fake.Forge{}
	for _, bad := range [][]string{
		{"--title", "x"},
		{"--head", "task/07-foo"},
		{"--head", "task/07-foo", "--title", "x", "--body-file", "/etc/passwd"},
		{"--head", "task/07-foo", "--title", "x", "--body-file=/etc/passwd"},
		{"--head", "task/07-foo", "--title", "x", "-F", "/etc/passwd"},
		{"--head", "task/07-foo", "--title", "x", "--push"},
		{"--head", "task/07-foo", "--title", "x", "extra"},
	} {
		if r := kit(t, dir, f, nil, append([]string{"pr", "open"}, bad...)...); r.code != 2 {
			t.Errorf("pr open %v: exit %d", bad, r.code)
		}
	}
	if len(f.Created) != 0 {
		t.Fatalf("a refused pr open reached the forge: %+v", f.Created)
	}
	r := kit(t, dir, f, nil, "pr", "open", "--head", "task/07-foo", "--base", "main", "--title", "Add parser", "--body", "line one\nline two\n", "--draft")
	if r.code != 0 || r.out != "#1  https://forge.example/pr/1\n" {
		t.Fatalf("pr open: %+v", r)
	}
	want := forge.CreatePRInput{Head: "task/07-foo", Base: "main", Title: "Add parser", Body: "line one\nline two\n", Draft: true}
	if f.Created[0] != want {
		t.Fatalf("created %+v", f.Created[0])
	}
}

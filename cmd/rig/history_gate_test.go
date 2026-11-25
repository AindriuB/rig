package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/git"
	"github.com/AindriuB/rig/internal/store"
)

// TestHistoryGate: a finding line this rig cannot read must not let a move
// through, while note and finding writes still work.
func TestHistoryGate(t *testing.T) {
	repo, _ := newRepo(t)
	loop := reviewLoopPath(t)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "none"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(t.TempDir(), "none"))
	a := &app{cwd: repo, getenv: func(k string) string {
		switch k {
		case "RIG_WORKFLOW":
			return loop
		case "RIG_ACTOR":
			return "tester"
		}
		return ""
	}}
	w, err := a.openWork(ctx, "", true)
	if err != nil {
		t.Fatal(err)
	}
	task, err := w.st.CreateTask(ctx, store.NewTask{Slug: "s", Title: "S", Initial: "implementing", Workflow: store.WorkflowRef{Name: w.wf.Name, Version: w.wf.Version}})
	if err != nil {
		t.Fatal(err)
	}
	if code, _, e := runIn(a, "finding", "add", task.Short, "--severity", "P1", "--summary", "boom"); code != 0 {
		t.Fatalf("add: %d %s", code, e)
	}
	if code, _, _ := runIn(a, "task", "move", task.Short, "reviewed"); code != 1 {
		t.Fatalf("move with open P1: exit %d, want 1", code)
	}

	ref := "refs/rig/tasks/" + task.ID
	r := git.Repo{Dir: repo}
	raw := gitc(t, repo, "cat-file", "blob", ref+":log.jsonl")
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	n := 0
	for i, l := range lines {
		if strings.Contains(l, `"kind":"finding"`) {
			lines[i] = strings.Replace(l, `"v":1`, `"v":2`, 1)
			n++
		}
	}
	if n != 1 {
		t.Fatalf("rewrote %d finding lines in %q", n, raw)
	}
	blob, _ := r.WriteBlob(ctx, []byte(strings.Join(lines, "\n")+"\n"))
	tree, _ := r.MakeTree(ctx, []git.TreeEntry{{Mode: "100644", Type: "blob", OID: blob, Name: "log.jsonl"}})
	tip := gitc(t, repo, "rev-parse", ref)
	c, _ := r.CommitTree(ctx, tree, []string{tip}, "future\n")
	if err := r.UpdateRef(ctx, ref, c, tip); err != nil {
		t.Fatal(err)
	}

	code, out, e := runIn(a, "task", "move", task.Short, "reviewed")
	if code != 1 || !strings.Contains(out+e, "history") || !strings.Contains(out+e, "upgrade rig") {
		t.Fatalf("move on degraded task: exit %d\n%s%s", code, out, e)
	}
	if code, _, e := runIn(a, "note", "add", task.Short, "--kind", "decision", "--text", "still ok"); code != 0 {
		t.Fatalf("note add: %d %s", code, e)
	}
	if code, _, e := runIn(a, "finding", "add", task.Short, "--severity", "P2", "--summary", "other"); code != 0 {
		t.Fatalf("finding add: %d %s", code, e)
	}
}

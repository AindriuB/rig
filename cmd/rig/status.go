package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/AindriuB/rig/internal/forge"
	"github.com/AindriuB/rig/internal/git"
	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/workflow"
)

const (
	handoffFile     = "claude-handoff.md"
	handoffMaxAge   = 7 * 24 * time.Hour
	handoffMaxLines = 20
	maxTasksShown   = 12
	maxPRs          = 15
	// maxStoreTasksShown caps the store section of the text output.
	maxStoreTasksShown = 50
)

type statusOut struct {
	Handoff   *handoffOut   `json:"handoff,omitempty"`
	Tasks     []string      `json:"tasks"`
	Worktrees []worktreeOut `json:"worktrees"`
	PRs       []prOut       `json:"prs"`
	PRsError  string        `json:"prsError,omitempty"`
	Store     storeView     `json:"store"`
}

// storeView is the store-based view: the open tasks with their effective
// states, and the open findings. Error is set when the store could not be
// read, and then Tasks is empty.
type storeView struct {
	Tasks    []storeTask  `json:"tasks"`
	Findings storeFinding `json:"findings"`
	Error    string       `json:"error,omitempty"`
}

type storeTask struct {
	ID       string         `json:"id"`
	Short    string         `json:"short"`
	Slug     string         `json:"slug"`
	Title    string         `json:"title"`
	State    string         `json:"state"`
	Parent   string         `json:"parent,omitempty"`
	External *storeExternal `json:"external,omitempty"`
}

type storeExternal struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
}

type storeFinding struct {
	Open   int            `json:"open"`
	ByTask map[string]int `json:"byTask"`
}

type handoffOut struct {
	Written string   `json:"written"`
	Lines   []string `json:"lines"`
}

type worktreeOut struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
	Ahead  int    `json:"ahead"`
	Dirty  int    `json:"dirty"`
}

// status implements `rig status [--json]`: the last handoff, the open task
// files, each worktree with how far ahead and how dirty it is, and one line
// per open pull request. It answers "where are we?" in one call.
//
// It always exits 0 once its arguments parse. A forge that cannot be reached
// costs the PR lines, not the rest.
func (a *app) status(ctx context.Context, args []string) int {
	fs := newFlags()
	jsonOut := fs.boolean("--json")
	if pos, err := fs.parse(args); err != nil || len(pos) > 0 {
		return a.fail("status", "usage: rig status [--json]")
	}
	out := statusOut{Tasks: []string{}, Worktrees: []worktreeOut{}, PRs: []prOut{},
		Store: storeView{Tasks: []storeTask{}, Findings: storeFinding{ByTask: map[string]int{}}}}
	repo := git.Repo{Dir: a.cwd}
	inRepo := false
	if wts, err := repo.Worktrees(ctx); err == nil && len(wts) > 0 {
		inRepo = true
		out.Handoff = readHandoff(ctx, repo)
		out.Tasks = taskFiles(wts[0].Path)
		base, _ := repo.DefaultBranch(ctx)
		for _, wt := range wts[1:] {
			if wt.Bare {
				continue
			}
			w := worktreeOut{Path: wt.Path, Branch: wt.Branch}
			if w.Branch == "" {
				w.Branch = "(detached)"
			}
			wr := git.Repo{Dir: wt.Path}
			w.Dirty, _ = wr.DirtyCount(ctx)
			if base != "" {
				w.Ahead, _, _ = wr.AheadBehind(ctx, base, "HEAD")
			}
			out.Worktrees = append(out.Worktrees, w)
		}
	}
	if f, err := a.forge(ctx, a.cwd); err != nil {
		out.PRsError = err.Error()
	} else if prs, err := f.ListPRs(ctx, forge.ListOptions{Mine: true, Limit: maxPRs}); err != nil {
		out.PRsError = err.Error()
	} else {
		for _, pr := range prs {
			out.PRs = append(out.PRs, prOut{pr, forge.Rollup(pr.Checks)})
		}
	}
	if inRepo {
		out.Store = a.storeView(ctx)
	}
	if *jsonOut {
		return a.printJSON(out)
	}

	w := a.stdout
	if !inRepo {
		fmt.Fprintln(w, "Not in a git repository.")
	}
	if h := out.Handoff; h != nil {
		fmt.Fprintf(w, "HANDOFF from the last session (%s):\n", h.Written)
		for _, l := range h.Lines {
			fmt.Fprintf(w, "  %s\n", l)
		}
	}
	if n := len(out.Tasks); n > 0 {
		fmt.Fprintf(w, "OPEN TASKS (%d):\n", n)
		for i, t := range out.Tasks {
			if i == maxTasksShown {
				fmt.Fprintf(w, "  ... and %d more\n", n-maxTasksShown)
				break
			}
			fmt.Fprintf(w, "  %s\n", t)
		}
	} else if inRepo {
		fmt.Fprintln(w, "OPEN TASKS: none")
	}
	if len(out.Worktrees) > 0 {
		fmt.Fprintf(w, "WORKTREES (%d):\n", len(out.Worktrees))
		for _, wt := range out.Worktrees {
			fmt.Fprintf(w, "  %s  ahead %d  dirty %d  %s\n", wt.Branch, wt.Ahead, wt.Dirty, wt.Path)
		}
	}
	switch {
	case out.PRsError != "":
		fmt.Fprintf(w, "OPEN PRS: unavailable (%s)\n", out.PRsError)
	case len(out.PRs) == 0:
		fmt.Fprintln(w, "OPEN PRS: none")
	default:
		fmt.Fprintf(w, "OPEN PRS (%d):\n", len(out.PRs))
		for _, pr := range out.PRs {
			fmt.Fprintf(w, "  %s\n", prLine(pr.PullRequest))
		}
	}
	printStoreView(w, out.Store)
	return 0
}

// storeView reads the task store once and returns the open tasks. It reads
// the store and the local branch heads of sha-bound tasks only: no tracker,
// forge or fetch. A store that cannot be read costs this view, not the rest.
func (a *app) storeView(ctx context.Context) storeView {
	v := storeView{Tasks: []storeTask{}, Findings: storeFinding{ByTask: map[string]int{}}}
	w, err := a.openWork(ctx, "", true)
	if err != nil {
		v.Error = err.Error()
		return v
	}
	tasks, err := w.st.Tasks(ctx, store.TaskFilter{})
	if err != nil {
		v.Error = err.Error()
		return v
	}
	shorts := make(map[string]string, len(tasks))
	for _, t := range tasks {
		shorts[t.ID] = t.Short
	}
	type openTask struct {
		t       store.Task
		view    storeTask
		created time.Time
	}
	var open []openTask
	for _, t := range tasks {
		state := t.State
		if st := w.wf.States[state]; st.ShaBound {
			// Only a sha-bound state can differ from the recorded one, and
			// only it needs the branch head.
			head, err := w.head(ctx, t)
			if err != nil {
				v.Error = err.Error()
				v.Tasks = []storeTask{}
				return v
			}
			state = w.wf.Effective(t, head)
		}
		if w.wf.States[state].Final {
			continue
		}
		st := storeTask{ID: t.ID, Short: t.Short, Slug: t.Slug, Title: t.Title, State: state}
		if t.Parent != "" {
			if st.Parent = shorts[t.Parent]; st.Parent == "" {
				st.Parent = t.Parent
			}
		}
		if e := t.External; e != nil {
			st.External = &storeExternal{Kind: e.Kind, Key: e.Key}
		}
		open = append(open, openTask{t, st, t.Created})
	}
	order := stateOrder(w.wf)
	rank := func(state string) int {
		if r, ok := order[state]; ok {
			return r
		}
		return len(order)
	}
	sort.SliceStable(open, func(i, j int) bool {
		x, y := open[i], open[j]
		if rx, ry := rank(x.view.State), rank(y.view.State); rx != ry {
			return rx < ry
		}
		if x.view.State != y.view.State {
			return x.view.State < y.view.State
		}
		if !x.created.Equal(y.created) {
			return x.created.After(y.created)
		}
		return x.t.ID > y.t.ID
	})
	for _, o := range open {
		v.Tasks = append(v.Tasks, o.view)
		if n := o.t.OpenFindings; n > 0 {
			v.Findings.Open += n
			v.Findings.ByTask[o.t.Short] = n
		}
	}
	return v
}

// stateOrder numbers the workflow's states by their shortest distance from
// the initial state along its transitions: the order a task moves through
// them. A state no transition reaches is left out and sorts last.
func stateOrder(wf *workflow.Workflow) map[string]int {
	dist := map[string]int{}
	if _, ok := wf.States[wf.Initial]; !ok {
		return dist
	}
	dist[wf.Initial] = 0
	queue := []string{wf.Initial}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		for _, tr := range wf.Transitions(s) {
			if _, ok := wf.States[tr.To]; !ok {
				continue
			}
			if _, seen := dist[tr.To]; !seen {
				dist[tr.To] = dist[s] + 1
				queue = append(queue, tr.To)
			}
		}
	}
	return dist
}

// printStoreView writes the store section of the text output.
func printStoreView(w io.Writer, v storeView) {
	switch {
	case v.Error != "":
		fmt.Fprintf(w, "STORE: unavailable (%s)\n", v.Error)
		return
	case len(v.Tasks) == 0:
		fmt.Fprintln(w, "STORE: no tasks")
		return
	}
	fmt.Fprintf(w, "STORE TASKS (%d):\n", len(v.Tasks))
	for i, t := range v.Tasks {
		if i == maxStoreTasksShown {
			fmt.Fprintf(w, "  … and %d more\n", len(v.Tasks)-maxStoreTasksShown)
			break
		}
		fmt.Fprintf(w, "  %s  %-10s  %s  %s\n", t.Short, t.State, t.Slug, escapeControl(t.Title))
	}
	if v.Findings.Open > 0 {
		shorts := sortedNames(v.Findings.ByTask)
		parts := make([]string, len(shorts))
		for i, s := range shorts {
			parts[i] = fmt.Sprintf("%s: %d", s, v.Findings.ByTask[s])
		}
		fmt.Fprintf(w, "STORE FINDINGS: %d open (%s)\n", v.Findings.Open, strings.Join(parts, ", "))
	}
}

// escapeControl writes control characters as \xNN (or \uNNNN), so a title
// cannot move the cursor or recolour the terminal.
func escapeControl(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x100 && unicode.IsControl(r):
			fmt.Fprintf(&b, `\x%02x`, r)
		case unicode.IsControl(r):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// readHandoff reads the note /handoff leaves in the git common directory,
// where every worktree sees it and it is never committed. A note older than
// seven days is ignored.
func readHandoff(ctx context.Context, repo git.Repo) *handoffOut {
	common, err := repo.CommonDir(ctx)
	if err != nil {
		return nil
	}
	path := filepath.Join(common, handoffFile)
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() || time.Since(fi.ModTime()) > handoffMaxAge {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	h := &handoffOut{Written: fi.ModTime().Format("2006-01-02 15:04"), Lines: []string{}}
	sc := bufio.NewScanner(f)
	for len(h.Lines) < handoffMaxLines && sc.Scan() {
		h.Lines = append(h.Lines, strings.TrimRight(sc.Text(), "\r"))
	}
	return h
}

// taskFiles lists the open task files in the main checkout, without ".md".
func taskFiles(root string) []string {
	entries, _ := os.ReadDir(filepath.Join(root, "docs", "plan", "tasks"))
	tasks := []string{}
	for _, e := range entries {
		if name := e.Name(); !e.IsDir() && strings.HasSuffix(name, ".md") {
			tasks = append(tasks, strings.TrimSuffix(name, ".md"))
		}
	}
	sort.Strings(tasks)
	return tasks
}

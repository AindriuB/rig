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

	"github.com/AindriuB/rig/internal/forge"
	"github.com/AindriuB/rig/internal/git"
	"github.com/AindriuB/rig/internal/store"
)

const (
	handoffFile     = "claude-handoff.md"
	handoffMaxAge   = 7 * 24 * time.Hour
	handoffMaxLines = 20
	maxTasksShown   = 12
	maxPRs          = 15
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
	for _, t := range tasks {
		if w.wf.States[t.State].Final {
			continue
		}
		v.Tasks = append(v.Tasks, storeTask{ID: t.ID, Short: t.Short, Slug: t.Slug, Title: t.Title, State: t.State})
	}
	return v
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
	for _, t := range v.Tasks {
		fmt.Fprintf(w, "  %s  %-10s  %s  %s\n", t.Short, t.State, t.Slug, t.Title)
	}
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

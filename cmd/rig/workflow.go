package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AindriuB/rig/internal/git"
	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/workflow"
)

// workflow implements `rig workflow`: show and validate.
func (a *app) workflow(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(a.stderr, "usage: rig workflow show|validate")
		return 2
	}
	switch args[0] {
	case "show":
		return a.workflowShow(ctx, args[1:])
	case "validate":
		return a.workflowValidate(ctx, args[1:])
	}
	return a.fail("workflow", "unknown subcommand %q; want show or validate", args[0])
}

// sourceView is a workflow's source as --json shows it: the workflow
// package's Source, plus whether $RIG_WORKFLOW chose it.
type sourceView struct {
	Preset string `json:"preset,omitempty"`
	Path   string `json:"path,omitempty"`
	// Env is true when $RIG_WORKFLOW named the workflow.
	Env bool `json:"env,omitempty"`
}

func (a *app) sourceView(src workflow.Source) sourceView {
	return sourceView{
		Preset: src.Preset,
		Path:   src.Path,
		Env:    strings.TrimSpace(a.getenv("RIG_WORKFLOW")) != "",
	}
}

func (s sourceView) String() string {
	out := s.Path
	if s.Preset != "" {
		out = "preset " + s.Preset
	}
	if s.Env {
		out += " (from RIG_WORKFLOW)"
	}
	return out
}

func (a *app) workflowShow(ctx context.Context, args []string) int {
	fs := newFlags()
	repo := fs.str("--repo")
	asJSON := fs.boolean("--json")
	pos, err := fs.parse(args)
	if err != nil {
		return a.fail("workflow show", "%v", err)
	}
	if len(pos) > 0 {
		return a.fail("workflow show", "unexpected argument %q", pos[0])
	}
	w, err := a.openWork(ctx, *repo, true)
	if err != nil {
		return a.storeFail("workflow show", err)
	}
	src := a.sourceView(w.src)
	if *asJSON {
		return a.printJSON(struct {
			Source   sourceView         `json:"source"`
			Workflow *workflow.Workflow `json:"workflow"`
		}{src, w.wf})
	}
	wf := w.wf
	fmt.Fprintf(a.stdout, "name: %s\nversion: %d\nsource: %s\ninitial: %s\n", wf.Name, wf.Version, src, wf.Initial)
	fmt.Fprintln(a.stdout, "states:")
	names := make([]string, 0, len(wf.States))
	for n := range wf.States {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		s := wf.States[n]
		who := s.Who
		if who == "" {
			who = workflow.WhoAgent
		}
		fmt.Fprintf(a.stdout, "  %s  action=%s who=%s final=%t shaBound=%t\n", n, dash(s.Action), who, s.Final, s.ShaBound)
	}
	fmt.Fprintln(a.stdout, "transitions:")
	for _, tr := range wf.AllTransitions {
		line := fmt.Sprintf("  %s -> %s", strings.Join(tr.From, ","), tr.To)
		if tr.Takes != "" {
			line += " takes " + tr.Takes
		}
		if len(tr.Gates) > 0 {
			gates := make([]string, len(tr.Gates))
			for i, g := range tr.Gates {
				b, err := json.Marshal(g)
				if err != nil {
					b = []byte(g.Kind)
				}
				gates[i] = string(b)
			}
			line += "  gates: " + strings.Join(gates, " ")
		}
		fmt.Fprintln(a.stdout, line)
	}
	if levels := wf.PushLevels(); len(levels) > 0 {
		fmt.Fprintf(a.stdout, "push levels: %s\n", strings.Join(levels, " > "))
	}
	if wf.Tracker != nil && len(wf.Tracker.Push.Types) > 0 {
		fmt.Fprintf(a.stdout, "push types: %s\n", strings.Join(wf.Tracker.Push.Types, ", "))
	}
	return 0
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// unknownState is a task whose recorded state the workflow lacks.
type unknownState struct {
	Task  string `json:"task"`
	Short string `json:"short"`
	State string `json:"state"`
}

func (a *app) workflowValidate(ctx context.Context, args []string) int {
	fs := newFlags()
	repoFlag := fs.str("--repo")
	asJSON := fs.boolean("--json")
	pos, err := fs.parse(args)
	if err != nil {
		return a.fail("workflow validate", "%v", err)
	}
	if len(pos) > 1 {
		return a.fail("workflow validate", "at most one file, got %d arguments", len(pos))
	}

	var (
		wf      *workflow.Workflow
		invalid *workflow.InvalidError
		st      store.Store
	)
	if len(pos) == 1 {
		file := pos[0]
		if !filepath.IsAbs(file) {
			file = filepath.Join(a.cwd, file)
		}
		var lerr error
		wf, _, lerr = workflow.Load(file)
		if lerr != nil && !errors.As(lerr, &invalid) {
			return a.fail("workflow validate", "%v", lerr)
		}
		dir, err := a.repoDir(*repoFlag)
		if err != nil {
			return a.fail("workflow validate", "%v", err)
		}
		// Outside a repository there are no tasks to check; the file's
		// validity is still the answer.
		if _, cerr := (git.Repo{Dir: dir}).CommonDir(ctx); cerr == nil {
			open := a.store
			if open == nil {
				open = a.openStore
			}
			if st, err = open(ctx, dir); err != nil {
				return a.fail("workflow validate", "%v", err)
			}
		}
	} else {
		w, err := a.openWork(ctx, *repoFlag, false)
		if w == nil {
			return a.fail("workflow validate", "%v", err)
		}
		wf, st = w.wf, w.st
		if err != nil && !errors.As(err, &invalid) {
			return a.fail("workflow validate", "%v", err)
		}
	}

	problems := []workflow.Problem{}
	if invalid != nil {
		problems = invalid.Problems
	}
	unknown := []unknownState{}
	var tasks []store.Task
	if st != nil {
		if tasks, err = st.Tasks(ctx, store.TaskFilter{}); err != nil {
			return a.fail("workflow validate", "%v", err)
		}
	}
	for _, t := range tasks {
		if _, ok := wf.States[t.State]; !ok {
			unknown = append(unknown, unknownState{Task: t.ID, Short: t.Short, State: t.State})
		}
	}

	code := 0
	if len(problems) > 0 {
		code = 1
	}
	if *asJSON {
		out := struct {
			Valid         bool               `json:"valid"`
			Problems      []workflow.Problem `json:"problems"`
			UnknownStates []unknownState     `json:"unknownStates"`
		}{code == 0, problems, unknown}
		if rc := a.printJSON(out); rc != 0 {
			return rc
		}
		return code
	}
	if code == 0 {
		fmt.Fprintf(a.stdout, "workflow %s is valid\n", wf.Name)
	} else {
		fmt.Fprintf(a.stdout, "workflow %s is invalid:\n", wf.Name)
		for _, p := range problems {
			fmt.Fprintf(a.stdout, "  %s\n", p)
		}
	}
	for _, u := range unknown {
		fmt.Fprintf(a.stdout, "task %s (%s) is in state %q, which the workflow lacks\n", u.Short, u.Task, u.State)
	}
	return code
}

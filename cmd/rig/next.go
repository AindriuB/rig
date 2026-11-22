package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/workflow"
)

// nextGate is one gate's verdict in `rig next`. OK is null for a gate that
// was not run: a command gate would run a program, and next stays fast and
// free of side effects.
type nextGate struct {
	Gate    string `json:"gate"`
	OK      *bool  `json:"ok"`
	Message string `json:"message,omitempty"`
}

// nextTransition is one transition out of a task's state. OK is true when
// every gate passes, false when one fails, and null when none fails but a
// command gate was not run. Failing names the gates that failed.
type nextTransition struct {
	To      string     `json:"to"`
	OK      *bool      `json:"ok"`
	Failing []string   `json:"failing"`
	Gates   []nextGate `json:"gates"`
}

// nextItem is one task and what it needs.
type nextItem struct {
	Task        string           `json:"task"`
	Short       string           `json:"short"`
	Slug        string           `json:"slug"`
	State       string           `json:"state"`
	Action      string           `json:"action"`
	Who         string           `json:"who"`
	Transitions []nextTransition `json:"transitions"`

	created  string
	distance int
}

// nextOut is the --json output. Skipped counts the tasks left out: those in
// a final state, those the --who filter drops, and those beyond --limit.
type nextOut struct {
	Items   []nextItem `json:"items"`
	Skipped int        `json:"skipped"`
}

// unknownState is the action of a task in a state the workflow lacks.
const unknownState = "unknown-state"

// unreachable is the distance of a state with no path to a final state.
const unreachable = 1 << 30

// next implements `rig next [--who agent|human] [--limit n] [--json]`. It
// reads the store, the workflow and branch heads; it never calls the forge,
// never writes and never runs a command gate.
func (a *app) next(ctx context.Context, args []string) int {
	const usage = "usage: rig next [--who agent|human] [--limit <n>] [--json] [--repo <dir>]"
	fs := newFlags()
	who := fs.str("--who")
	limitS := fs.str("--limit")
	repoFlag := fs.str("--repo")
	jsonOut := fs.boolean("--json")
	if pos, err := fs.parse(args); err != nil || len(pos) > 0 {
		return a.fail("next", "%s", usage)
	}
	if *who != "" && *who != workflow.WhoAgent && *who != workflow.WhoHuman {
		return a.fail("next", "--who is agent or human, got %q", *who)
	}
	limit := 0
	if *limitS != "" {
		n, err := strconv.Atoi(*limitS)
		if err != nil || n < 1 {
			return a.fail("next", "--limit needs a positive number, got %q", *limitS)
		}
		limit = n
	}

	w, err := a.openWork(ctx, *repoFlag, true)
	if err != nil {
		return a.storeFail("next", err)
	}
	tasks, err := w.st.Tasks(ctx, store.TaskFilter{})
	if err != nil {
		return a.storeFail("next", err)
	}
	dist := w.distances()
	env := w.env()
	env.Runner = nil // command gates are not run

	out := nextOut{Items: []nextItem{}}
	for _, t := range tasks {
		head, err := w.head(ctx, t)
		if err != nil {
			return a.storeFail("next", err)
		}
		state := w.wf.Effective(t, head)
		if w.wf.States[state].Final {
			out.Skipped++
			continue
		}
		findings, err := w.st.Findings(ctx, t.ID)
		if err != nil {
			return a.storeFail("next", err)
		}
		item := nextItem{
			Task: t.ID, Short: t.Short, Slug: t.Slug, State: state,
			Action: w.wf.States[state].Action, Who: w.wf.States[state].Who,
			Transitions: []nextTransition{},
			created:     t.Created.UTC().Format("2006-01-02T15:04:05.000000000Z"),
			distance:    unreachable,
		}
		if _, ok := w.wf.States[state]; !ok {
			item.Action = unknownState
		} else if d, ok := dist[state]; ok {
			item.distance = d
		}
		if item.Who == "" {
			item.Who = workflow.WhoAgent
		}
		if *who != "" && item.Who != *who {
			out.Skipped++
			continue
		}
		for _, tr := range w.wf.Transitions(state) {
			item.Transitions = append(item.Transitions, w.nextTransition(ctx, env, t, findings, tr, head))
		}
		out.Items = append(out.Items, item)
	}

	sort.SliceStable(out.Items, func(i, j int) bool {
		x, y := out.Items[i], out.Items[j]
		if (x.Who == workflow.WhoHuman) != (y.Who == workflow.WhoHuman) {
			return x.Who == workflow.WhoHuman
		}
		if x.distance != y.distance {
			return x.distance < y.distance
		}
		if x.created != y.created {
			return x.created < y.created
		}
		return x.Task < y.Task
	})
	if limit > 0 && len(out.Items) > limit {
		out.Skipped += len(out.Items) - limit
		out.Items = out.Items[:limit]
	}

	if *jsonOut {
		return a.printJSON(out)
	}
	for _, it := range out.Items {
		fmt.Fprintf(a.stdout, "%s  %s  %s  %s (%s)\n", it.Short, it.Slug, it.State, it.Action, it.Who)
		for _, tr := range it.Transitions {
			mark := "ok"
			switch {
			case tr.OK == nil:
				mark = "not checked: command gate"
			case !*tr.OK:
				mark = fmt.Sprintf("blocked by %v", tr.Failing)
			}
			fmt.Fprintf(a.stdout, "    -> %s: %s\n", tr.To, mark)
		}
	}
	if len(out.Items) == 0 {
		fmt.Fprintln(a.stdout, "nothing to do")
	}
	return 0
}

// distances returns, for each declared state with a path to a final state,
// the length of the shortest one. $previous targets are not followed.
func (w *work) distances() map[string]int {
	dist := map[string]int{}
	var queue []string
	for name, s := range w.wf.States {
		if s.Final {
			dist[name] = 0
			queue = append(queue, name)
		}
	}
	// Reverse edges: to -> the states that reach it in one step.
	rev := map[string][]string{}
	for name := range w.wf.States {
		for _, tr := range w.wf.Transitions(name) {
			if _, ok := w.wf.States[tr.To]; ok {
				rev[tr.To] = append(rev[tr.To], name)
			}
		}
	}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		for _, p := range rev[s] {
			if _, seen := dist[p]; !seen {
				dist[p] = dist[s] + 1
				queue = append(queue, p)
			}
		}
	}
	return dist
}

// nextTransition checks tr's gates for t with the branch head as the sha
// argument, leaving command gates unrun.
func (w *work) nextTransition(ctx context.Context, env workflow.Env, t store.Task, findings []store.Finding, tr workflow.Transition, head string) nextTransition {
	var notRun []workflow.Gate
	checked := tr
	checked.Gates = nil
	for _, g := range tr.Gates {
		if g.Kind == workflow.GateCommand {
			notRun = append(notRun, g)
		} else {
			checked.Gates = append(checked.Gates, g)
		}
	}
	out := nextTransition{To: tr.To, Failing: []string{}, Gates: []nextGate{}}
	allOK := true
	for _, r := range w.wf.Check(ctx, env, t, findings, checked, workflow.Args{Sha: head}) {
		ok := r.OK
		out.Gates = append(out.Gates, nextGate{Gate: r.Gate, OK: &ok, Message: r.Message})
		if !ok {
			allOK = false
			out.Failing = append(out.Failing, r.Gate)
		}
	}
	for _, g := range notRun {
		out.Gates = append(out.Gates, nextGate{Gate: g.Kind, Message: "not run"})
	}
	switch {
	case !allOK:
		f := false
		out.OK = &f
	case len(notRun) == 0:
		tt := true
		out.OK = &tt
	}
	return out
}

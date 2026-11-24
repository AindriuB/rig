package workflow

import (
	"errors"
	"fmt"
	"strings"

	"github.com/AindriuB/rig/internal/store"
)

// Args are the arguments a transition may take.
type Args struct {
	Sha    string
	PR     int
	Reason string
}

// BranchFor returns the task's branch name from the workflow's template, or
// "" when the workflow has none. {key} is the linked issue's key as
// store.BranchKey gives it, or the short id when the task has no link, as
// store.ExpandBranch does when a branch is stamped.
func (w *Workflow) BranchFor(t store.Task) string {
	if w.Branch == "" {
		return ""
	}
	key := ""
	if t.External != nil {
		key = t.External.Key
	}
	return store.ExpandBranch(w.Branch, t.Short, t.Slug, t.ID, key)
}

// Effective returns the task's state after the sha-bound check: when the
// recorded state is sha-bound and the branch head has moved off the sha
// recorded entering that state, it is OnNewCommit. headSha "" means the head
// is unknown (no branch), and the recorded state stands.
func (w *Workflow) Effective(t store.Task, headSha string) string {
	s, ok := w.States[t.State]
	if !ok || !s.ShaBound || headSha == "" || w.OnNewCommit == "" {
		return t.State
	}
	if headSha != t.Shas[t.State] {
		return w.OnNewCommit
	}
	return t.State
}

// Events returns the events that record tr, taken from the task's current
// state to the state to. The current state is Effective(t, headSha), and tr
// must leave it: its From names that state, or AnyState when that state is
// not final and is not tr's target. to must be tr.To, or t.Previous when
// tr.To is Previous. The transition records the argument tr takes, which args
// must carry, and a count event follows when tr counts.
//
// A transition into a sha-bound state that takes no sha binds the target to
// the sha the from-state was bound to, when the from-state is sha-bound too;
// otherwise it records none, and the target keeps the sha it was last
// entered at. So "verified → pr_open" carries verified's sha, and returning
// from "stopped" to "ready" keeps ready's original sha, which a commit made
// while stopped then moves the task off.
func (w *Workflow) Events(t store.Task, headSha string, tr Transition, to string, args Args) ([]store.Event, error) {
	from := w.Effective(t, headSha)
	if !w.leaves(tr, from) {
		return nil, fmt.Errorf("transition: from %v does not leave the task's state %q", []string(tr.From), from)
	}
	want := tr.To
	if want == Previous {
		want = t.Previous
	}
	switch {
	case to == "" || to == Previous:
		return nil, errors.New("transition: target state is not resolved")
	case want == "":
		return nil, fmt.Errorf("transition %s → %s: the task has no previous state", from, Previous)
	case to != want:
		return nil, fmt.Errorf("transition: target %q is not the transition's target %q", to, want)
	}
	d := store.TransitionData{From: from, To: to}
	switch tr.Takes {
	case TakesSha:
		if args.Sha == "" {
			return nil, fmt.Errorf("transition %s → %s takes a sha", from, to)
		}
		d.Sha = args.Sha
	case TakesPR:
		if args.PR <= 0 {
			return nil, fmt.Errorf("transition %s → %s takes a pull request number", from, to)
		}
		d.PR = args.PR
	case TakesReason:
		if strings.TrimSpace(args.Reason) == "" {
			return nil, fmt.Errorf("transition %s → %s takes a reason", from, to)
		}
		d.Reason = args.Reason
	}
	if d.Sha == "" && w.States[to].ShaBound && w.States[from].ShaBound {
		d.Sha = t.Shas[from]
	}
	ev, err := store.NewEvent(store.KindTransition, d)
	if err != nil {
		return nil, fmt.Errorf("transition: %w", err)
	}
	out := []store.Event{ev}
	if tr.Count != "" {
		ev, err := store.NewEvent(store.KindCount, store.CountData{Counter: tr.Count, By: 1})
		if err != nil {
			return nil, fmt.Errorf("transition: %w", err)
		}
		out = append(out, ev)
	}
	return out, nil
}

// leaves reports whether tr leaves state from: its From names from, or names
// AnyState while from is not final and is not tr's target.
func (w *Workflow) leaves(tr Transition, from string) bool {
	for _, f := range tr.From {
		if f == from {
			return true
		}
		if f == AnyState && !w.States[from].Final && tr.To != from {
			return true
		}
	}
	return false
}

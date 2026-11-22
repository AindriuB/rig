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
// "" when the workflow has none.
func (w *Workflow) BranchFor(t store.Task) string {
	if w.Branch == "" {
		return ""
	}
	return strings.NewReplacer("{short}", t.Short, "{slug}", t.Slug, "{id}", t.ID).Replace(w.Branch)
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

// Events returns the events that record tr, taken from tr.From[0] (as
// Transitions gives it) to the state to; the caller resolves a Previous
// target from t.Previous. The transition records the argument tr takes,
// which args must carry, and a count event follows when tr counts.
//
// A transition into a sha-bound state that takes no sha binds the target to
// the sha the from-state was bound to, when the from-state is sha-bound too;
// otherwise it records none, and the target keeps the sha it was last
// entered at. So "verified → pr_open" carries verified's sha, and returning
// from "stopped" to "ready" keeps ready's original sha, which a commit made
// while stopped then moves the task off.
func (w *Workflow) Events(t store.Task, tr Transition, to string, args Args) ([]store.Event, error) {
	if len(tr.From) != 1 {
		return nil, errors.New("transition: give one from-state, as Transitions returns it")
	}
	if to == "" || to == Previous {
		return nil, errors.New("transition: target state is not resolved")
	}
	from := tr.From[0]
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

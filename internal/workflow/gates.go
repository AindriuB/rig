package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/AindriuB/rig/internal/store"
)

// Env is what gates need from outside the workflow. A nil member fails the
// gates that need it.
type Env struct {
	// HeadSha returns the tip of the task's branch, or "" when the branch
	// does not exist.
	HeadSha func(ctx context.Context, t store.Task) (string, error)
	// Task resolves a task reference, for dependsIn.
	Task func(ctx context.Context, ref string) (store.Task, error)
	// Runner runs command gates.
	Runner Runner
}

// GateResult is one gate's verdict.
type GateResult struct {
	Gate    string `json:"gate"`
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

// Check evaluates tr's gates, in order, for task t with its findings and the
// arguments the caller gives the transition. Every gate is evaluated; the
// transition may be written only when every result is OK. A workflow that
// does not pass Validate fails every gate, and a transition with no gates
// gets one failing result, so nothing moves under a broken workflow.
func (w *Workflow) Check(ctx context.Context, env Env, t store.Task, findings []store.Finding, tr Transition, args Args) []GateResult {
	if ps := w.Validate(); len(ps) > 0 {
		msg := (&InvalidError{Name: w.Name, Problems: ps}).Error()
		if len(tr.Gates) == 0 {
			return []GateResult{{Gate: "workflow", Message: msg}}
		}
		out := make([]GateResult, len(tr.Gates))
		for i, g := range tr.Gates {
			out[i] = GateResult{Gate: g.Kind, Message: msg}
		}
		return out
	}
	out := make([]GateResult, 0, len(tr.Gates))
	for _, g := range tr.Gates {
		r := GateResult{Gate: g.Kind}
		if g.problem != "" {
			r.Message = "invalid gate: " + g.problem
		} else {
			r.OK, r.Message = w.check(ctx, env, t, findings, tr, args, g)
		}
		out = append(out, r)
	}
	return out
}

// check evaluates one well-formed gate.
func (w *Workflow) check(ctx context.Context, env Env, t store.Task, findings []store.Finding, tr Transition, args Args, g Gate) (bool, string) {
	switch g.Kind {
	case GateNoOpenFindings:
		var open []string
		for _, f := range findings {
			if f.Status == store.StatusOpen && slices.Contains(g.Severities, f.Severity) {
				open = append(open, f.Short+" ("+f.Severity+")")
			}
		}
		if len(open) > 0 {
			return false, fmt.Sprintf("%d open finding(s): %s", len(open), strings.Join(open, ", "))
		}
		return true, ""

	case GateShaIsHead:
		if args.Sha == "" {
			return false, "no sha given"
		}
		if env.HeadSha == nil {
			return false, "the branch head is not available"
		}
		head, err := env.HeadSha(ctx, t)
		switch {
		case err != nil:
			return false, fmt.Sprintf("read the branch head: %v", err)
		case head == "":
			return false, "the task's branch does not exist"
		case head != args.Sha:
			return false, fmt.Sprintf("sha %s is not the branch head %s", args.Sha, head)
		}
		return true, ""

	case GateShaIs:
		want := t.Shas[g.State]
		switch {
		case args.Sha == "":
			return false, "no sha given"
		case want == "":
			return false, fmt.Sprintf("no sha recorded entering %s", g.State)
		case want != args.Sha:
			return false, fmt.Sprintf("sha %s is not the sha recorded entering %s, %s", args.Sha, g.State, want)
		}
		return true, ""

	case GateDependsIn:
		refs, err := taskRefs(t.Fields[DependsField])
		if err != nil {
			return false, fmt.Sprintf("field %s: %v", DependsField, err)
		}
		if len(refs) > 0 && env.Task == nil {
			return false, "other tasks are not available"
		}
		var waiting []string
		for _, ref := range refs {
			dep, err := env.Task(ctx, ref)
			if err != nil {
				return false, fmt.Sprintf("dependency %s: %v", ref, err)
			}
			state := dep.State
			if w.States[state].ShaBound {
				if env.HeadSha == nil {
					return false, "the branch head is not available"
				}
				head, err := env.HeadSha(ctx, dep)
				if err != nil {
					return false, fmt.Sprintf("dependency %s: read the branch head: %v", ref, err)
				}
				state = w.Effective(dep, head)
			}
			if !slices.Contains(g.States, state) {
				waiting = append(waiting, fmt.Sprintf("%s is %s", ref, state))
			}
		}
		if len(waiting) > 0 {
			return false, fmt.Sprintf("dependencies not in %s: %s", strings.Join(g.States, ", "), strings.Join(waiting, "; "))
		}
		return true, ""

	case GateFieldSet:
		var v any
		switch g.Field {
		case "title":
			v = t.Title
		case "body":
			v = t.Body
		default:
			v = t.Fields[g.Field]
		}
		if isEmpty(v) {
			return false, fmt.Sprintf("field %s is not set", g.Field)
		}
		return true, ""

	case GateCounterBelow:
		if n := t.Counters[g.Counter]; n >= g.Max {
			return false, fmt.Sprintf("counter %s is %d, not below %d", g.Counter, n, g.Max)
		}
		return true, ""

	case GateCommand:
		head := ""
		if env.HeadSha != nil {
			h, err := env.HeadSha(ctx, t)
			if err != nil {
				return false, fmt.Sprintf("read the branch head: %v", err)
			}
			head = h
		}
		stdin, err := json.Marshal(w.View(t, head))
		if err != nil {
			return false, err.Error()
		}
		argv := substitute(g.Argv, placeholderValues(t, tr.fromState(t), args.Reason, args.Sha))
		if err := run(ctx, env.Runner, argv, stdin); err != nil {
			return false, err.Error()
		}
		return true, ""
	}
	return false, fmt.Sprintf("unknown gate %q", g.Kind)
}

// fromState is the state the transition leaves: its one from-state, as
// Transitions gives it, or else the task's recorded state.
func (tr Transition) fromState(t store.Task) string {
	if len(tr.From) == 1 && tr.From[0] != AnyState {
		return tr.From[0]
	}
	return t.State
}

// taskRefs reads a tasks field's value as task references.
func taskRefs(v any) ([]string, error) {
	switch v := v.(type) {
	case nil:
		return nil, nil
	case []string:
		return v, nil
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			s, ok := e.(string)
			if !ok || s == "" {
				return nil, fmt.Errorf("%v is not a task reference", e)
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("want a list of task references, got %T", v)
}

// isEmpty reports whether a field value counts as unset.
func isEmpty(v any) bool {
	switch v := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(v) == ""
	case []any:
		return len(v) == 0
	case []string:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	}
	return false
}

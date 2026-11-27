package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/AindriuB/rig/internal/store"
)

// QueuedTimeout is how long a queued hook may run when it sets no timeout.
const QueuedTimeout = 2 * time.Minute

// QueuedHooks returns the indexes of the workflow's queued hooks that have a
// command, in file order.
func (w *Workflow) QueuedHooks() []int {
	var out []int
	for i, h := range w.Hooks {
		if h.Mode == ModeQueued && len(h.Run) > 0 {
			out = append(out, i)
		}
	}
	return out
}

// QueuedMatches reports whether hook i's selectors match the event.
func (w *Workflow) QueuedMatches(i int, event store.Event) (bool, error) {
	var d store.TransitionData
	if event.Kind == store.KindTransition {
		if err := json.Unmarshal(event.Data, &d); err != nil {
			return false, fmt.Errorf("hooks: read transition: %w", err)
		}
	}
	return w.hookMatches(w.Hooks[i], event.Kind, d), nil
}

// QueuedMaxDepth is the deepest cause.depth hook i runs for. ok is false when
// the hook sets no maxDepth: it is then uncapped.
func (w *Workflow) QueuedMaxDepth(i int) (max int, ok bool) {
	if m := w.Hooks[i].MaxDepth; m != nil {
		return *m, true
	}
	return 0, false
}

// RunQueued runs queued hook i once for one event and returns its error. t is
// the task as recorded right after the event, nil for a repository note. The
// hook gets {"format":1,"event":…,"task":…} on stdin, as rig events --json
// prints it, and runs for its timeout (QueuedTimeout by default).
func (w *Workflow) RunQueued(ctx context.Context, r Runner, i int, event store.Event, t *store.Task) error {
	h := w.Hooks[i]
	in := hookInput{Format: 1, Event: event}
	var values map[string]string
	var taskID, state, reason, sha string
	if event.Kind == store.KindTransition {
		var d store.TransitionData
		if err := json.Unmarshal(event.Data, &d); err != nil {
			return fmt.Errorf("hooks: read transition: %w", err)
		}
		state, reason, sha = d.To, d.Reason, d.Sha
	}
	if t != nil {
		v := w.View(*t, "")
		v.State, v.HeadSha = v.RecordedState, ""
		in.Task, taskID = &v, t.ID
		if state == "" {
			state = t.State
		}
		values = placeholderValues(*t, state, reason, sha)
	} else {
		values = placeholderValues(store.Task{}, "", "", "")
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("hooks: %w", err)
	}
	name := w.HookName(i)
	hr := HookRun{Timeout: hookTimeout(h, QueuedTimeout), Env: hookEnv(name, event, taskID),
		EnvAllow: append(append([]string(nil), baseHookEnv...), h.Env...)}
	return runHook(ctx, r, substitute(h.Run, values), stdin, hr)
}

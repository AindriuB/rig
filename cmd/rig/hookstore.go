package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/AindriuB/rig/internal/git"
	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/store/gitref"
)

type cmdKey struct{}

// withCmd names the running command ("task move") for messages from hooks.
func withCmd(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, cmdKey{}, name)
}

func cmdFrom(ctx context.Context) string {
	s, _ := ctx.Value(cmdKey{}).(string)
	return s
}

// maxHookDepth is the depth at which a write made inside a hook stops firing
// hooks: the third write in a chain.
const maxHookDepth = 3

// envDepthCap bounds a depth taken from $RIG_HOOK_DEPTH, far beyond any
// hook's maxDepth, so adding one cannot overflow.
const envDepthCap = 1 << 20

// hookWriter wraps a store so that every event a single-task write produces
// runs the workflow's inline hooks that match it. Hooks run after the store
// has released its lock. Commands that must run none (rig tracker) use the
// unwrapped store.
type hookWriter struct {
	store.Store
	w      *work
	a      *app
	warned bool
	errs   []error
}

func (h *hookWriter) UpdateEvents(ctx context.Context, ref string, next func(store.Task, []store.Finding) ([]store.Event, error)) (store.Task, []store.Event, error) {
	t, evs, err := h.Store.UpdateEvents(ctx, ref, next)
	if err != nil {
		return t, evs, err
	}
	h.after(ctx, t.ID, evs)
	return t, evs, nil
}

func (h *hookWriter) Update(ctx context.Context, ref string, next func(store.Task, []store.Finding) ([]store.Event, error)) (store.Task, error) {
	t, _, err := h.UpdateEvents(ctx, ref, next)
	return t, err
}

func (h *hookWriter) CreateTaskEvent(ctx context.Context, n store.NewTask) (store.Task, store.Event, error) {
	t, ev, err := h.Store.CreateTaskEvent(ctx, n)
	if err != nil {
		return t, ev, err
	}
	h.after(ctx, t.ID, []store.Event{ev})
	return t, ev, nil
}

func (h *hookWriter) CreateTask(ctx context.Context, n store.NewTask) (store.Task, error) {
	t, _, err := h.CreateTaskEvent(ctx, n)
	return t, err
}

func (h *hookWriter) AddNoteEvent(ctx context.Context, n store.Note) (store.Note, store.Event, error) {
	got, ev, err := h.Store.AddNoteEvent(ctx, n)
	if err != nil {
		return got, ev, err
	}
	h.after(ctx, got.Task, []store.Event{ev})
	return got, ev, nil
}

func (h *hookWriter) AddNote(ctx context.Context, n store.Note) (store.Note, error) {
	got, _, err := h.AddNoteEvent(ctx, n)
	return got, err
}

// take returns the hook errors so far and forgets them.
func (h *hookWriter) take() []error {
	e := h.errs
	h.errs = nil
	return e
}

// after runs the hooks for evs, which were just written to the log of task id
// (the repository's notes log when id is ""). A failure is printed and kept;
// the write stands.
func (h *hookWriter) after(ctx context.Context, id string, evs []store.Event) {
	cmd := cmdFrom(ctx)
	wf := h.w.wf
	if len(evs) == 0 || wf == nil || len(wf.Hooks) == 0 {
		return
	}
	if c := evs[0].Cause; c != nil && c.Depth >= maxHookDepth {
		ids := make([]string, len(evs))
		for i, e := range evs {
			ids[i] = e.ID
		}
		fmt.Fprintf(h.out(), "rig %s: hook depth %d reached: no hooks run for event %s\n", cmd, c.Depth, strings.Join(ids, ", "))
		return
	}
	if !h.warned {
		h.warned = true
		for _, m := range wf.HookWarnings() {
			fmt.Fprintf(h.out(), "rig %s: warning: %s\n", cmd, m)
		}
	}
	runner := h.w.env().Runner
	fail := func(errs []error) {
		for _, e := range errs {
			fmt.Fprintf(h.out(), "rig %s: hook: %v\n", cmd, e)
			h.errs = append(h.errs, e)
		}
	}
	if id == "" {
		for _, ev := range evs {
			fail(wf.RunRepoHooks(ctx, runner, ev))
		}
		return
	}
	log, err := h.Store.Events(ctx, id)
	if err != nil {
		fail([]error{fmt.Errorf("hooks: read log: %w", err)})
		return
	}
	store.SortEvents(log)
	// Where the command sees the branch head: once, at the final state.
	var head string
	headDone := false
	for _, ev := range evs {
		end := -1
		for i := range log {
			if log[i].ID == ev.ID {
				end = i + 1
			}
		}
		if end < 0 {
			fail([]error{fmt.Errorf("hooks: event %s not found in its log", ev.ID)})
			continue
		}
		t, _, _, _ := store.Fold(log[:end])
		if !headDone {
			full, _, _, _ := store.Fold(log)
			if hd, err := h.w.head(ctx, full); err == nil {
				head = hd
			}
			headDone = true
		}
		fail(wf.RunHooks(ctx, runner, ev, t, head))
	}
}

// hookCause is the cause a write made inside a hook records, from the event
// the hook ran for ($RIG_HOOK_EVENT) and its hook ($RIG_HOOK_NAME): depth is
// the cause event's own cause depth plus one, or 1 when it has none or cannot
// be found, in which case $RIG_HOOK_DEPTH (a non-negative integer) plus one
// stands in when set (exactly, up to envDepthCap, so a hook's own maxDepth
// can compare against it; larger values record envDepthCap rather than
// overflow). The event is looked up in $RIG_HOOK_TASK's log, or the
// repository's notes log when that is unset. It returns nil outside a hook.
func (a *app) hookCause(ctx context.Context, st store.Store, repo git.Repo) *store.Cause {
	evID := strings.TrimSpace(a.getenv("RIG_HOOK_EVENT"))
	if evID == "" {
		return nil
	}
	c := &store.Cause{Event: evID, Hook: a.getenv("RIG_HOOK_NAME"), Depth: 1}
	var log []store.Event
	if ref := strings.TrimSpace(a.getenv("RIG_HOOK_TASK")); ref != "" {
		log, _ = st.Events(ctx, ref)
	} else if tip, ok, err := repo.ResolveRef(ctx, gitref.NotesRef); err == nil && ok {
		if blobs, err := repo.ReadBlobs(ctx, []string{tip + ":" + gitref.LogFile}); err == nil && len(blobs) == 1 && !blobs[0].Missing {
			for _, line := range bytes.Split(blobs[0].Data, []byte("\n")) {
				var e store.Event
				if json.Unmarshal(line, &e) == nil {
					log = append(log, e)
				}
			}
		}
	}
	found := false
	for _, e := range log {
		if e.ID == evID {
			found = true
			if e.Cause != nil {
				c.Depth = e.Cause.Depth + 1
			}
		}
	}
	if !found {
		if n, err := strconv.Atoi(strings.TrimSpace(a.getenv("RIG_HOOK_DEPTH"))); err == nil && n >= 0 {
			c.Depth = min(n, envDepthCap-1) + 1
		}
	}
	return c
}

// out is stderr, or nowhere for an app that has none.
func (h *hookWriter) out() io.Writer {
	if h.a.stderr == nil {
		return io.Discard
	}
	return h.a.stderr
}

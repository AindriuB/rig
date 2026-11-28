package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/AindriuB/rig/internal/hookstate"
	"github.com/AindriuB/rig/internal/store"
)

const (
	hooksStatusUsage = "usage: rig hooks status [--json] [--repo <dir>]"
	hooksRetryUsage  = "usage: rig hooks retry <hook> [--repo <dir>]"
	hooksSkipUsage   = "usage: rig hooks skip <hook> <event> [--repo <dir>]"

	// runnerStale is how old a runner's last heartbeat may be before the
	// runner is not counted as running.
	runnerStale = 2 * time.Minute
)

// hookCtx is one queued hook's state file and lease directory.
type hookCtx struct {
	name string
	path string
	dir  string
}

// queuedHook finds the workflow's queued hook called name.
func (a *app) queuedHook(ctx context.Context, w *work, name string) (hookCtx, bool, error) {
	common, err := w.repo.CommonDir(ctx)
	if err != nil {
		return hookCtx{}, false, err
	}
	for _, i := range w.wf.QueuedHooks() {
		if w.wf.HookName(i) == name {
			p := hookstate.Path(common, w.wf.Name, name)
			return hookCtx{name: name, path: p, dir: filepath.Dir(p)}, true, nil
		}
	}
	return hookCtx{}, false, nil
}

type parkedOut struct {
	Task     string `json:"task"`
	Event    string `json:"event"`
	Attempts int    `json:"attempts"`
	Reason   string `json:"reason"`
}

type hookOut struct {
	Name      string      `json:"name"`
	Runner    string      `json:"runner"`
	Pid       int         `json:"pid,omitempty"`
	Heartbeat *time.Time  `json:"heartbeat,omitempty"`
	Started   bool        `json:"started"`
	Parked    []parkedOut `json:"parked"`
}

func (a *app) hooksStatus(ctx context.Context, args []string) int {
	fs := newFlags()
	repoFlag, asJSON := fs.str("--repo"), fs.boolean("--json")
	pos, err := fs.parse(args)
	if err != nil || len(pos) > 0 {
		return a.fail("hooks status", "%s", hooksStatusUsage)
	}
	w, err := a.openWorkPlain(ctx, *repoFlag, true)
	if err != nil {
		return a.storeFail("hooks status", err)
	}
	out := []hookOut{}
	for _, i := range w.wf.QueuedHooks() {
		h, _, err := a.queuedHook(ctx, w, w.wf.HookName(i))
		if err != nil {
			return a.fail("hooks status", "%v", err)
		}
		st, err := hookstate.Load(h.path)
		if err != nil {
			return a.fail("hooks status", "%v", err)
		}
		o := hookOut{Name: h.name, Runner: "none", Parked: []parkedOut{}}
		holder, live, err := hookstate.Inspect(h.dir, h.name)
		if err != nil {
			return a.fail("hooks status", "%v", err)
		}
		if holder != nil && live {
			o.Runner, o.Pid = "running", holder.PID
			beat := holder.Beat
			o.Heartbeat = &beat
		}
		if st != nil {
			o.Started = st.Cursor != nil
			type row struct {
				p  parkedOut
				at time.Time
			}
			var rows []row
			for key, m := range st.Parked {
				slug := "-"
				if key != "" {
					slug = key
					if t, err := w.st.Task(ctx, key); err == nil {
						slug = t.Slug
					}
				}
				for id, p := range m {
					rows = append(rows, row{parkedOut{slug, id, p.Attempts, p.Reason}, p.At})
				}
			}
			sort.Slice(rows, func(x, y int) bool {
				if !rows[x].at.Equal(rows[y].at) {
					return rows[x].at.Before(rows[y].at)
				}
				if rows[x].p.Task != rows[y].p.Task {
					return rows[x].p.Task < rows[y].p.Task
				}
				return rows[x].p.Event < rows[y].p.Event
			})
			for _, r := range rows {
				o.Parked = append(o.Parked, r.p)
			}
		}
		out = append(out, o)
	}
	if *asJSON {
		b, _ := json.Marshal(struct {
			Hooks []hookOut `json:"hooks"`
		}{out})
		fmt.Fprintln(a.stdout, string(b))
		return 0
	}
	for _, o := range out {
		runner := "none"
		if o.Runner == "running" {
			runner = fmt.Sprintf("running (pid %d, last heartbeat %s)", o.Pid, o.Heartbeat.Format(time.RFC3339))
		}
		started := "not started"
		if o.Started {
			started = "started"
		}
		fmt.Fprintf(a.stdout, "%s: runner %s, %s, %d parked\n", o.Name, runner, started, len(o.Parked))
		for _, p := range o.Parked {
			fmt.Fprintf(a.stdout, "  %s  %s  attempts %d  %s\n", p.Task, p.Event, p.Attempts, p.Reason)
		}
	}
	return 0
}

// hooksRetry unparks every parked event of a hook.
func (a *app) hooksRetry(ctx context.Context, args []string) int {
	fs := newFlags()
	repoFlag := fs.str("--repo")
	pos, err := fs.parse(args)
	if err != nil || len(pos) != 1 {
		return a.fail("hooks retry", "%s", hooksRetryUsage)
	}
	w, err := a.openWorkPlain(ctx, *repoFlag, true)
	if err != nil {
		return a.storeFail("hooks retry", err)
	}
	h, ok, err := a.queuedHook(ctx, w, pos[0])
	if err != nil {
		return a.fail("hooks retry", "%v", err)
	}
	if !ok {
		fmt.Fprintf(a.stderr, "rig hooks retry: no queued hook named %q\n", pos[0])
		return 1
	}
	n := 0
	// Removing the entries resets their attempts: the runner parks afresh.
	// Update is atomic, so a running runner's own change is not lost.
	if st, err := hookstate.Load(h.path); err != nil {
		return a.fail("hooks retry", "%v", err)
	} else if st == nil {
		fmt.Fprintf(a.stdout, "%s: nothing parked\n", h.name)
		return 0
	}
	err = hookstate.Update(h.path, func(s *hookstate.State) error {
		for _, m := range s.Parked {
			n += len(m)
		}
		s.Parked = map[string]map[string]hookstate.Parked{}
		return nil
	})
	if err != nil {
		return a.fail("hooks retry", "%v", err)
	}
	fmt.Fprintf(a.stdout, "%s: released %d parked events\n", h.name, n)
	return 0
}

// hooksSkip drops one parked event and moves the hook's cursor past it, as
// the runner does after a success. The runner rewrites the whole cursor, so
// skip takes the hook's lease: a live runner makes it refuse rather than
// lose the move.
func (a *app) hooksSkip(ctx context.Context, args []string) int {
	fs := newFlags()
	repoFlag := fs.str("--repo")
	pos, err := fs.parse(args)
	if err != nil || len(pos) != 2 {
		return a.fail("hooks skip", "%s", hooksSkipUsage)
	}
	w, err := a.openWorkPlain(ctx, *repoFlag, true)
	if err != nil {
		return a.storeFail("hooks skip", err)
	}
	h, ok, err := a.queuedHook(ctx, w, pos[0])
	if err != nil {
		return a.fail("hooks skip", "%v", err)
	}
	if !ok {
		fmt.Fprintf(a.stderr, "rig hooks skip: no queued hook named %q\n", pos[0])
		return 1
	}
	event := pos[1]
	notParked := func() int {
		fmt.Fprintf(a.stderr, "rig hooks skip: event %s is not parked for hook %s\n", event, h.name)
		return 1
	}
	st, err := hookstate.Load(h.path)
	if err != nil {
		return a.fail("hooks skip", "%v", err)
	}
	if st == nil {
		return notParked()
	}
	key, found := "", false
	for k, m := range st.Parked {
		if _, has := m[event]; has && (!found || k < key) {
			key, found = k, true
		}
	}
	if !found || st.Cursor == nil {
		return notParked()
	}
	feeder, ok := w.st.(store.Feeder)
	if !ok {
		return a.fail("hooks skip", "this store cannot list events across tasks")
	}
	lease, err := hookstate.Acquire(h.dir, h.name)
	var held *hookstate.HeldError
	if errors.As(err, &held) {
		fmt.Fprintf(a.stderr, "rig hooks skip: %s: %v; stop that runner first, it would undo the skip\n", h.name, held)
		return 1
	}
	if err != nil {
		return a.fail("hooks skip", "%v", err)
	}
	defer lease.Release()
	log := logName(store.FeedItem{Task: taskFor(key)})
	pos2, err := skipPos(ctx, feeder, st.Cursor, log, event)
	if err != nil {
		fmt.Fprintf(a.stderr, "rig hooks skip: %v\n", err)
		return 1
	}
	err = hookstate.Update(h.path, func(s *hookstate.State) error {
		if _, has := s.Parked[key][event]; !has {
			return fmt.Errorf("event %s is no longer parked", event)
		}
		c := cloneCursor(s.Cursor)
		c.Logs[log] = pos2
		s.Cursor = c
		delete(s.Parked[key], event)
		if len(s.Parked[key]) == 0 {
			delete(s.Parked, key)
		}
		return nil
	})
	if err != nil {
		return a.fail("hooks skip", "%v", err)
	}
	fmt.Fprintf(a.stdout, "%s: skipped event %s\n", h.name, event)
	return 0
}

// taskFor is a stand-in task carrying only an id, so logName names its log.
func taskFor(id string) *store.Task {
	if id == "" {
		return nil
	}
	return &store.Task{ID: id}
}

// skipPos is the cursor position of log after event, found as the runner
// advances: the old tip with one more event counted, or the new tip when the
// event ends what the feed read.
func skipPos(ctx context.Context, f store.Feeder, cur *store.Cursor, log, event string) (store.LogPos, error) {
	res, err := f.Feed(ctx, cur)
	if err != nil {
		return store.LogPos{}, err
	}
	old, seen := cur.Logs[log]
	base := 0
	if seen {
		base = old.Count
	}
	var items []store.FeedItem
	for _, it := range res.Items {
		if logName(it) == log {
			items = append(items, it)
		}
	}
	for k, it := range items {
		if it.Event.ID != event {
			continue
		}
		if k == len(items)-1 {
			return res.Next.Logs[log], nil
		}
		return store.LogPos{Tip: old.Tip, Count: base + k + 1}, nil
	}
	return store.LogPos{}, fmt.Errorf("event %s is not among the undelivered events of its log", event)
}

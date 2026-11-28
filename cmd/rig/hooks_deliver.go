package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/AindriuB/rig/internal/hookstate"
	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/store/gitref"
	"github.com/AindriuB/rig/internal/workflow"
)

const (
	// deliverAttempts is how many times one event is tried before it is parked.
	deliverAttempts = 5
	// deliverWaitFirst and deliverWaitCap bound the wait between attempts.
	deliverWaitFirst = time.Second
	deliverWaitCap   = 5 * time.Minute
)

// Seams the tests replace.
var (
	// deliverRunner makes the runner queued hooks run through; dir is the
	// repository root.
	deliverRunner = func(dir string) workflow.Runner { return workflow.ProcRunner{Dir: dir} }
	// deliverSleep waits d, or returns false when ctx ends first.
	deliverSleep = func(ctx context.Context, d time.Duration) bool {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			return true
		}
	}
	// leaseTouchEvery is how often a held lease's heartbeat is rewritten.
	leaseTouchEvery = 30 * time.Second
)

// retryWait is the wait after the n-th failed attempt (n from 1): 1 s,
// doubling, never more than 5 min.
func retryWait(n int) time.Duration {
	d := deliverWaitFirst
	for i := 1; i < n && d < deliverWaitCap; i++ {
		d *= 2
	}
	return min(d, deliverWaitCap)
}

// hooksDeliver runs the workflow's queued hooks: each gets every event it
// matches at least once, in order within a task. One pass, or with --follow
// a pass every second until interrupted.
func (a *app) hooksDeliver(ctx context.Context, args []string) int {
	fs := newFlags()
	repoFlag, follow := fs.str("--repo"), fs.boolean("--follow")
	pos, err := fs.parse(args)
	if err != nil || len(pos) > 0 {
		return a.fail("hooks deliver", "%s", hooksDeliverUsage)
	}
	// The runner's own reads need no hooks, and it never runs inline hooks.
	w, err := a.openWorkPlain(ctx, *repoFlag, true)
	if err != nil {
		return a.storeFail("hooks deliver", err)
	}
	feeder, ok := w.st.(store.Feeder)
	if !ok {
		return a.fail("hooks deliver", "this store cannot list events across tasks")
	}
	common, err := w.repo.CommonDir(ctx)
	if err != nil {
		return a.fail("hooks deliver", "%v", err)
	}
	root := w.dir
	if wts, err := w.repo.Worktrees(ctx); err == nil && len(wts) > 0 && !wts[0].Bare {
		// git prints forward slashes on Windows; hooks get the native form.
		root = filepath.FromSlash(wts[0].Path)
	}
	d := &deliverer{a: a, wf: w.wf, feeder: feeder, common: common, runner: deliverRunner(root),
		leases: map[string]*hookstate.Lease{}, heldBy: map[string]bool{}, held: map[string]map[string]bool{},
		errSeen: map[string]string{}}
	stop := make(chan struct{})
	beat := make(chan struct{})
	go func() { defer close(beat); d.heartbeat(stop) }()
	defer func() { close(stop); <-beat; d.releaseAll() }()
	for {
		failed := d.pass(ctx)
		if !*follow || ctx.Err() != nil {
			if failed && ctx.Err() == nil {
				return 1
			}
			return 0
		}
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(followEvery):
		}
	}
}

// deliverer is one runner: the leases it holds and what it has warned about.
type deliverer struct {
	a      *app
	wf     *workflow.Workflow
	feeder store.Feeder
	common string
	runner workflow.Runner

	mu      sync.Mutex // guards leases and a.stderr
	leases  map[string]*hookstate.Lease
	heldBy  map[string]bool            // hooks a live runner holds, already warned about
	held    map[string]map[string]bool // per hook, the logs held back, already warned about
	errSeen map[string]string          // per hook, the last error text logged
}

func (d *deliverer) logf(format string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	fmt.Fprintf(d.a.stderr, "rig hooks deliver: "+format+"\n", args...)
}

// heartbeat rewrites every held lease's beat until stop closes.
func (d *deliverer) heartbeat(stop <-chan struct{}) {
	t := time.NewTicker(leaseTouchEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			d.mu.Lock()
			for name, l := range d.leases {
				if err := l.Touch(); err != nil {
					fmt.Fprintf(d.a.stderr, "rig hooks deliver: %s: %v\n", name, err)
					if errors.Is(err, hookstate.ErrTakenOver) {
						// Not ours any more: the next pass must Acquire again.
						delete(d.leases, name)
					}
				}
			}
			d.mu.Unlock()
		}
	}
}

func (d *deliverer) releaseAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for name, l := range d.leases {
		if err := l.Release(); err != nil {
			fmt.Fprintf(d.a.stderr, "rig hooks deliver: %s: release lease: %v\n", name, err)
		}
		delete(d.leases, name)
	}
}

// pass delivers every queued hook once over. A hook's error is logged (once
// while its text is unchanged) and the other hooks still run; failed reports
// whether any hook failed.
func (d *deliverer) pass(ctx context.Context) (failed bool) {
	for _, i := range d.wf.QueuedHooks() {
		if ctx.Err() != nil {
			return failed
		}
		name := d.wf.HookName(i)
		err := d.hook(ctx, i)
		if err == nil {
			delete(d.errSeen, name)
			continue
		}
		if ctx.Err() != nil {
			return failed
		}
		failed = true
		if d.errSeen[name] != err.Error() {
			d.errSeen[name] = err.Error()
			d.logf("%s: %v", name, err)
		}
	}
	return failed
}

func cloneCursor(c *store.Cursor) *store.Cursor {
	n := store.NewCursor()
	if c != nil {
		for k, v := range c.Logs {
			n.Logs[k] = v
		}
	}
	return n
}

// logItems is one log's items in order.
type logItems struct {
	name  string
	items []store.FeedItem
}

func logName(it store.FeedItem) string {
	if it.Task == nil {
		return gitref.NotesRef
	}
	return gitref.TasksPrefix + it.Task.ID
}

func taskKey(it store.FeedItem) string {
	if it.Task == nil {
		return ""
	}
	return it.Task.ID
}

// lease takes hook name's lease unless this runner holds it already. It
// returns false, after one warning, when a live runner holds it.
func (d *deliverer) lease(dir, name string) (bool, error) {
	d.mu.Lock()
	_, have := d.leases[name]
	d.mu.Unlock()
	if have {
		return true, nil
	}
	l, err := hookstate.Acquire(dir, name)
	var held *hookstate.HeldError
	if errors.As(err, &held) {
		if !d.heldBy[name] {
			d.heldBy[name] = true
			d.logf("%s: skipped: %v", name, held)
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	delete(d.heldBy, name)
	d.mu.Lock()
	d.leases[name] = l
	d.mu.Unlock()
	return true, nil
}

// hook delivers one queued hook's pending events.
func (d *deliverer) hook(ctx context.Context, i int) error {
	name := d.wf.HookName(i)
	h := d.wf.Hooks[i]
	path := hookstate.Path(d.common, d.wf.Name, name)
	if ok, err := d.lease(filepath.Dir(path), name); !ok || err != nil {
		return err
	}
	st, err := hookstate.LoadLocked(path)
	if err != nil {
		return err
	}
	var cur *store.Cursor
	parked := map[string]map[string]hookstate.Parked{}
	if st != nil {
		cur = st.Cursor
		for k, v := range st.Parked {
			parked[k] = v
		}
	}
	if cur == nil && h.From == "start" {
		cur = store.NewCursor()
	}
	res, err := d.feeder.Feed(ctx, cur)
	if err != nil {
		return err
	}
	d.warnHeld(name, res.Held)
	if cur == nil {
		// A new hook starts at the tips now.
		return d.saveCursor(path, res.Next)
	}
	work := cloneCursor(cur)
	dirty := false
	var groups []*logItems
	byLog := map[string]*logItems{}
	for _, it := range res.Items {
		n := logName(it)
		g := byLog[n]
		if g == nil {
			g = &logItems{name: n}
			byLog[n] = g
			groups = append(groups, g)
		}
		g.items = append(g.items, it)
	}
	for n, pos := range res.Next.Logs {
		if byLog[n] == nil && work.Logs[n] != pos {
			work.Logs[n] = pos
			dirty = true
		}
	}
	for _, g := range groups {
		old, seen := cur.Logs[g.name]
		base := 0
		if seen {
			base = old.Count
		}
	items:
		for k, it := range g.items {
			key := taskKey(it)
			if len(parked[key]) > 0 {
				break // held back until its parked event is dealt with
			}
			out := d.deliver(ctx, i, it)
			switch out.kind {
			case outAborted:
				return nil
			case outParked:
				if err := d.park(path, key, it.Event.ID, out.attempts, out.reason); err != nil {
					return err
				}
				if parked[key] == nil {
					parked[key] = map[string]hookstate.Parked{}
				}
				parked[key][it.Event.ID] = hookstate.Parked{Attempts: out.attempts, Reason: out.reason}
				d.logf("%s: parked event %s: %s", name, it.Event.ID, out.reason)
				break items
			}
			// A prefix of the log is done. Its tip stays the old one (or none)
			// so the feed reads the rest again; only a whole log takes the
			// new tip.
			pos := store.LogPos{Tip: old.Tip, Count: base + k + 1}
			if k == len(g.items)-1 {
				pos = res.Next.Logs[g.name]
			}
			work.Logs[g.name] = pos
			dirty = true
			if out.kind == outDelivered {
				if err := d.saveCursor(path, work); err != nil {
					return err
				}
				dirty = false
			}
		}
	}
	if dirty {
		return d.saveCursor(path, work)
	}
	return nil
}

func (d *deliverer) saveCursor(path string, c *store.Cursor) error {
	c = cloneCursor(c)
	return hookstate.Update(path, func(s *hookstate.State) error {
		s.Cursor = c
		return nil
	})
}

func (d *deliverer) park(path, key, id string, attempts int, reason string) error {
	at := d.a.now().UTC()
	return hookstate.Update(path, func(s *hookstate.State) error {
		if s.Parked[key] == nil {
			s.Parked[key] = map[string]hookstate.Parked{}
		}
		s.Parked[key][id] = hookstate.Parked{Attempts: attempts, Reason: reason, At: at}
		return nil
	})
}

// warnHeld says once when a log becomes held and once when it is released.
func (d *deliverer) warnHeld(hook string, held []store.HeldLog) {
	prev := d.held[hook]
	now := map[string]bool{}
	for _, h := range held {
		now[h.Name] = true
		if !prev[h.Name] {
			d.logf("%s: %s held: %d unreadable lines in its log", hook, h.Name, h.Skipped)
		}
	}
	for n := range prev {
		if !now[n] {
			d.logf("%s: %s no longer held", hook, n)
		}
	}
	d.held[hook] = now
}

type outKind int

const (
	outDelivered outKind = iota
	outSkipped           // no run needed: another workflow, or no selector matched
	outParked
	outAborted // the runner was interrupted
)

type outcome struct {
	kind     outKind
	attempts int
	reason   string
}

// deliver runs hook i for one event, with retries.
func (d *deliverer) deliver(ctx context.Context, i int, it store.FeedItem) outcome {
	name := d.wf.HookName(i)
	if it.Task != nil && it.Task.Workflow.Name != d.wf.Name {
		return outcome{kind: outSkipped}
	}
	ev := it.Event
	match, err := d.wf.QueuedMatches(i, ev)
	if err != nil {
		return outcome{kind: outParked, reason: err.Error()}
	}
	if !match {
		return outcome{kind: outSkipped}
	}
	depth := 0
	if ev.Cause != nil {
		depth = ev.Cause.Depth
	}
	if max, capped := d.wf.QueuedMaxDepth(i); capped && depth > max {
		return outcome{kind: outParked, reason: fmt.Sprintf("depth %d exceeds maxDepth %d", depth, max)}
	}
	var last error
	for n := 1; n <= deliverAttempts; n++ {
		last = d.wf.RunQueued(ctx, d.runner, i, ev, it.Task)
		if last == nil {
			return outcome{kind: outDelivered}
		}
		if ctx.Err() != nil {
			return outcome{kind: outAborted}
		}
		d.logf("%s: event %s attempt %d of %d failed: %v", name, ev.ID, n, deliverAttempts, last)
		if n < deliverAttempts && !deliverSleep(ctx, retryWait(n)) {
			return outcome{kind: outAborted}
		}
	}
	return outcome{kind: outParked, attempts: deliverAttempts, reason: last.Error()}
}

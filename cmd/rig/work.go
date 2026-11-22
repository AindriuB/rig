package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/AindriuB/rig/internal/config"
	"github.com/AindriuB/rig/internal/git"
	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/store/gitref"
	"github.com/AindriuB/rig/internal/workflow"
)

// work is what a store command works with: the repository, its task store
// and the workflow in effect.
type work struct {
	dir  string
	repo git.Repo
	st   store.Store
	wf   *workflow.Workflow
	src  workflow.Source
}

// loadWorkflow returns the workflow in effect for the repository at dir:
// $RIG_WORKFLOW if set, else the config key "workflow" (read from the global
// file only, as the config package enforces), else "basic". A workflow that
// parses but breaks the format's rules comes back with an
// *workflow.InvalidError.
func (a *app) loadWorkflow(ctx context.Context, dir string) (*workflow.Workflow, workflow.Source, error) {
	name := strings.TrimSpace(a.getenv("RIG_WORKFLOW"))
	if name == "" {
		remote, err := git.Repo{Dir: dir}.RemoteURL(ctx, "origin")
		if err != nil {
			remote = ""
		}
		l, err := config.Open(config.Options{Remote: remote})
		if err != nil {
			return nil, workflow.Source{}, fmt.Errorf("read config: %w", err)
		}
		if v, ok := l.Get(config.KeyWorkflow); ok {
			name, _ = v.Value.(string)
		}
		if name == "" {
			name = config.DefaultWorkflow
		}
	}
	return workflow.Load(name)
}

// openWork opens the store and workflow for --repo (or the cwd). With
// needValid, a workflow that breaks the format's rules is an error listing
// its problems. Without it, such a workflow is returned in the work together
// with its *workflow.InvalidError, for commands that report on it; any other
// error returns no work.
func (a *app) openWork(ctx context.Context, repoFlag string, needValid bool) (*work, error) {
	dir, err := a.repoDir(repoFlag)
	if err != nil {
		return nil, err
	}
	wf, src, wfErr := a.loadWorkflow(ctx, dir)
	var invalid *workflow.InvalidError
	if wfErr != nil && (needValid || wf == nil || !errors.As(wfErr, &invalid)) {
		return nil, wfErr
	}
	open := a.store
	if open == nil {
		open = a.openStore
	}
	st, err := open(ctx, dir)
	if err != nil {
		return nil, err
	}
	return &work{dir: dir, repo: git.Repo{Dir: dir}, st: st, wf: wf, src: src}, wfErr
}

// branch is the task's branch: its own, or else the workflow's template.
func (w *work) branch(t store.Task) string {
	if t.Branch != "" {
		return t.Branch
	}
	return w.wf.BranchFor(t)
}

// head returns the tip of the task's branch, or "" when the branch does not
// exist.
func (w *work) head(ctx context.Context, t store.Task) (string, error) {
	b := w.branch(t)
	if b == "" {
		return "", nil
	}
	oid, ok, err := w.repo.ResolveRef(ctx, "refs/heads/"+b)
	if err != nil {
		return "", fmt.Errorf("branch head of %s: %w", t.Short, err)
	}
	if !ok {
		return "", nil
	}
	return oid, nil
}

// view returns the task as rig shows it, with its branch head now.
func (w *work) view(ctx context.Context, t store.Task) (workflow.TaskView, error) {
	head, err := w.head(ctx, t)
	if err != nil {
		return workflow.TaskView{}, err
	}
	return w.wf.View(t, head), nil
}

// env is what gates need: branch heads, other tasks and a runner for command
// gates, which run in the repository's directory.
func (w *work) env() workflow.Env {
	return workflow.Env{
		HeadSha: w.head,
		Task:    w.st.Task,
		Runner:  workflow.ProcRunner{Dir: w.dir},
	}
}

// refusal is a move the workflow or a gate refused; nothing was written.
type refusal struct {
	reason string
	gates  []workflow.GateResult
}

func (r *refusal) Error() string { return r.reason }

// errMoved reports a task that changed between the gate check and the write.
var errMoved = errors.New("the task changed since it was read")

// moveRetries is how many times move re-reads and re-checks a task that
// changed under it before giving up with gitref.ErrConflict.
const moveRetries = 3

// moveResult is what a move did.
type moveResult struct {
	view       workflow.TaskView
	gates      []workflow.GateResult
	hookErrors []error
}

// move takes the task ref names to the state to through one transition, its
// gates checked outside the store write. A refused move is a *refusal and
// writes nothing. Hooks run after the write; their failures are returned in
// the result, and the transition stands.
func (w *work) move(ctx context.Context, ref, to string, args workflow.Args) (moveResult, error) {
	for try := 0; ; try++ {
		res, err := w.moveOnce(ctx, ref, to, args)
		if !errors.Is(err, errMoved) {
			return res, err
		}
		if try == moveRetries {
			return moveResult{}, fmt.Errorf("move %s to %s: %w", ref, to, gitref.ErrConflict)
		}
	}
}

// moveOnce is one attempt at move; it returns errMoved when the task changed
// between reading it and writing.
func (w *work) moveOnce(ctx context.Context, ref, to string, args workflow.Args) (moveResult, error) {
	t, err := w.st.Task(ctx, ref)
	if err != nil {
		return moveResult{}, err
	}
	findings, err := w.st.Findings(ctx, t.ID)
	if err != nil {
		return moveResult{}, err
	}
	head, err := w.head(ctx, t)
	if err != nil {
		return moveResult{}, err
	}
	from := w.wf.Effective(t, head)
	snap, err := snapshot(t, findings)
	if err != nil {
		return moveResult{}, err
	}

	if to == workflow.Previous {
		if t.Previous == "" {
			return moveResult{}, &refusal{reason: fmt.Sprintf("%s has no previous state", t.Short)}
		}
		to = t.Previous
	}
	tr, found := pickTransition(w.wf.Transitions(from), to, t.Previous)
	if !found {
		return moveResult{}, &refusal{reason: fmt.Sprintf("no transition from %s to %s", from, to)}
	}

	env, deps := w.recordingEnv()
	gates := w.wf.Check(ctx, env, t, findings, tr, args)
	for _, g := range gates {
		if !g.OK {
			return moveResult{}, &refusal{reason: fmt.Sprintf("%s → %s refused by its gates", from, to), gates: gates}
		}
	}

	// A dependsIn gate read other tasks and their branch heads. Read them
	// again now, outside the Update callback (a store callback must not call
	// the store), and start over if any differs from what the gates used.
	// A dependency can still change between this re-read and the write, as
	// the branch head can between its check in the callback and the write;
	// that window is not closed.
	if err := w.recheckDeps(ctx, deps); err != nil {
		return moveResult{}, err
	}

	var written []store.Event
	after, err := w.st.Update(ctx, t.ID, func(cur store.Task, curFindings []store.Finding) ([]store.Event, error) {
		// The gates passed on what was read above; anything that changed
		// since, the task, its findings or its branch head, means they must
		// be checked again.
		now, err := snapshot(cur, curFindings)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(now, snap) {
			return nil, errMoved
		}
		curHead, err := w.head(ctx, cur)
		if err != nil {
			return nil, err
		}
		if curHead != head {
			return nil, errMoved
		}
		evs, err := w.wf.Events(cur, head, tr, to, args)
		written = evs
		return evs, err
	})
	if err != nil {
		return moveResult{}, err
	}

	var hookErrs []error
	if len(written) > 0 {
		ev := w.stamped(ctx, after.ID, written[0])
		hookErrs = w.wf.RunHooks(ctx, w.env().Runner, ev, after, head)
	}
	return moveResult{view: w.wf.View(after, head), gates: gates, hookErrors: hookErrs}, nil
}

// depReads is what the gates of one move read of other tasks: each task's
// JSON as returned for a reference, and each branch head by task ID.
type depReads struct {
	tasks map[string][]byte
	heads map[string]string
	// changed is set when two reads of the same task or head during one
	// Check disagree: no single state satisfied every gate.
	changed bool
}

// recordingEnv is env, noting every other task and branch head the gates read.
func (w *work) recordingEnv() (workflow.Env, *depReads) {
	reads := &depReads{tasks: map[string][]byte{}, heads: map[string]string{}}
	env := w.env()
	task, head := env.Task, env.HeadSha
	env.Task = func(ctx context.Context, ref string) (store.Task, error) {
		t, err := task(ctx, ref)
		if err == nil {
			b, merr := json.Marshal(t)
			if merr != nil {
				return store.Task{}, fmt.Errorf("snapshot of %s: %w", ref, merr)
			}
			if was, ok := reads.tasks[ref]; ok && !bytes.Equal(was, b) {
				reads.changed = true
			}
			reads.tasks[ref] = b
		}
		return t, err
	}
	env.HeadSha = func(ctx context.Context, t store.Task) (string, error) {
		h, err := head(ctx, t)
		if err == nil {
			if was, ok := reads.heads[t.ID]; ok && was != h {
				reads.changed = true
			}
			reads.heads[t.ID] = h
		}
		return h, err
	}
	return env, reads
}

// recheckDeps reads again what reads recorded and returns errMoved if any of
// it changed.
func (w *work) recheckDeps(ctx context.Context, reads *depReads) error {
	if reads.changed {
		return errMoved
	}
	for ref, was := range reads.tasks {
		cur, err := w.st.Task(ctx, ref)
		if err != nil {
			return err
		}
		b, err := json.Marshal(cur)
		if err != nil {
			return fmt.Errorf("snapshot of %s: %w", ref, err)
		}
		if !bytes.Equal(b, was) {
			return errMoved
		}
		if h, ok := reads.heads[cur.ID]; ok {
			now, err := w.head(ctx, cur)
			if err != nil {
				return err
			}
			if now != h {
				return errMoved
			}
		}
	}
	return nil
}

// pickTransition returns the transition among candidates that goes to to: an
// explicit one first, so its gates apply, else one to $previous when the
// task's previous state is to.
func pickTransition(candidates []workflow.Transition, to, previous string) (workflow.Transition, bool) {
	for _, c := range candidates {
		if c.To == to {
			return c, true
		}
	}
	for _, c := range candidates {
		if c.To == workflow.Previous && previous != "" && previous == to {
			return c, true
		}
	}
	return workflow.Transition{}, false
}

// snapshot encodes a task and its findings, to tell whether either changed
// between the gate check and the write.
func snapshot(t store.Task, findings []store.Finding) ([]byte, error) {
	if len(findings) == 0 {
		findings = nil
	}
	b, err := json.Marshal(struct {
		Task     store.Task      `json:"task"`
		Findings []store.Finding `json:"findings"`
	}{t, findings})
	if err != nil {
		return nil, fmt.Errorf("snapshot of %s: %w", t.Short, err)
	}
	return b, nil
}

// stamped returns the logged copy of ev, the transition move just wrote, or
// ev itself if the log cannot be read.
func (w *work) stamped(ctx context.Context, id string, ev store.Event) store.Event {
	log, err := w.st.Events(ctx, id)
	if err != nil {
		return ev
	}
	for i := len(log) - 1; i >= 0; i-- {
		if log[i].Kind == ev.Kind && bytes.Equal(log[i].Data, ev.Data) {
			return log[i]
		}
	}
	return ev
}

// storeFail reports err from a store command and returns its exit code: 1
// for a refusal, printed with its failing gates; 2 for anything else (no
// such task, an ambiguous reference with its matches, a write conflict, an
// invalid workflow, a store or git error).
func (a *app) storeFail(cmd string, err error) int {
	var r *refusal
	if errors.As(err, &r) {
		fmt.Fprintf(a.stderr, "rig %s: %s\n", cmd, r.reason)
		for _, g := range r.gates {
			if !g.OK {
				fmt.Fprintf(a.stderr, "  %s: %s\n", g.Gate, g.Message)
			}
		}
		return 1
	}
	return a.fail(cmd, "%v", err)
}

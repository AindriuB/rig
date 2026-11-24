package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/tracker"
)

// splitRef splits `<key>` or `<kind>:<key>` into the kind and the key.
// prefixed says whether a kind was written, so ":12" is a (bad) kind and not
// the absence of one.
func splitRef(ref string) (kind, key string, prefixed bool) {
	kind, key, prefixed = strings.Cut(ref, ":")
	if !prefixed {
		return "", ref, false
	}
	return kind, key, true
}

// taskNewFrom is `rig task new --from`: a thin wrapper over intake, the
// function `rig tracker pull` uses too.
func (a *app) taskNewFrom(ctx context.Context, repo, from, state, slug string, rawFields []string, asJSON bool) int {
	const cmd = "task new"
	w, err := a.openWork(ctx, repo, true)
	if err != nil {
		return a.storeFail(cmd, err)
	}
	fields, err := parseFieldArgs(w.wf, rawFields, false)
	if err != nil {
		return a.fail(cmd, "%v", err)
	}
	// Before the tracker is read.
	startState, err := intakeState(w.wf, state)
	if err != nil {
		return a.storeFail(cmd, err)
	}
	tr, spec, err := a.selectTracker(ctx, w.dir)
	if err != nil {
		return a.fail(cmd, "%v", err)
	}
	kind, key, prefixed := splitRef(from)
	if prefixed && kind != spec.Kind {
		return a.fail(cmd, "--from %q: the tracker is %s", from, spec.Kind)
	}
	if err := tracker.ValidateKey(key); err != nil {
		return a.fail(cmd, "--from %q: %v", from, err)
	}
	item, err := intake(ctx, a, w.st, tr, w.wf, key, intakeOpts{State: startState, Slug: slug, Fields: fields, Link: spec.Link})
	if err != nil {
		return a.storeFail(cmd, err)
	}
	for _, warn := range item.Warnings {
		fmt.Fprintf(a.stderr, "rig %s: warning: %s\n", cmd, warn)
	}
	return a.printTask(ctx, cmd, w, item.Task, asJSON)
}

// taskParent is `rig task parent <task> <parent|none>`. A cycle or a parent
// that does not exist is a refusal (exit 1) and nothing is written.
func (a *app) taskParent(ctx context.Context, args []string) int {
	const cmd = "task parent"
	fs := newFlags()
	repo := fs.str("--repo")
	asJSON := fs.boolean("--json")
	pos, err := fs.parse(args)
	if err != nil || len(pos) != 2 {
		return a.fail(cmd, "usage: rig task parent <task> <parent|none> [--json] [--repo <dir>]")
	}
	w, t, err := a.openTask(ctx, *repo, pos[0])
	if err != nil {
		return a.storeFail(cmd, err)
	}
	parent := ""
	if pos[1] != "none" {
		p, err := w.st.Task(ctx, pos[1])
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrAmbiguous) {
			return a.storeFail(cmd, &refusal{reason: fmt.Sprintf("parent: %v", err)})
		}
		if err != nil {
			return a.storeFail(cmd, err)
		}
		parent = p.ID
	}
	// An Update callback must not call the store, so the tasks the check
	// needs are read first. Two writers on two tasks are not atomic across
	// tasks; the window is the one `task link` has.
	tasks, err := w.st.Tasks(ctx, store.TaskFilter{})
	if err != nil {
		return a.storeFail(cmd, err)
	}
	ev, err := store.NewEvent(store.KindParent, store.ParentData{Task: parent})
	if err != nil {
		return a.storeFail(cmd, err)
	}
	after, err := w.st.Update(ctx, t.ID, func(cur store.Task, _ []store.Finding) ([]store.Event, error) {
		if cur.Parent == parent {
			return nil, nil
		}
		if err := store.CheckParent(cur.ID, parent, tasks); err != nil {
			return nil, &refusal{reason: err.Error()}
		}
		return []store.Event{ev}, nil
	})
	if err != nil {
		return a.storeFail(cmd, err)
	}
	return a.printTask(ctx, cmd, w, after, *asJSON)
}

// taskLink is `rig task link <task> <ref>`: it links an existing task to an
// issue, then does one refresh. The uniqueness check is the store's; two
// writers linking one issue from two tasks are not atomic across tasks, and
// the window is small.
func (a *app) taskLink(ctx context.Context, args []string) int {
	const cmd = "task link"
	fs := newFlags()
	repo := fs.str("--repo")
	asJSON := fs.boolean("--json")
	pos, err := fs.parse(args)
	if err != nil || len(pos) != 2 {
		return a.fail(cmd, "usage: rig task link <task> [<kind>:]<key> [--json] [--repo <dir>]")
	}
	kind, key, prefixed := splitRef(pos[1])
	if err := tracker.ValidateKey(key); err != nil {
		return a.fail(cmd, "%q: %v", pos[1], err)
	}
	w, t, err := a.openTask(ctx, *repo, pos[0])
	if err != nil {
		return a.storeFail(cmd, err)
	}
	tr, spec, err := a.selectTracker(ctx, w.dir)
	if err != nil {
		return a.fail(cmd, "%v", err)
	}
	if prefixed && kind != spec.Kind {
		return a.fail(cmd, "%q: the tracker is %s", pos[1], spec.Kind)
	}
	ev, err := store.NewEvent(store.KindExternal, store.ExternalData{
		Kind: spec.Link.Kind, Host: spec.Link.Host, Project: spec.Link.Project, Key: key, Owned: w.wf.OwnedFields(),
	})
	if err != nil {
		return a.storeFail(cmd, err)
	}
	after, err := w.st.Update(ctx, t.ID, func(store.Task, []store.Finding) ([]store.Event, error) {
		return []store.Event{ev}, nil
	})
	if errors.Is(err, store.ErrLinked) {
		return a.storeFail(cmd, &refusal{reason: err.Error()})
	}
	if err != nil {
		return a.storeFail(cmd, err)
	}
	res := a.refreshOne(ctx, w, tr, spec, after, false)
	if res.Status != "updated" && res.Status != "unchanged" {
		why := res.Error
		if res.Status == "gone" {
			why = key + " was not found in the tracker"
		}
		return a.fail(cmd, "linked %s to %s, but the refresh failed: %s; the link stands, run `rig tracker refresh %s` to retry", after.Short, key, why, after.Short)
	}
	if after, err = w.st.Task(ctx, t.ID); err != nil {
		return a.storeFail(cmd, err)
	}
	return a.printTask(ctx, cmd, w, after, *asJSON)
}

// taskUnlink is `rig task unlink <task>`: it removes the link. The owned
// fields keep their last values and become the task's own.
func (a *app) taskUnlink(ctx context.Context, args []string) int {
	const cmd = "task unlink"
	fs := newFlags()
	repo := fs.str("--repo")
	asJSON := fs.boolean("--json")
	pos, err := fs.parse(args)
	if err != nil || len(pos) != 1 {
		return a.fail(cmd, "usage: rig task unlink <task> [--json] [--repo <dir>]")
	}
	w, t, err := a.openTask(ctx, *repo, pos[0])
	if err != nil {
		return a.storeFail(cmd, err)
	}
	ev, err := store.NewEvent(store.KindExternal, store.ExternalData{})
	if err != nil {
		return a.storeFail(cmd, err)
	}
	after, err := w.st.Update(ctx, t.ID, func(cur store.Task, _ []store.Finding) ([]store.Event, error) {
		if cur.External == nil {
			return nil, &refusal{reason: fmt.Sprintf("%s is not linked to an issue", cur.Short)}
		}
		return []store.Event{ev}, nil
	})
	if err != nil {
		return a.storeFail(cmd, err)
	}
	return a.printTask(ctx, cmd, w, after, *asJSON)
}

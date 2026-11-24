package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/AindriuB/rig/internal/tracker"
)

// splitRef splits `<key>` or `<kind>:<key>` into the kind ("" when absent)
// and the key.
func splitRef(ref string) (kind, key string) {
	if k, rest, ok := strings.Cut(ref, ":"); ok {
		return k, rest
	}
	return "", ref
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
	kind, key := splitRef(from)
	if kind != "" && kind != spec.Kind {
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

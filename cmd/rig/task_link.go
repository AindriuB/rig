package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/tracker"
	"github.com/AindriuB/rig/internal/workflow"
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

// listShape is the hierarchy flags of `rig task list`: at most one is given.
type listShape struct {
	parent      *string
	roots, tree *bool
}

func addListShape(fs *flagSet) listShape {
	return listShape{parent: fs.str("--parent"), roots: fs.boolean("--roots"), tree: fs.boolean("--tree")}
}

// parentGiven reports whether --parent is in args, even with an empty value.
func parentGiven(args []string) bool {
	return slices.ContainsFunc(args, func(s string) bool { return s == "--parent" || strings.HasPrefix(s, "--parent=") })
}

// count is how many of --parent, --roots and --tree were given.
func (s listShape) count(args []string) int {
	n := 0
	for _, on := range []bool{parentGiven(args), *s.roots, *s.tree} {
		if on {
			n++
		}
	}
	return n
}

func (s listShape) active(args []string) bool { return s.count(args) > 0 }

// shapeViews narrows and orders views, which are already filtered by state,
// as the hierarchy flag asks. With --tree the depths of the result are
// returned too; a view whose parent is not among views is a root.
func (a *app) shapeViews(ctx context.Context, w *work, s listShape, views []workflow.TaskView) ([]workflow.TaskView, []int, error) {
	out := []workflow.TaskView{}
	switch {
	case *s.roots:
		for _, v := range views {
			if v.Parent == "" {
				out = append(out, v)
			}
		}
	case *s.tree:
		out, depths := treeViews(views)
		return out, depths, nil
	default:
		p, err := w.st.Task(ctx, *s.parent)
		if err != nil {
			return nil, nil, err
		}
		for _, v := range views {
			if v.Parent == p.ID {
				out = append(out, v)
			}
		}
	}
	return out, nil, nil
}

// treeViews orders views parents first, each task's children right after it,
// and returns each one's depth.
func treeViews(views []workflow.TaskView) ([]workflow.TaskView, []int) {
	present := make(map[string]bool, len(views))
	kids := map[string][]int{}
	for _, v := range views {
		present[v.ID] = true
	}
	var roots []int
	for i, v := range views {
		if v.Parent == "" || !present[v.Parent] {
			roots = append(roots, i)
		} else {
			kids[v.Parent] = append(kids[v.Parent], i)
		}
	}
	out := make([]workflow.TaskView, 0, len(views))
	depths := make([]int, 0, len(views))
	seen := make([]bool, len(views))
	var walk func(i, depth int)
	walk = func(i, depth int) {
		if seen[i] {
			return
		}
		seen[i] = true
		out, depths = append(out, views[i]), append(depths, depth)
		for _, c := range kids[views[i].ID] {
			walk(c, depth+1)
		}
	}
	for _, i := range roots {
		walk(i, 0)
	}
	// Only a corrupt log has a parent cycle with no root: show it anyway.
	for i := range views {
		walk(i, 0)
	}
	return out, depths
}

// treeLineMD is one task as a Markdown bullet, without its parent.
func treeLineMD(v workflow.TaskView) string {
	line := fmt.Sprintf("- %s [%s] %s (%s)", v.Short, mdLine(v.State), mdLine(v.Title), mdLine(v.Slug))
	if v.External != nil && v.External.Key != "" {
		line += " — " + mdLine(v.External.Key)
	}
	return line + "\n"
}

// renderTreeMD renders `rig task list --tree --markdown`: children indented
// under their parent instead of marked with "←".
func renderTreeMD(views []workflow.TaskView, depths []int) string {
	var b strings.Builder
	b.WriteString("# Tasks\n\n")
	for i, v := range views {
		b.WriteString(strings.Repeat("  ", depths[i]) + treeLineMD(v))
	}
	if len(views) == 0 {
		b.WriteString("none\n")
	}
	return b.String()
}

// showChildren is `rig task show --children --json`: the Task and its children.
type showChildren struct {
	workflow.TaskView
	Children []workflow.TaskView `json:"children"`
}

// childViews returns the tasks whose parent is t, in the store's order.
func (a *app) childViews(ctx context.Context, w *work, t store.Task) ([]workflow.TaskView, error) {
	tasks, err := w.st.Tasks(ctx, store.TaskFilter{})
	if err != nil {
		return nil, err
	}
	out := []workflow.TaskView{}
	for _, c := range tasks {
		if c.Parent != t.ID {
			continue
		}
		v, err := w.view(ctx, c)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// renderChildrenMD is the "Children" section of `rig task show`.
func renderChildrenMD(kids []workflow.TaskView) string {
	var b strings.Builder
	b.WriteString("## Children\n\n")
	for _, v := range kids {
		b.WriteString(treeLineMD(v))
	}
	if len(kids) == 0 {
		b.WriteString("none\n")
	}
	return b.String()
}

// ownedRefusal is the refusal for a change to a field the tracker owns on a
// linked task, or nil when t is not linked or no owned field is in changed.
func ownedRefusal(t store.Task, changed map[string]any) error {
	if t.External == nil {
		return nil
	}
	owned := t.External.Owned
	var hit []string
	for _, n := range sortedNames(changed) {
		if slices.Contains(owned, n) {
			hit = append(hit, n)
		}
	}
	if len(hit) == 0 {
		return nil
	}
	return &refusal{reason: fmt.Sprintf("%s: owned by the %s issue %s; change it there, or run `rig task unlink %s` to make it the task's own",
		strings.Join(hit, ", "), t.External.Kind, t.External.Key, t.Short)}
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

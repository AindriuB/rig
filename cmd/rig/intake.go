package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/tracker"
	"github.com/AindriuB/rig/internal/workflow"
)

// intakeOpts are the choices a caller makes for one intake.
type intakeOpts struct {
	// State is the state the task starts in; "" means the first intake state.
	State string
	// Slug is the task's slug; "" means one made from the key and title.
	Slug string
	// Fields are extra task fields (task new --from --field). A field the
	// tracker owns is refused.
	Fields map[string]any
	// Link names the tracker the issue is in: the kind, host and project the
	// task's external link records.
	Link trackerLink
}

// intakeItem is the result of one intake.
type intakeItem struct {
	// Task is the new task, or the one already linked to the issue.
	Task store.Task
	// Key is the issue's key as the tracker gave it.
	Key string
	// Existing is set when the issue was already linked.
	Existing bool
	// Parent is the short id of the task's parent task, or "".
	Parent string
	// Warnings are capabilities the tracker lacks and the like.
	Warnings []string
}

// intakeState returns the state an intake starts in: state if it is an
// intake state of wf, the first one if state is empty, otherwise a refusal.
func intakeState(wf *workflow.Workflow, state string) (string, error) {
	states := wf.IntakeStates()
	if state == "" {
		return states[0], nil
	}
	if !slices.Contains(states, state) {
		return "", &refusal{reason: fmt.Sprintf("--state %q is not an intake state; use one of: %s", state, strings.Join(states, ", "))}
	}
	return state, nil
}

var slugJunk = regexp.MustCompile(`[^a-z0-9]+`)

// issueSlug makes a task slug from an issue: the key and the title, lower
// case, runs of anything outside a-z and 0-9 as one "-", at most maxSlug
// characters.
func issueSlug(key, title string) string {
	s := strings.Trim(slugJunk.ReplaceAllString(strings.ToLower(key+" "+title), "-"), "-")
	if len(s) > maxSlug {
		s = strings.Trim(s[:maxSlug], "-")
	}
	if s == "" {
		return "issue"
	}
	return s
}

// attrValue is the issue's value for a neutral attribute of the tracker map.
func attrValue(is tracker.Issue, attr string) any {
	list := func(l []string) []string {
		if l == nil {
			return []string{}
		}
		return slices.Clone(l)
	}
	switch attr {
	case workflow.AttrTitle:
		return is.Title
	case workflow.AttrDescription:
		return is.Description
	case workflow.AttrAcceptance:
		return list(is.Acceptance)
	case workflow.AttrPriority:
		return is.Priority
	case workflow.AttrAssignee:
		return is.Assignee
	case workflow.AttrLabels:
		return list(is.Labels)
	case workflow.AttrType:
		return is.Type
	}
	return nil
}

// ownedValues maps the issue onto the rig fields in owned. A field the
// workflow no longer maps is left out.
func ownedValues(wf *workflow.Workflow, is tracker.Issue, owned []string) map[string]any {
	out := map[string]any{}
	for _, f := range owned {
		if attr, ok := wf.AttributeFor(f); ok {
			out[f] = attrValue(is, attr)
		}
	}
	return out
}

// checkIssueKey validates a key the tracker returned, before it is used for
// anything.
func checkIssueKey(what, key string) error {
	if err := tracker.ValidateKey(key); err != nil {
		return fmt.Errorf("the tracker returned a bad %s key: %w", what, err)
	}
	return nil
}

// intake creates a task linked to the issue key, or returns the task already
// linked to it. Every key the tracker returns is validated before use. It
// never creates a parent: a parent already in the store is linked, otherwise
// the parent's key is kept on the link. `rig tracker pull` and `rig task new
// --from` both use it.
func intake(ctx context.Context, a *app, st store.Store, tr tracker.Tracker, wf *workflow.Workflow, key string, o intakeOpts) (intakeItem, error) {
	state, err := intakeState(wf, o.State)
	if err != nil {
		return intakeItem{}, err
	}
	if err := tracker.ValidateKey(key); err != nil {
		return intakeItem{}, err
	}
	if o.Slug != "" && (len(o.Slug) > maxSlug || !slugRE.MatchString(o.Slug)) {
		return intakeItem{}, fmt.Errorf("--slug must be 1 to %d characters of a-z, 0-9 and -", maxSlug)
	}
	owned := wf.OwnedFields()
	for name := range o.Fields {
		if slices.Contains(owned, name) {
			return intakeItem{}, fmt.Errorf("field %q is owned by the tracker", name)
		}
	}
	tasks, err := st.Tasks(ctx, store.TaskFilter{})
	if err != nil {
		return intakeItem{}, err
	}
	link := &store.External{Kind: o.Link.Kind, Host: o.Link.Host, Project: o.Link.Project, Key: key}
	if t, ok := store.Linked(link, tasks); ok {
		return a.existingItem(t, key, tasks), nil
	}

	issue, err := tr.Issue(ctx, key)
	if err != nil {
		return intakeItem{}, fmt.Errorf("%s: %w", key, err)
	}
	if err := checkIssueKey("issue", issue.Key); err != nil {
		return intakeItem{}, err
	}
	if !strings.EqualFold(issue.Key, key) {
		return intakeItem{}, fmt.Errorf("asked for %s and the tracker answered %s", key, issue.Key)
	}
	link.Key = issue.Key
	if t, ok := store.Linked(link, tasks); ok {
		return a.existingItem(t, issue.Key, tasks), nil
	}
	if strings.TrimSpace(issue.Title) == "" {
		return intakeItem{}, fmt.Errorf("%s has no title", issue.Key)
	}

	link.URL, link.Type, link.Owned = issue.URL, issue.Type, owned
	link.Rev = issue.Rev
	item := intakeItem{Key: issue.Key}
	n := store.NewTask{
		Title: issue.Title, Fields: map[string]any{},
		Workflow:       store.WorkflowRef{Name: wf.Name, Version: wf.Version},
		Initial:        state,
		BranchTemplate: wf.Branch,
		External:       link,
	}
	for name, v := range o.Fields {
		n.Fields[name] = v
	}
	n.Slug = o.Slug
	if n.Slug == "" {
		n.Slug = issueSlug(issue.Key, issue.Title)
	}
	values := ownedValues(wf, issue, owned)
	for name, v := range values {
		switch name {
		case "title":
			n.Title, _ = v.(string)
		case "body":
			n.Body, _ = v.(string)
		default:
			n.Fields[name] = v
		}
	}
	if strings.TrimSpace(n.Title) == "" {
		return intakeItem{}, fmt.Errorf("%s has no title", issue.Key)
	}
	n.Snapshot = values

	if p := issue.Parent; p != nil {
		if err := checkIssueKey("parent", p.Key); err != nil {
			return intakeItem{}, err
		}
		pl := &store.External{Kind: o.Link.Kind, Host: o.Link.Host, Project: o.Link.Project, Key: p.Key}
		if pt, ok := store.Linked(pl, tasks); ok {
			n.Parent, item.Parent = pt.ID, pt.Short
		} else {
			link.ParentKey = p.Key
		}
	}

	t, err := st.CreateTask(ctx, n)
	if errors.Is(err, store.ErrLinked) {
		return a.existingItem(t, issue.Key, nil), nil
	}
	if err != nil {
		return intakeItem{}, err
	}
	item.Task = t
	return item, nil
}

// existingItem is the result for an issue that is already linked. It says so
// on stderr, once.
func (a *app) existingItem(t store.Task, key string, tasks []store.Task) intakeItem {
	fmt.Fprintf(a.stderr, "rig: %s is already task %s\n", key, t.Short)
	item := intakeItem{Task: t, Key: key, Existing: true}
	for _, p := range tasks {
		if t.Parent != "" && p.ID == t.Parent {
			item.Parent = p.Short
		}
	}
	return item
}

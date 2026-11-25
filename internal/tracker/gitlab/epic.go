package gitlab

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/AindriuB/rig/internal/tracker"
)

// A group epic is keyed "epic-<iid>" and is reached through GraphQL only; no
// epic key or iid is ever put into a REST path. Constant documents; every
// untrusted value travels as a variable.
const (
	groupQuery        = `query RigGroup($path: ID!) { project(fullPath: $path) { group { fullPath } } }`
	epicReadQuery     = `query RigEpicRead($path: ID!, $iid: String!) { group(fullPath: $path) { workItems(iid: $iid, types: [EPIC], first: 1) { nodes { id iid webUrl title state updatedAt widgets { type ... on WorkItemWidgetDescription { description } ... on WorkItemWidgetLabels { labels { nodes { title } } } ... on WorkItemWidgetHierarchy { parent { iid webUrl title workItemType { name } } children(first: 100) { nodes { iid webUrl title workItemType { name } } } } } } } } }`
	epicSearchQuery   = `query RigEpicSearch($path: ID!, $labels: [String!], $search: String, $first: Int) { group(fullPath: $path) { workItems(types: [EPIC], labelName: $labels, search: $search, first: $first) { nodes { iid webUrl title workItemType { name } } } } }`
	groupLabelsQuery  = `query RigGroupLabels($path: ID!, $term: String!) { group(fullPath: $path) { labels(searchTerm: $term, includeAncestorGroups: true, first: 100) { nodes { id title } } } }`
	noteCreateMut     = `mutation RigNote($input: CreateNoteInput!) { createNote(input: $input) { note { id } errors } }`
	workItemUpdateMut = `mutation RigState($input: WorkItemUpdateInput!) { workItemUpdate(input: $input) { workItem { id } errors } }`
)

// groupPath returns the project's direct group path, or "" when it has none.
func (t *Tracker) groupPath(ctx context.Context) (string, error) {
	t.gmu.Lock()
	defer t.gmu.Unlock()
	if t.groupLoaded {
		return t.group, nil
	}
	var data struct {
		Project *struct {
			Group *struct {
				FullPath string `json:"fullPath"`
			} `json:"group"`
		}
	}
	err := t.graphql(ctx, groupQuery, map[string]any{"path": t.project}, &data)
	switch {
	case errors.Is(err, errGraphQL):
	case err != nil:
		return "", err
	case data.Project != nil && data.Project.Group != nil:
		t.group = data.Project.Group.FullPath
	}
	t.groupLoaded = true
	return t.group, nil
}

type epicData struct {
	ID, IID, WebURL, Title, State, UpdatedAt, Description string
	Labels                                                []string
	Parent                                                *workItemRef
	Kids                                                  []workItemRef
}

// epicRead reads the group epic with this iid. An epic that is not in the
// project's direct group is not found.
func (t *Tracker) epicRead(ctx context.Context, iid string) (epicData, error) {
	notFound := fmt.Errorf("gitlab tracker: epic %s: %w", iid, tracker.ErrNotFound)
	if err := t.projectBase(ctx); err != nil {
		return epicData{}, err
	}
	group, err := t.groupPath(ctx)
	if err != nil {
		return epicData{}, err
	}
	if group == "" {
		return epicData{}, notFound
	}
	var data struct {
		Group *struct {
			WorkItems struct {
				Nodes []struct {
					ID, IID, WebURL, Title, State, UpdatedAt string
					Widgets                                  []struct {
						Type        string
						Description string
						Labels      *struct{ Nodes []struct{ Title string } }
						Parent      *workItemRef                   `json:"parent"`
						Kids        *struct{ Nodes []workItemRef } `json:"children"`
					}
				}
			} `json:"workItems"`
		}
	}
	if err := t.graphql(ctx, epicReadQuery, map[string]any{"path": group, "iid": iid}, &data); err != nil {
		return epicData{}, err
	}
	if data.Group == nil || len(data.Group.WorkItems.Nodes) == 0 {
		return epicData{}, notFound
	}
	n := data.Group.WorkItems.Nodes[0]
	if n.IID != iid || n.ID == "" {
		return epicData{}, notFound
	}
	if r, ok := t.workItem(workItemRef{IID: n.IID, WebURL: n.WebURL}); !ok || r.Key != epicPrefix+iid {
		return epicData{}, notFound
	}
	e := epicData{ID: n.ID, IID: n.IID, WebURL: n.WebURL, Title: n.Title, UpdatedAt: n.UpdatedAt}
	switch strings.ToUpper(n.State) {
	case "CLOSED":
		e.State = "closed"
	default:
		e.State = "opened"
	}
	for _, w := range n.Widgets {
		e.Description += w.Description
		if w.Labels != nil {
			for _, l := range w.Labels.Nodes {
				e.Labels = append(e.Labels, l.Title)
			}
		}
		if w.Parent != nil {
			e.Parent = w.Parent
		}
		if w.Kids != nil {
			e.Kids = append(e.Kids, w.Kids.Nodes...)
		}
	}
	return e, nil
}

func (t *Tracker) epicIssue(ctx context.Context, key, iid string) (tracker.Issue, error) {
	e, err := t.epicRead(ctx, iid)
	if err != nil {
		return tracker.Issue{}, err
	}
	out := tracker.Issue{Key: key, URL: e.WebURL, Title: e.Title, Description: e.Description,
		Labels: e.Labels, Type: "epic", Status: e.State, Rev: e.UpdatedAt}
	if e.Parent != nil {
		if r, ok := t.workItem(*e.Parent); ok {
			out.Parent = &r
		}
	}
	return out, nil
}

func (t *Tracker) epicChildren(ctx context.Context, iid string) ([]tracker.IssueRef, bool, error) {
	e, err := t.epicRead(ctx, iid)
	if errors.Is(err, tracker.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var kids []tracker.IssueRef
	for _, k := range e.Kids {
		if r, ok := t.workItem(k); ok {
			kids = append(kids, r)
		}
	}
	return kids, true, nil
}

// searchEpics lists epics of the project's group that carry every label.
func (t *Tracker) searchEpics(ctx context.Context, q tracker.Query, limit int) ([]tracker.IssueRef, error) {
	if err := t.projectBase(ctx); err != nil {
		return nil, err
	}
	group, err := t.groupPath(ctx)
	if err != nil || group == "" {
		return nil, err
	}
	vars := map[string]any{"path": group, "labels": q.Labels, "first": limit}
	if q.Text != "" {
		vars["search"] = q.Text
	}
	var data struct {
		Group *struct {
			WorkItems struct{ Nodes []workItemRef } `json:"workItems"`
		}
	}
	if err := t.graphql(ctx, epicSearchQuery, vars, &data); err != nil {
		if errors.Is(err, errGraphQL) {
			return nil, nil // no group epics on this instance
		}
		return nil, err
	}
	var out []tracker.IssueRef
	if data.Group != nil {
		for _, n := range data.Group.WorkItems.Nodes {
			if r, ok := t.workItem(n); ok && strings.HasPrefix(r.Key, epicPrefix) {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

func (t *Tracker) epicComment(ctx context.Context, iid string, c tracker.Comment) (tracker.CommentResult, error) {
	e, err := t.epicRead(ctx, iid)
	if err != nil {
		return tracker.CommentResult{}, err
	}
	var data struct {
		CreateNote struct {
			Note *struct {
				ID string `json:"id"`
			} `json:"note"`
			Errors []string `json:"errors"`
		} `json:"createNote"`
	}
	in := map[string]any{"noteableId": e.ID, "body": c.Body}
	if err := t.graphql(ctx, noteCreateMut, map[string]any{"input": in}, &data); err != nil {
		return tracker.CommentResult{}, err
	}
	r := data.CreateNote
	if len(r.Errors) > 0 {
		return tracker.CommentResult{}, fmt.Errorf("gitlab tracker: comment refused: %s", strings.Join(r.Errors, "; "))
	}
	if r.Note == nil || r.Note.ID == "" {
		return tracker.CommentResult{}, errors.New("gitlab tracker: the note reply had no id")
	}
	id := r.Note.ID[strings.LastIndex(r.Note.ID, "/")+1:]
	return tracker.CommentResult{ID: id, URL: e.WebURL + "#note_" + id}, nil
}

func (t *Tracker) epicSetStatus(ctx context.Context, iid, want, event string) (tracker.StatusResult, error) {
	e, err := t.epicRead(ctx, iid)
	if err != nil {
		return tracker.StatusResult{}, err
	}
	if e.State == want {
		return tracker.StatusResult{From: e.State, To: want}, nil
	}
	var data struct {
		WorkItemUpdate struct {
			Errors []string `json:"errors"`
		} `json:"workItemUpdate"`
	}
	in := map[string]any{"id": e.ID, "stateEvent": strings.ToUpper(event)}
	if err := t.graphql(ctx, workItemUpdateMut, map[string]any{"input": in}, &data); err != nil {
		return tracker.StatusResult{}, err
	}
	if errs := data.WorkItemUpdate.Errors; len(errs) > 0 {
		return tracker.StatusResult{}, fmt.Errorf("gitlab tracker: status change refused: %s", strings.Join(errs, "; "))
	}
	return tracker.StatusResult{From: e.State, To: want, Changed: true}, nil
}

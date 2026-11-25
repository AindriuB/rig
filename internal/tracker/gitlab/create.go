package gitlab

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/AindriuB/rig/internal/tracker"
)

// Constant documents; every untrusted value travels as a variable.
const (
	typesQuery  = `query RigTypes($path: ID!) { project(fullPath: $path) { workItemTypes { nodes { id name } } group { fullPath workItemTypes { nodes { id name } } } } }`
	parentQuery = `query RigParent($path: ID!, $iid: String!) { project(fullPath: $path) { workItems(iid: $iid, first: 1) { nodes { id } } } }`
	labelsQuery = `query RigLabels($path: ID!, $term: String!) { project(fullPath: $path) { labels(searchTerm: $term, includeAncestorGroups: true, first: 100) { nodes { id title } } } }`
	createMut   = `mutation RigCreate($input: WorkItemCreateInput!) { workItemCreate(input: $input) { workItem { iid webUrl updatedAt } errors } }`
)

type typeInfo struct {
	ID   string
	Name string
	// Namespace is the full path the type creates into: the project, or the
	// group for an epic.
	Namespace string
}

// workItemTypes lists the types this project can create. Epic is offered only
// from the project's group, and only when the group reports it.
func (t *Tracker) workItemTypes(ctx context.Context) ([]typeInfo, error) {
	var data struct {
		Project *struct {
			WorkItemTypes struct{ Nodes []struct{ ID, Name string } } `json:"workItemTypes"`
			Group         *struct {
				FullPath      string                                      `json:"fullPath"`
				WorkItemTypes struct{ Nodes []struct{ ID, Name string } } `json:"workItemTypes"`
			} `json:"group"`
		}
	}
	if err := t.graphql(ctx, typesQuery, map[string]any{"path": t.project}, &data); err != nil {
		if errors.Is(err, errGraphQL) {
			return nil, fmt.Errorf("gitlab tracker: work item types are not available here: %w", tracker.ErrUnsupported)
		}
		return nil, err
	}
	if data.Project == nil {
		return nil, fmt.Errorf("gitlab tracker: project %s: %w", t.project, tracker.ErrNotFound)
	}
	var out []typeInfo
	for _, n := range data.Project.WorkItemTypes.Nodes {
		if !strings.EqualFold(n.Name, "Epic") {
			out = append(out, typeInfo{ID: n.ID, Name: n.Name, Namespace: t.project})
		}
	}
	if g := data.Project.Group; g != nil {
		for _, n := range g.WorkItemTypes.Nodes {
			if strings.EqualFold(n.Name, "Epic") {
				out = append(out, typeInfo{ID: n.ID, Name: n.Name, Namespace: g.FullPath})
			}
		}
	}
	return out, nil
}

// IssueTypes lists the project's work item types with their levels.
func (t *Tracker) IssueTypes(ctx context.Context) ([]tracker.IssueType, error) {
	types, err := t.workItemTypes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]tracker.IssueType, 0, len(types))
	for _, ti := range types {
		it := tracker.IssueType{Name: ti.Name}
		if l, ok := map[string]int{"epic": 2, "issue": 1, "task": 0}[strings.ToLower(ti.Name)]; ok {
			it.Level = &l
		}
		out = append(out, it)
	}
	return out, nil
}

// Create makes one work item with one write. The type, parent and label IDs
// are read first; a missing one stops the call before any write.
func (t *Tracker) Create(ctx context.Context, ni tracker.NewIssue) (tracker.Issue, error) {
	if ni.ParentKey != "" {
		if err := validateKey(ni.ParentKey); err != nil {
			return tracker.Issue{}, err
		}
	}
	types, err := t.workItemTypes(ctx)
	if err != nil {
		return tracker.Issue{}, err
	}
	var ti *typeInfo
	for i := range types {
		if strings.EqualFold(types[i].Name, ni.Type) {
			ti = &types[i]
		}
	}
	if ti == nil {
		if strings.EqualFold(ni.Type, "Epic") {
			return tracker.Issue{}, fmt.Errorf("gitlab tracker: work item type %q is not available (no group or tier without epics): %w", ni.Type, tracker.ErrUnsupported)
		}
		return tracker.Issue{}, fmt.Errorf("gitlab tracker: unknown work item type %q", ni.Type)
	}
	input := map[string]any{"namespacePath": ti.Namespace, "workItemTypeId": ti.ID, "title": ni.Title}
	if desc := description(ni); desc != "" {
		input["descriptionWidget"] = map[string]any{"description": desc}
	}
	if len(ni.Labels) > 0 {
		ids := make([]string, 0, len(ni.Labels))
		for _, l := range ni.Labels {
			id, err := t.labelID(ctx, l)
			if err != nil {
				return tracker.Issue{}, err
			}
			ids = append(ids, id)
		}
		input["labelsWidget"] = map[string]any{"labelIds": ids}
	}
	if ni.ParentKey != "" {
		if strings.EqualFold(ti.Name, "Epic") {
			return tracker.Issue{}, fmt.Errorf("gitlab tracker: an epic cannot take a project issue as its parent: %w", tracker.ErrUnsupported)
		}
		h, err := t.hasHierarchy(ctx)
		if err != nil {
			return tracker.Issue{}, err
		}
		if !h {
			return tracker.Issue{}, fmt.Errorf("gitlab tracker: work-item hierarchy is not available here: %w", tracker.ErrUnsupported)
		}
		id, err := t.parentID(ctx, ni.ParentKey)
		if err != nil {
			return tracker.Issue{}, err
		}
		input["hierarchyWidget"] = map[string]any{"parentId": id}
	}
	var data struct {
		WorkItemCreate struct {
			WorkItem *struct {
				IID       string `json:"iid"`
				WebURL    string `json:"webUrl"`
				UpdatedAt string `json:"updatedAt"`
			} `json:"workItem"`
			Errors []string `json:"errors"`
		} `json:"workItemCreate"`
	}
	if err := t.graphql(ctx, createMut, map[string]any{"input": input}, &data); err != nil {
		return tracker.Issue{}, err
	}
	r := data.WorkItemCreate
	if len(r.Errors) > 0 {
		return tracker.Issue{}, fmt.Errorf("gitlab tracker: create refused: %s", strings.Join(r.Errors, "; "))
	}
	if r.WorkItem == nil || validateKey(r.WorkItem.IID) != nil || r.WorkItem.WebURL == "" {
		return tracker.Issue{}, errors.New("gitlab tracker: the create reply had no work item")
	}
	return tracker.Issue{
		Key: r.WorkItem.IID, URL: r.WorkItem.WebURL, Title: ni.Title, Description: description(ni),
		Labels: ni.Labels, Type: strings.ToLower(ti.Name), Status: "opened", Rev: r.WorkItem.UpdatedAt,
	}, nil
}

func description(ni tracker.NewIssue) string {
	d := ni.Body
	if len(ni.Acceptance) > 0 {
		if d != "" {
			d += "\n\n"
		}
		d += "## Acceptance criteria\n"
		for _, a := range ni.Acceptance {
			d += "- [ ] " + a + "\n"
		}
		d = strings.TrimRight(d, "\n")
	}
	return d
}

func (t *Tracker) parentID(ctx context.Context, key string) (string, error) {
	var data struct {
		Project *struct {
			WorkItems struct{ Nodes []struct{ ID string } } `json:"workItems"`
		}
	}
	if err := t.graphql(ctx, parentQuery, map[string]any{"path": t.project, "iid": key}, &data); err != nil {
		return "", err
	}
	if data.Project == nil || len(data.Project.WorkItems.Nodes) == 0 || data.Project.WorkItems.Nodes[0].ID == "" {
		return "", fmt.Errorf("gitlab tracker: parent %s: %w", key, tracker.ErrNotFound)
	}
	return data.Project.WorkItems.Nodes[0].ID, nil
}

func (t *Tracker) labelID(ctx context.Context, name string) (string, error) {
	var data struct {
		Project *struct {
			Labels struct{ Nodes []struct{ ID, Title string } } `json:"labels"`
		}
	}
	if err := t.graphql(ctx, labelsQuery, map[string]any{"path": t.project, "term": name}, &data); err != nil {
		return "", err
	}
	if data.Project != nil {
		for _, n := range data.Project.Labels.Nodes {
			if n.Title == name {
				return n.ID, nil
			}
		}
	}
	return "", fmt.Errorf("gitlab tracker: label %s does not exist in %s: %w", strconv.Quote(name), t.project, tracker.ErrNotFound)
}

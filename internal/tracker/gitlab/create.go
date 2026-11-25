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
	typesQuery  = `query RigTypes($path: ID!) { project(fullPath: $path) { workItemTypes { nodes { id name } } } }`
	parentQuery = `query RigParent($path: ID!, $iid: String!) { project(fullPath: $path) { workItems(iid: $iid, first: 1) { nodes { id } } } }`
	labelsQuery = `query RigLabels($path: ID!, $term: String!) { project(fullPath: $path) { labels(searchTerm: $term, includeAncestorGroups: true, first: 100) { nodes { id title } } } }`
	createMut   = `mutation RigCreate($input: WorkItemCreateInput!) { workItemCreate(input: $input) { workItem { iid webUrl updatedAt } errors } }`
)

// labelColor is the one fixed colour of labels this adapter creates.
const labelColor = "#6699cc"

type typeInfo struct {
	ID   string
	Name string
}

// workItemTypes lists the types this project can create. Group-level types
// (Epic) are never listed: an epic's key is group-scoped and does not
// round-trip through this adapter (task 28).
func (t *Tracker) workItemTypes(ctx context.Context) ([]typeInfo, error) {
	var data struct {
		Project *struct {
			WorkItemTypes struct{ Nodes []struct{ ID, Name string } } `json:"workItemTypes"`
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
			out = append(out, typeInfo{ID: n.ID, Name: n.Name})
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
		if l, ok := map[string]int{"issue": 1, "task": 0}[strings.ToLower(ti.Name)]; ok {
			it.Level = &l
		}
		out = append(out, it)
	}
	return out, nil
}

// Create makes one work item with one workItemCreate write. The type and
// parent are read first, then each missing label is created; a failure before
// the work item write leaves no work item. When the item is written but its
// revision cannot be read, the created Key and URL come back with the error.
func (t *Tracker) Create(ctx context.Context, ni tracker.NewIssue) (tracker.Issue, error) {
	if strings.EqualFold(ni.Type, "Epic") {
		return tracker.Issue{}, fmt.Errorf("gitlab tracker: work item type %q is not supported here: %w", ni.Type, tracker.ErrUnsupported)
	}
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
		return tracker.Issue{}, fmt.Errorf("gitlab tracker: unknown work item type %q", ni.Type)
	}
	input := map[string]any{"namespacePath": t.project, "workItemTypeId": ti.ID, "title": ni.Title}
	if desc := description(ni); desc != "" {
		input["descriptionWidget"] = map[string]any{"description": desc}
	}
	if ni.ParentKey != "" {
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
	if len(ni.Labels) > 0 {
		ids := make([]string, 0, len(ni.Labels))
		for _, l := range ni.Labels {
			id, err := t.ensureLabel(ctx, l)
			if err != nil {
				return tracker.Issue{}, err
			}
			ids = append(ids, id)
		}
		input["labelsWidget"] = map[string]any{"labelIds": ids}
	}
	var data struct {
		WorkItemCreate struct {
			WorkItem *struct {
				IID    string `json:"iid"`
				WebURL string `json:"webUrl"`
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
	made := tracker.Issue{
		Key: r.WorkItem.IID, URL: r.WorkItem.WebURL, Title: ni.Title, Description: description(ni),
		Labels: ni.Labels, Type: strings.ToLower(ti.Name), Status: "opened",
	}
	// Rev comes from the REST read, the same source as Issue(), so the two agree.
	is, err := t.fetch(ctx, made.Key)
	if err != nil {
		return made, fmt.Errorf("gitlab tracker: work item %s was created but its revision could not be read: %w", made.Key, err)
	}
	made.Rev = is.Updated
	return made, nil
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

// labelID finds a label by exact name in the project or its ancestor groups.
func (t *Tracker) labelID(ctx context.Context, name string) (string, bool, error) {
	var data struct {
		Project *struct {
			Labels struct{ Nodes []struct{ ID, Title string } } `json:"labels"`
		}
	}
	if err := t.graphql(ctx, labelsQuery, map[string]any{"path": t.project, "term": name}, &data); err != nil {
		return "", false, err
	}
	if data.Project != nil {
		for _, n := range data.Project.Labels.Nodes {
			if n.Title == name && n.ID != "" {
				return n.ID, true, nil
			}
		}
	}
	return "", false, nil
}

// ensureLabel returns the label's id, creating the label first when it is
// missing. A create that answers "already exists" counts as present.
func (t *Tracker) ensureLabel(ctx context.Context, name string) (string, error) {
	if id, ok, err := t.labelID(ctx, name); err != nil || ok {
		return id, err
	}
	_, err := t.run(ctx, nil, "-X", "POST", "projects/"+t.path+"/labels", "-f", "name="+name, "-f", "color="+labelColor)
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "already exists") {
		return "", fmt.Errorf("gitlab tracker: create label %s: %w", strconv.Quote(name), err)
	}
	id, ok, err := t.labelID(ctx, name)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("gitlab tracker: label %s was created but not found in %s: %w", strconv.Quote(name), t.project, tracker.ErrNotFound)
	}
	return id, nil
}

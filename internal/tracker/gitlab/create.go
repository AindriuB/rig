package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/AindriuB/rig/internal/tracker"
)

// Constant documents; every untrusted value travels as a variable.
const (
	typesQuery  = `query RigTypes($path: ID!) { project(fullPath: $path) { workItemTypes { nodes { id name } } group { workItemTypes { nodes { id name } } } } }`
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

// workItemTypes lists the types this project can create. Epic is listed only
// when the project's direct group offers it; it is created in that group and
// keyed "epic-<iid>".
func (t *Tracker) workItemTypes(ctx context.Context) ([]typeInfo, error) {
	var data struct {
		Project *struct {
			WorkItemTypes struct{ Nodes []struct{ ID, Name string } } `json:"workItemTypes"`
			Group         *struct {
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
			out = append(out, typeInfo{ID: n.ID, Name: n.Name})
		}
	}
	if g := data.Project.Group; g != nil {
		for _, n := range g.WorkItemTypes.Nodes {
			if strings.EqualFold(n.Name, "Epic") {
				out = append(out, typeInfo{ID: n.ID, Name: n.Name})
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

// Create makes one work item with one workItemCreate write. The type and
// parent are read first, then each missing label is created; a failure before
// the work item write leaves no work item. When the item is written but its
// revision cannot be read, the created Key and URL come back with the error.
func (t *Tracker) Create(ctx context.Context, ni tracker.NewIssue) (tracker.Issue, error) {
	isEpic := strings.EqualFold(ni.Type, "Epic")
	if ni.ParentKey != "" {
		if err := validateKey(ni.ParentKey); err != nil {
			return tracker.Issue{}, err
		}
		if isEpic {
			return tracker.Issue{}, fmt.Errorf("gitlab tracker: an epic with a parent is not supported here: %w", tracker.ErrUnsupported)
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
	if ti == nil && isEpic {
		return tracker.Issue{}, fmt.Errorf("gitlab tracker: work item type %q is not supported here: %w", ni.Type, tracker.ErrUnsupported)
	}
	if ti == nil {
		return tracker.Issue{}, fmt.Errorf("gitlab tracker: unknown work item type %q", ni.Type)
	}
	namespace := t.project
	if isEpic {
		if namespace, err = t.groupPath(ctx); err != nil {
			return tracker.Issue{}, err
		}
		if namespace == "" {
			return tracker.Issue{}, fmt.Errorf("gitlab tracker: work item type %q is not supported here: %w", ni.Type, tracker.ErrUnsupported)
		}
	}
	input := map[string]any{"namespacePath": namespace, "workItemTypeId": ti.ID, "title": ni.Title}
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
			id, err := t.ensureLabel(ctx, l, isEpic)
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
	key := r.WorkItem.IID
	if isEpic {
		key = epicPrefix + key
	}
	made := tracker.Issue{
		Key: key, URL: r.WorkItem.WebURL, Title: ni.Title, Description: description(ni),
		Labels: ni.Labels, Type: strings.ToLower(ti.Name), Status: "opened",
	}
	// Rev comes from the same read as Issue(), so the two agree.
	if isEpic {
		e, err := t.epicRead(ctx, r.WorkItem.IID)
		if err != nil {
			return made, fmt.Errorf("gitlab tracker: epic %s was created but its revision could not be read: %w", made.Key, err)
		}
		made.Rev = e.UpdatedAt
		return made, nil
	}
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
	iid, epic, err := parseKey(key)
	if err != nil {
		return "", err
	}
	if epic {
		e, err := t.epicRead(ctx, iid)
		return e.ID, err
	}
	var data struct {
		Project *struct {
			WorkItems struct{ Nodes []struct{ ID string } } `json:"workItems"`
		}
	}
	if err := t.graphql(ctx, parentQuery, map[string]any{"path": t.project, "iid": iid}, &data); err != nil {
		return "", err
	}
	if data.Project == nil || len(data.Project.WorkItems.Nodes) == 0 || data.Project.WorkItems.Nodes[0].ID == "" {
		return "", fmt.Errorf("gitlab tracker: parent %s: %w", key, tracker.ErrNotFound)
	}
	return data.Project.WorkItems.Nodes[0].ID, nil
}

// labelID finds a label by exact name in the project or its ancestor groups.
func (t *Tracker) labelID(ctx context.Context, name string, inGroup bool) (string, bool, error) {
	type labelSet struct {
		Labels struct{ Nodes []struct{ ID, Title string } } `json:"labels"`
	}
	var data struct {
		Project *labelSet
		Group   *labelSet
	}
	query, path := labelsQuery, t.project
	if inGroup {
		query = groupLabelsQuery
		path, _ = t.groupPath(ctx)
	}
	if err := t.graphql(ctx, query, map[string]any{"path": path, "term": name}, &data); err != nil {
		return "", false, err
	}
	set := data.Project
	if inGroup {
		set = data.Group
	}
	if set != nil {
		for _, n := range set.Labels.Nodes {
			if n.Title == name && n.ID != "" {
				return n.ID, true, nil
			}
		}
	}
	return "", false, nil
}

// ensureLabel returns the label's id, creating the label first when it is
// missing. A create that answers "already exists" counts as present.
func (t *Tracker) ensureLabel(ctx context.Context, name string, inGroup bool) (string, error) {
	if id, ok, err := t.labelID(ctx, name, inGroup); err != nil || ok {
		return id, err
	}
	endpoint := "projects/" + t.path + "/labels"
	if inGroup {
		g, _ := t.groupPath(ctx)
		endpoint = "groups/" + url.PathEscape(g) + "/labels"
	}
	_, err := t.run(ctx, nil, "-X", "POST", endpoint, "-f", "name="+name, "-f", "color="+labelColor)
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "already exists") {
		return "", fmt.Errorf("gitlab tracker: create label %s: %w", strconv.Quote(name), err)
	}
	id, ok, err := t.labelID(ctx, name, inGroup)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("gitlab tracker: label %s was created but not found in %s: %w", strconv.Quote(name), t.project, tracker.ErrNotFound)
	}
	return id, nil
}

package jira

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/AindriuB/rig/internal/tracker"
)

// epicLevel is the hierarchy level of an epic.
const epicLevel = 1

type typeInfo struct {
	name    string
	level   int
	subtask bool
}

// issueTypes reads the project's issue types from createmeta. Cloud gives each
// a hierarchyLevel; Server/DC gives none, so a sub-task type is -1, the Epic
// type 1 and any other 0.
func (a *Adapter) issueTypes(ctx context.Context, d dialect) ([]typeInfo, error) {
	if a.cfg.Project == "" {
		return nil, errors.New("jira: no project is configured; set tracker.jira.project")
	}
	var resp struct {
		IssueTypes []jiraType `json:"issueTypes"`
		Values     []jiraType `json:"values"`
	}
	path := d.path() + "/issue/createmeta/" + url.PathEscape(a.cfg.Project) + "/issuetypes"
	if err := a.do(ctx, http.MethodGet, path, nil, nil, &resp); err != nil {
		return nil, err
	}
	list := resp.IssueTypes
	if len(list) == 0 {
		list = resp.Values
	}
	out := make([]typeInfo, 0, len(list))
	for _, t := range list {
		ti := typeInfo{name: t.Name, subtask: t.Subtask}
		switch {
		case t.HierarchyLevel != nil:
			ti.level = *t.HierarchyLevel
		case t.Subtask:
			ti.level = -1
		case strings.EqualFold(t.Name, "Epic"):
			ti.level = epicLevel
		}
		out = append(out, ti)
	}
	return out, nil
}

type jiraType struct {
	Name           string `json:"name"`
	Subtask        bool   `json:"subtask"`
	HierarchyLevel *int   `json:"hierarchyLevel"`
}

// IssueTypes lists the project's issue types with their hierarchy levels.
func (a *Adapter) IssueTypes(ctx context.Context) ([]tracker.IssueType, error) {
	d, err := a.resolve(ctx)
	if err != nil {
		return nil, err
	}
	types, err := a.issueTypes(ctx, d)
	if err != nil {
		return nil, err
	}
	if len(types) == 0 {
		return nil, fmt.Errorf("%w: project %s has no issue types", tracker.ErrUnsupported, a.cfg.Project)
	}
	out := make([]tracker.IssueType, 0, len(types))
	for _, t := range types {
		lv := t.level
		out = append(out, tracker.IssueType{Name: t.name, Level: &lv})
	}
	return out, nil
}

// Create makes an issue. Nothing is written until the parent key, the issue
// type and (on Server/DC) the Epic Link field have been checked.
func (a *Adapter) Create(ctx context.Context, ni tracker.NewIssue) (tracker.Issue, error) {
	if ni.ParentKey != "" {
		if err := checkKey(ni.ParentKey); err != nil {
			return tracker.Issue{}, err
		}
	}
	if strings.TrimSpace(ni.Title) == "" {
		return tracker.Issue{}, errors.New("jira: an issue needs a title")
	}
	if strings.TrimSpace(ni.Type) == "" {
		return tracker.Issue{}, errors.New("jira: an issue needs a type")
	}
	d, err := a.resolve(ctx)
	if err != nil {
		return tracker.Issue{}, err
	}
	types, err := a.issueTypes(ctx, d)
	if err != nil {
		return tracker.Issue{}, err
	}
	var typ *typeInfo
	for i := range types {
		if types[i].name == ni.Type {
			typ = &types[i]
		}
	}
	if typ == nil {
		return tracker.Issue{}, fmt.Errorf("jira: project %s has no issue type %q", a.cfg.Project, ni.Type)
	}
	fields := map[string]any{
		"project":   map[string]string{"key": a.cfg.Project},
		"summary":   ni.Title,
		"issuetype": map[string]string{"name": ni.Type},
	}
	if strings.TrimSpace(ni.Body) != "" {
		if d == v3 {
			fields["description"] = markdownToADF(ni.Body)
		} else {
			fields["description"] = markdownToWiki(ni.Body)
		}
	}
	if len(ni.Labels) > 0 {
		fields["labels"] = ni.Labels
	}
	if id := a.cfg.AcceptanceField; id != "" && len(ni.Acceptance) > 0 {
		var md strings.Builder
		for _, item := range ni.Acceptance {
			md.WriteString("- " + strings.Join(strings.Fields(item), " ") + "\n")
		}
		if d == v3 {
			fields[id] = markdownToADF(md.String())
		} else {
			fields[id] = markdownToWiki(md.String())
		}
	}
	if ni.ParentKey != "" {
		if err := a.setParent(ctx, d, fields, *typ, types, ni.ParentKey); err != nil {
			return tracker.Issue{}, err
		}
	}
	var resp struct {
		Key string `json:"key"`
	}
	if err := a.do(ctx, http.MethodPost, d.path()+"/issue", nil, map[string]any{"fields": fields}, &resp); err != nil {
		return tracker.Issue{}, err
	}
	if err := checkKey(resp.Key); err != nil {
		return tracker.Issue{}, fmt.Errorf("jira: create returned no usable key: %w", err)
	}
	is, err := a.Issue(ctx, resp.Key)
	if err != nil {
		return tracker.Issue{Key: resp.Key, URL: a.browse(resp.Key)}, fmt.Errorf("jira: created %s but could not read it back: %w", resp.Key, err)
	}
	return is, nil
}

// setParent adds the parent to fields the way the deployment expects.
func (a *Adapter) setParent(ctx context.Context, d dialect, fields map[string]any, typ typeInfo, types []typeInfo, parent string) error {
	if d == v3 || typ.subtask {
		fields["parent"] = map[string]string{"key": parent}
		return nil
	}
	// Server/DC: a child of an epic is linked by Epic Link.
	var r issueResp
	if err := a.do(ctx, http.MethodGet, d.path()+"/issue/"+url.PathEscape(parent), url.Values{"fields": {"issuetype"}}, nil, &r); err != nil {
		return err
	}
	var pt nameObj
	if err := r.field("issuetype", &pt); err != nil {
		return err
	}
	level := 0
	for _, t := range types {
		if t.name == pt.Name {
			level = t.level
		}
	}
	if level != epicLevel {
		fields["parent"] = map[string]string{"key": parent}
		return nil
	}
	id, err := a.epicFieldID(ctx)
	if err != nil {
		return err
	}
	if id == "" {
		return errors.New("jira: creating a child of an epic on Server/DC needs the Epic Link field id, and none was found by name; set tracker.jira.epicField")
	}
	fields[id] = parent
	return nil
}

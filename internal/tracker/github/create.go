package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/AindriuB/rig/internal/tracker"
)

// The gh calls below are ASSUMED, like the ones in the package doc, and are
// tested only against the fake gh:
//
//   - REST `GET orgs/OWNER/issue-types` answers an array of {id,name}; it is
//     404 for a user-owned repository.
//   - REST `POST repos/O/R/issues` takes a JSON body of title, body, labels
//     and type (an issue type name) and answers number, id, html_url,
//     updated_at.
//   - REST `POST repos/O/R/issues/P/sub_issues` takes {"sub_issue_id": id},
//     where id is the child's database id (not its number).

// IssueTypes lists the owner's issue types. Level is nil: GitHub puts no
// hierarchy level on a type. A user-owned repository, or an organisation
// with no types, is ErrUnsupported.
func (t *Tracker) IssueTypes(ctx context.Context) ([]tracker.IssueType, error) {
	res, err := t.api(ctx, nil, "orgs/"+t.owner+"/issue-types")
	if err != nil {
		if errors.Is(err, tracker.ErrNotFound) {
			return nil, fmt.Errorf("issue types: %w: %s has no issue types", tracker.ErrUnsupported, t.owner)
		}
		return nil, err
	}
	var raw []nameJSON
	if err := json.Unmarshal(res.Stdout, &raw); err != nil {
		return nil, fmt.Errorf("gh api orgs/%s/issue-types: unreadable JSON: %w", t.owner, err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("issue types: %w: %s has no issue types", tracker.ErrUnsupported, t.owner)
	}
	out := make([]tracker.IssueType, 0, len(raw))
	for _, r := range raw {
		out = append(out, tracker.IssueType{Name: r.Name})
	}
	return out, nil
}

// Create makes an issue, then attaches it to ni.ParentKey as a sub-issue. A
// type name is checked against the owner's types before anything is written;
// an owner with no types gets the issue without one. If the attach fails the
// error names the created issue.
func (t *Tracker) Create(ctx context.Context, ni tracker.NewIssue) (tracker.Issue, error) {
	var parent int64
	if ni.ParentKey != "" {
		p, err := checkKey(ni.ParentKey)
		if err != nil {
			return tracker.Issue{}, err
		}
		parent = p
	}
	typeName := ""
	if ni.Type != "" {
		types, err := t.IssueTypes(ctx)
		switch {
		case errors.Is(err, tracker.ErrUnsupported):
			// No types here: send the issue without one.
		case err != nil:
			return tracker.Issue{}, err
		default:
			for _, it := range types {
				if strings.EqualFold(it.Name, ni.Type) {
					typeName = it.Name
					break
				}
			}
			if typeName == "" {
				return tracker.Issue{}, fmt.Errorf("issue type %q is not one of %s's types", ni.Type, t.owner)
			}
		}
	}
	if parent != 0 {
		// Refuse a missing parent before writing, so no orphan is left.
		if _, err := t.graphql(ctx, `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){issue(number:$number){number}}}`, parent); err != nil {
			return tracker.Issue{}, fmt.Errorf("parent %s: %w", ni.ParentKey, err)
		}
	}
	body := ni.Body
	if len(ni.Acceptance) > 0 {
		var b strings.Builder
		b.WriteString(strings.TrimRight(body, "\n"))
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("## Acceptance\n")
		for _, a := range ni.Acceptance {
			b.WriteString("- [ ] " + strings.Join(strings.Fields(a), " ") + "\n")
		}
		body = b.String()
	}
	req := map[string]any{"title": ni.Title, "body": body}
	if len(ni.Labels) > 0 {
		req["labels"] = ni.Labels
	}
	if typeName != "" {
		req["type"] = typeName
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return tracker.Issue{}, err
	}
	res, err := t.api(ctx, payload, "repos/"+t.owner+"/"+t.name+"/issues", "--method", "POST", "--input", "-")
	if err != nil {
		return tracker.Issue{}, err
	}
	var out struct {
		Number    int64       `json:"number"`
		ID        json.Number `json:"id"`
		HTMLURL   string      `json:"html_url"`
		UpdatedAt string      `json:"updated_at"`
	}
	if err := json.Unmarshal(res.Stdout, &out); err != nil || out.Number < 1 {
		return tracker.Issue{}, fmt.Errorf("gh api create issue: unreadable answer (the issue may have been created)")
	}
	key := strconv.FormatInt(out.Number, 10)
	rev := out.UpdatedAt
	var parentRef *tracker.IssueRef
	if parent != 0 {
		sub, err := json.Marshal(map[string]json.Number{"sub_issue_id": out.ID})
		if err != nil {
			return tracker.Issue{}, err
		}
		if _, err := t.api(ctx, sub, "repos/"+t.owner+"/"+t.name+"/issues/"+strconv.FormatInt(parent, 10)+"/sub_issues",
			"--method", "POST", "--input", "-"); err != nil {
			return tracker.Issue{Key: key, URL: out.HTMLURL, Title: ni.Title}, fmt.Errorf("issue %s was created but not attached to parent %s: %w", key, ni.ParentKey, err)
		}
		// Attaching a parent changes the child, so read its Rev back.
		got, err := t.Issue(ctx, key)
		if err != nil {
			return tracker.Issue{Key: key, URL: out.HTMLURL, Title: ni.Title}, fmt.Errorf("issue %s was created and attached to parent %s but its revision could not be read: %w", key, ni.ParentKey, err)
		}
		rev = got.Rev
		parentRef = got.Parent
	}
	is := tracker.Issue{
		Key: key, URL: out.HTMLURL, Title: ni.Title, Description: body, Type: typeName,
		Acceptance: []string{}, Labels: append([]string{}, ni.Labels...),
		Status: "open", Rev: rev, Parent: parentRef,
	}
	return is, nil
}

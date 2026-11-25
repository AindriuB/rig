package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/AindriuB/rig/internal/tracker"
)

// newIssueWire is the create op's issue argument.
type newIssueWire struct {
	Type       string   `json:"type"`
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	Acceptance []string `json:"acceptance"`
	Labels     []string `json:"labels"`
	ParentKey  string   `json:"parentKey,omitempty"`
}

// Create makes an issue through the create op. It returns ErrUnsupported when
// the program does not report create in caps, or answers unsupported.
func (t *Tracker) Create(ctx context.Context, ni tracker.NewIssue) (tracker.Issue, error) {
	if ni.ParentKey != "" {
		if err := tracker.ValidateKey(ni.ParentKey); err != nil {
			return tracker.Issue{}, err
		}
	}
	if err := t.need(ctx, "create", func(c tracker.Caps) bool { return c.Create }); err != nil {
		return tracker.Issue{}, err
	}
	w := newIssueWire{
		Type: ni.Type, Title: ni.Title, Body: ni.Body,
		Acceptance: ni.Acceptance, Labels: ni.Labels, ParentKey: ni.ParentKey,
	}
	if w.Acceptance == nil {
		w.Acceptance = []string{}
	}
	if w.Labels == nil {
		w.Labels = []string{}
	}
	var is tracker.Issue
	err := t.call(ctx, request{Op: "create", Issue: &w}, func(r *response) json.RawMessage { return r.Issue }, &is)
	if err != nil {
		return tracker.Issue{}, err
	}
	if is.Key == "" {
		return tracker.Issue{}, errors.New("exec tracker create: response issue has no key")
	}
	return is, nil
}

// IssueTypes lists issue types through the types op. A program that predates
// the op answers unsupported, which is ErrUnsupported.
func (t *Tracker) IssueTypes(ctx context.Context) ([]tracker.IssueType, error) {
	var types []tracker.IssueType
	if err := t.call(ctx, request{Op: "types"}, func(r *response) json.RawMessage { return r.Types }, &types); err != nil {
		return nil, err
	}
	for _, it := range types {
		if it.Name == "" {
			return nil, fmt.Errorf("exec tracker types: an issue type has no name")
		}
	}
	return types, nil
}

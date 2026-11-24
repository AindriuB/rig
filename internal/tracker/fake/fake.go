// Package fake is an in-memory tracker.Tracker for tests. It makes no network
// call and holds no credential.
package fake

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/AindriuB/rig/internal/tracker"
)

// Tracker is an in-memory tracker seeded with issues.
type Tracker struct {
	mu       sync.Mutex
	caps     tracker.Caps
	issues   map[string]*tracker.Issue
	order    []string
	statuses map[string]bool
	rev      int

	// Err, when set, is returned by every method. Set it to ErrAuth or a
	// transient error to test how a caller handles a failing tracker.
	Err error

	// Project is what CheckProject returns; nil means the fake cannot check.
	Project *tracker.ProjectInfo

	// Comments records every successful Comment call.
	Comments []CommentCall
	// StatusCalls records every successful SetStatus call.
	StatusCalls []StatusCall
}

// CommentCall is one recorded Comment call.
type CommentCall struct {
	Key     string
	Comment tracker.Comment
}

// StatusCall is one recorded SetStatus call.
type StatusCall struct {
	Key, Status string
}

var (
	_ tracker.Tracker        = (*Tracker)(nil)
	_ tracker.ProjectChecker = (*Tracker)(nil)
)

// New returns a fake with the given capabilities, issues and known statuses.
func New(caps tracker.Caps, issues []tracker.Issue, statuses []string) *Tracker {
	t := &Tracker{
		caps:     caps,
		issues:   map[string]*tracker.Issue{},
		statuses: map[string]bool{},
	}
	for _, s := range statuses {
		t.statuses[s] = true
	}
	for _, is := range issues {
		t.rev++
		is.Rev = strconv.Itoa(t.rev)
		t.issues[is.Key] = &is
		t.order = append(t.order, is.Key)
	}
	return t
}

// Capabilities returns the configured capabilities.
func (t *Tracker) Capabilities(ctx context.Context) (tracker.Caps, error) {
	if t.Err != nil {
		return tracker.Caps{}, t.Err
	}
	return t.caps, nil
}

// CheckProject returns Project, or ErrUnsupported when it is nil.
func (t *Tracker) CheckProject(ctx context.Context) (tracker.ProjectInfo, error) {
	if t.Err != nil {
		return tracker.ProjectInfo{}, t.Err
	}
	if t.Project == nil {
		return tracker.ProjectInfo{}, tracker.ErrUnsupported
	}
	return *t.Project, nil
}

func ref(is *tracker.Issue) tracker.IssueRef {
	return tracker.IssueRef{Key: is.Key, URL: is.URL, Title: is.Title, Type: is.Type}
}

func (t *Tracker) get(key string) (*tracker.Issue, error) {
	if err := tracker.ValidateKey(key); err != nil {
		return nil, err
	}
	is, ok := t.issues[key]
	if !ok {
		return nil, fmt.Errorf("issue %q: %w", key, tracker.ErrNotFound)
	}
	return is, nil
}

// Issue returns a copy of the issue.
func (t *Tracker) Issue(ctx context.Context, key string) (tracker.Issue, error) {
	if t.Err != nil {
		return tracker.Issue{}, t.Err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	is, err := t.get(key)
	if err != nil {
		return tracker.Issue{}, err
	}
	return *is, nil
}

// Search returns issues carrying every label in q.Labels, up to q.Limit. The
// fake ignores q.Text.
func (t *Tracker) Search(ctx context.Context, q tracker.Query) ([]tracker.IssueRef, error) {
	if t.Err != nil {
		return nil, t.Err
	}
	if !t.caps.Search {
		return nil, fmt.Errorf("search: %w", tracker.ErrUnsupported)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []tracker.IssueRef
	for _, k := range t.order {
		is := t.issues[k]
		if !hasAll(is.Labels, q.Labels) {
			continue
		}
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
		out = append(out, ref(is))
	}
	return out, nil
}

func hasAll(have, want []string) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			if h == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Children returns the issues whose Parent is key.
func (t *Tracker) Children(ctx context.Context, key string) ([]tracker.IssueRef, error) {
	if t.Err != nil {
		return nil, t.Err
	}
	if !t.caps.Children {
		return nil, fmt.Errorf("children: %w", tracker.ErrUnsupported)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, err := t.get(key); err != nil {
		return nil, err
	}
	var out []tracker.IssueRef
	for _, k := range t.order {
		is := t.issues[k]
		if is.Parent != nil && is.Parent.Key == key {
			out = append(out, ref(is))
		}
	}
	return out, nil
}

// Comment records the comment and returns a generated id.
func (t *Tracker) Comment(ctx context.Context, key string, c tracker.Comment) (tracker.CommentResult, error) {
	if t.Err != nil {
		return tracker.CommentResult{}, t.Err
	}
	if !t.caps.Comment {
		return tracker.CommentResult{}, fmt.Errorf("comment: %w", tracker.ErrUnsupported)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, err := t.get(key); err != nil {
		return tracker.CommentResult{}, err
	}
	t.Comments = append(t.Comments, CommentCall{Key: key, Comment: c})
	id := strconv.Itoa(len(t.Comments))
	return tracker.CommentResult{ID: id, URL: "fake://" + key + "/comment/" + id}, nil
}

// SetStatus moves the issue to status and bumps its Rev when the status
// changes. A status the fake was not given returns ErrNoTransition.
func (t *Tracker) SetStatus(ctx context.Context, key, status string) (tracker.StatusResult, error) {
	if t.Err != nil {
		return tracker.StatusResult{}, t.Err
	}
	if !t.caps.SetStatus {
		return tracker.StatusResult{}, fmt.Errorf("set status: %w", tracker.ErrUnsupported)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	is, err := t.get(key)
	if err != nil {
		return tracker.StatusResult{}, err
	}
	if !t.statuses[status] {
		return tracker.StatusResult{}, fmt.Errorf("status %q: %w", status, tracker.ErrNoTransition)
	}
	res := tracker.StatusResult{From: is.Status, To: status, Changed: is.Status != status}
	if res.Changed {
		t.rev++
		is.Status = status
		is.Rev = strconv.Itoa(t.rev)
	}
	t.StatusCalls = append(t.StatusCalls, StatusCall{Key: key, Status: status})
	return res, nil
}

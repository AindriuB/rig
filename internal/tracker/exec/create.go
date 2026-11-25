package exec

import (
	"context"
	"fmt"

	"github.com/AindriuB/rig/internal/tracker"
)

// Create is not implemented yet; it returns ErrUnsupported.
func (t *Tracker) Create(ctx context.Context, ni tracker.NewIssue) (tracker.Issue, error) {
	return tracker.Issue{}, fmt.Errorf("create: %w", tracker.ErrUnsupported)
}

// IssueTypes is not implemented yet; it returns ErrUnsupported.
func (t *Tracker) IssueTypes(ctx context.Context) ([]tracker.IssueType, error) {
	return nil, fmt.Errorf("issue types: %w", tracker.ErrUnsupported)
}

// Package trackertest is the contract test every tracker adapter runs. An
// adapter's test builds the adapter against a stub (never the network) and
// calls Run.
package trackertest

import (
	"context"
	"errors"
	"testing"

	"github.com/AindriuB/rig/internal/tracker"
)

// Seed names the issues the tracker under test was built with.
type Seed struct {
	// Key is an existing issue. Status is its current status and NextStatus
	// a different status the tracker accepts.
	Key, Status, NextStatus string
	// ParentKey is an issue whose child is ChildKey. Needed when
	// Caps.Children is true.
	ParentKey, ChildKey string
	// MissingKey is a well-formed key that no issue has.
	MissingKey string
}

// Run exercises every Tracker method against what newTracker returns. For
// each capability that is false it asserts ErrUnsupported.
func Run(t *testing.T, newTracker func(t *testing.T) (tracker.Tracker, Seed)) {
	ctx := context.Background()
	setup := func(t *testing.T) (tracker.Tracker, Seed, tracker.Caps) {
		t.Helper()
		tr, seed := newTracker(t)
		caps, err := tr.Capabilities(ctx)
		if err != nil {
			t.Fatalf("Capabilities: %v", err)
		}
		return tr, seed, caps
	}

	t.Run("Issue", func(t *testing.T) {
		tr, s, _ := setup(t)
		is, err := tr.Issue(ctx, s.Key)
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if is.Key != s.Key {
			t.Errorf("Key = %q, want %q", is.Key, s.Key)
		}
		if _, err := tr.Issue(ctx, s.MissingKey); !errors.Is(err, tracker.ErrNotFound) {
			t.Errorf("Issue(missing) = %v, want ErrNotFound", err)
		}
	})

	t.Run("Search", func(t *testing.T) {
		tr, _, caps := setup(t)
		_, err := tr.Search(ctx, tracker.Query{})
		if !caps.Search {
			if !errors.Is(err, tracker.ErrUnsupported) {
				t.Errorf("Search = %v, want ErrUnsupported", err)
			}
			return
		}
		if err != nil {
			t.Errorf("Search: %v", err)
		}
	})

	t.Run("Children", func(t *testing.T) {
		tr, s, caps := setup(t)
		got, err := tr.Children(ctx, s.ParentKey)
		if !caps.Children {
			if !errors.Is(err, tracker.ErrUnsupported) {
				t.Errorf("Children = %v, want ErrUnsupported", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("Children: %v", err)
		}
		found := false
		for _, c := range got {
			found = found || c.Key == s.ChildKey
		}
		if !found {
			t.Errorf("Children(%s) = %v, missing %s", s.ParentKey, got, s.ChildKey)
		}
		if _, err := tr.Children(ctx, s.MissingKey); !errors.Is(err, tracker.ErrNotFound) {
			t.Errorf("Children(missing) = %v, want ErrNotFound", err)
		}
	})

	t.Run("Comment", func(t *testing.T) {
		tr, s, caps := setup(t)
		res, err := tr.Comment(ctx, s.Key, tracker.Comment{Body: "hello"})
		if !caps.Comment {
			if !errors.Is(err, tracker.ErrUnsupported) {
				t.Errorf("Comment = %v, want ErrUnsupported", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("Comment: %v", err)
		}
		if res.ID == "" {
			t.Errorf("Comment returned an empty ID: %+v", res)
		}
		if _, err := tr.Comment(ctx, s.MissingKey, tracker.Comment{Body: "x"}); !errors.Is(err, tracker.ErrNotFound) {
			t.Errorf("Comment(missing) = %v, want ErrNotFound", err)
		}
	})

	t.Run("SetStatus", func(t *testing.T) {
		tr, s, caps := setup(t)
		res, err := tr.SetStatus(ctx, s.Key, s.NextStatus)
		if !caps.SetStatus {
			if !errors.Is(err, tracker.ErrUnsupported) {
				t.Errorf("SetStatus = %v, want ErrUnsupported", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("SetStatus: %v", err)
		}
		if !res.Changed || res.To != s.NextStatus {
			t.Errorf("SetStatus result = %+v", res)
		}
		before, err := tr.Issue(ctx, s.Key)
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if before.Status != s.NextStatus {
			t.Errorf("Status = %q, want %q", before.Status, s.NextStatus)
		}
		if _, err := tr.SetStatus(ctx, s.Key, s.Status); err != nil {
			t.Fatalf("SetStatus back: %v", err)
		}
		after, err := tr.Issue(ctx, s.Key)
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if after.Rev == before.Rev {
			t.Errorf("Rev did not change with status: %q", after.Rev)
		}
		if _, err := tr.SetStatus(ctx, s.MissingKey, s.NextStatus); !errors.Is(err, tracker.ErrNotFound) {
			t.Errorf("SetStatus(missing) = %v, want ErrNotFound", err)
		}
	})
}

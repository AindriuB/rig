// Package trackertest is the contract test every tracker adapter runs. An
// adapter's test builds the adapter against a stub (never the network) and
// calls Run.
package trackertest

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/AindriuB/rig/internal/tracker"
)

// Seed names the issues the tracker under test was built with.
type Seed struct {
	// Key is an existing issue. Status is its current status and NextStatus
	// a different status the tracker accepts.
	Key, Status, NextStatus string
	// ParentKey is an issue whose child is ChildKey, which has no children
	// of its own. Needed when
	// Caps.Children is true.
	ParentKey, ChildKey string
	// Label is carried by at least two issues, and the tracker holds at
	// least one issue without it.
	Label string
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

	t.Run("MalformedKeys", func(t *testing.T) {
		tr, _, _ := setup(t)
		bad := []string{"", "a b", "a/b", "a?b", "a#b", "a%b", "..", "a\u200bb", "a\u200fb", "\ufeffa", "a\xffb"}
		// A malformed key must be refused before the tracker is asked, so
		// ErrNotFound (a lookup that found nothing) is not an acceptable
		// refusal either.
		check := func(op, k string, err error) {
			t.Helper()
			if err == nil {
				t.Errorf("%s(%q) succeeded", op, k)
			} else if errors.Is(err, tracker.ErrNotFound) {
				t.Errorf("%s(%q) = %v: looked the key up instead of refusing it", op, k, err)
			}
		}
		for _, k := range bad {
			_, err := tr.Issue(ctx, k)
			check("Issue", k, err)
			_, err = tr.Children(ctx, k)
			check("Children", k, err)
			_, err = tr.Comment(ctx, k, tracker.Comment{Body: "x"})
			check("Comment", k, err)
			_, err = tr.SetStatus(ctx, k, "x")
			check("SetStatus", k, err)
		}
	})

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
		tr, s, caps := setup(t)
		_, err := tr.Search(ctx, tracker.Query{})
		if !caps.Search {
			if !errors.Is(err, tracker.ErrUnsupported) {
				t.Errorf("Search = %v, want ErrUnsupported", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		limited, err := tr.Search(ctx, tracker.Query{Labels: []string{s.Label}, Limit: 1})
		if err != nil {
			t.Fatalf("Search(limit 1): %v", err)
		}
		if len(limited) != 1 {
			t.Errorf("Search(label, Limit 1) returned %d issues, want 1", len(limited))
		}
		labelled, err := tr.Search(ctx, tracker.Query{Labels: []string{s.Label}})
		if err != nil {
			t.Fatalf("Search(label): %v", err)
		}
		if len(labelled) < 2 {
			t.Errorf("Search(label) returned %d issues, want at least 2", len(labelled))
		}
		for _, ref := range labelled {
			is, err := tr.Issue(ctx, ref.Key)
			if err != nil {
				t.Fatalf("Issue(%s): %v", ref.Key, err)
			}
			if !slices.Contains(is.Labels, s.Label) {
				t.Errorf("Search(label %q) returned %s with labels %v", s.Label, ref.Key, is.Labels)
			}
		}
		none, err := tr.Search(ctx, tracker.Query{Labels: []string{"rig-no-such-label"}})
		if err != nil {
			t.Fatalf("Search(no such label): %v", err)
		}
		if len(none) != 0 {
			t.Errorf("Search(no such label) = %v, want none", none)
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
		for _, c := range got {
			if c.Key == s.ParentKey {
				t.Errorf("Children(%s) lists the parent itself", s.ParentKey)
			}
		}
		leaf, err := tr.Children(ctx, s.ChildKey)
		if err != nil {
			t.Fatalf("Children(leaf): %v", err)
		}
		if len(leaf) != 0 {
			t.Errorf("Children(%s) = %v, want none for a leaf", s.ChildKey, leaf)
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
		if !res.Changed || res.To != s.NextStatus || res.From != s.Status {
			t.Errorf("SetStatus result = %+v, want From %q To %q Changed", res, s.Status, s.NextStatus)
		}
		same, err := tr.SetStatus(ctx, s.Key, s.NextStatus)
		if err != nil {
			t.Fatalf("SetStatus to the current status: %v", err)
		}
		if same.Changed || same.From != s.NextStatus || same.To != s.NextStatus {
			t.Errorf("SetStatus to the current status = %+v, want unchanged", same)
		}
		if _, err := tr.SetStatus(ctx, s.Key, "rig-no-such-status"); !errors.Is(err, tracker.ErrNoTransition) {
			t.Errorf("SetStatus(unknown status) = %v, want ErrNoTransition", err)
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

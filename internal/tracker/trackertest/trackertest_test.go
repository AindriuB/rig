package trackertest_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/tracker"
	"github.com/AindriuB/rig/internal/tracker/fake"
	"github.com/AindriuB/rig/internal/tracker/trackertest"
)

func build(caps tracker.Caps) func(t *testing.T) (tracker.Tracker, trackertest.Seed) {
	return func(t *testing.T) (tracker.Tracker, trackertest.Seed) {
		parent := tracker.IssueRef{Key: "P-1"}
		f := fake.New(caps, []tracker.Issue{
			{Key: "P-1", Title: "parent", Status: "todo", Labels: []string{"rig"}},
			{Key: "P-2", Title: "child", Status: "todo", Parent: &parent, Labels: []string{"rig"}},
			{Key: "P-3", Title: "other", Status: "todo", Labels: []string{"other"}},
		}, []string{"todo", "done"})
		return f, trackertest.Seed{Key: "P-1", Status: "todo", NextStatus: "done",
			ParentKey: "P-1", ChildKey: "P-2", Label: "rig", MissingKey: "P-404"}
	}
}

// AC1: the fake passes the contract with all caps on and with each off.
func TestFakePassesContract(t *testing.T) {
	all := tracker.Caps{Parent: true, Children: true, Types: true, Search: true, Comment: true, SetStatus: true}
	t.Run("all", func(t *testing.T) { trackertest.Run(t, build(all)) })
	offs := map[string]func(*tracker.Caps){
		"Parent":    func(c *tracker.Caps) { c.Parent = false },
		"Children":  func(c *tracker.Caps) { c.Children = false },
		"Types":     func(c *tracker.Caps) { c.Types = false },
		"Search":    func(c *tracker.Caps) { c.Search = false },
		"Comment":   func(c *tracker.Caps) { c.Comment = false },
		"SetStatus": func(c *tracker.Caps) { c.SetStatus = false },
	}
	for name, off := range offs {
		c := all
		off(&c)
		t.Run("no"+name, func(t *testing.T) { trackertest.Run(t, build(c)) })
	}
}

// AC2: the sentinels are distinguishable through wrapping.
func TestSentinelsDistinct(t *testing.T) {
	errs := []error{tracker.ErrUnsupported, tracker.ErrNotFound, tracker.ErrAuth, tracker.ErrNoTransition}
	for i, e := range errs {
		w := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", e))
		for j, target := range errs {
			if got := errors.Is(w, target); got != (i == j) {
				t.Errorf("errors.Is(wrap(%v), %v) = %v", e, target, got)
			}
		}
	}
}

// AC3: key validation.
func TestValidateKey(t *testing.T) {
	for _, k := range []string{"A-1", "owner-repo-12", strings.Repeat("a", 256)} {
		if err := tracker.ValidateKey(k); err != nil {
			t.Errorf("ValidateKey(%q) = %v", k, err)
		}
	}
	for _, k := range []string{"", "A 1", " A-1", "A-1\n", "A\t1", "A\x00", "A 1", strings.Repeat("a", 257),
		"a/b", "a?b", "a#b", "a%b", "..", "a..b", "a\u200fb", "a\u200bb", "\ufeffa", "a\xffb"} {
		if err := tracker.ValidateKey(k); err == nil {
			t.Errorf("ValidateKey(%q) accepted", k)
		}
	}
}

// The fake records calls and rejects unknown statuses.
func TestFakeRecordsAndRejects(t *testing.T) {
	f := fake.New(tracker.Caps{Comment: true, SetStatus: true},
		[]tracker.Issue{{Key: "A-1", Status: "todo"}}, []string{"todo", "done"})
	ctx := t.Context()
	if _, err := f.Comment(ctx, "A-1", tracker.Comment{Body: "hi"}); err != nil || len(f.Comments) != 1 {
		t.Fatalf("Comment: %v, %v", err, f.Comments)
	}
	if _, err := f.SetStatus(ctx, "A-1", "bogus"); !errors.Is(err, tracker.ErrNoTransition) {
		t.Errorf("unknown status = %v, want ErrNoTransition", err)
	}
	if _, err := f.SetStatus(ctx, "A-1", "done"); err != nil || len(f.StatusCalls) != 1 {
		t.Fatalf("SetStatus: %v, %v", err, f.StatusCalls)
	}
}

// A fake with Err set fails Capabilities with that error.
func TestFakeCapabilitiesErr(t *testing.T) {
	f := fake.New(tracker.Caps{}, nil, nil)
	f.Err = fmt.Errorf("login: %w", tracker.ErrAuth)
	if _, err := f.Capabilities(t.Context()); !errors.Is(err, tracker.ErrAuth) {
		t.Errorf("Capabilities = %v, want ErrAuth", err)
	}
}

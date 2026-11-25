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
	all := tracker.Caps{Parent: true, Children: true, Types: true, Search: true, Comment: true, SetStatus: true, Create: true}
	t.Run("all", func(t *testing.T) { trackertest.Run(t, build(all)) })
	offs := map[string]func(*tracker.Caps){
		"Parent":    func(c *tracker.Caps) { c.Parent = false },
		"Children":  func(c *tracker.Caps) { c.Children = false },
		"Types":     func(c *tracker.Caps) { c.Types = false },
		"Search":    func(c *tracker.Caps) { c.Search = false },
		"Comment":   func(c *tracker.Caps) { c.Comment = false },
		"SetStatus": func(c *tracker.Caps) { c.SetStatus = false },
		"Create":    func(c *tracker.Caps) { c.Create = false },
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

// A fake with Err set fails every method with that error, ahead of the
// capability and key checks.
func TestFakeErr(t *testing.T) {
	caps := tracker.Caps{Children: true, Search: true, Comment: true, SetStatus: true}
	transient := errors.New("connection reset")
	for _, want := range []error{tracker.ErrAuth, transient} {
		f := fake.New(caps, []tracker.Issue{{Key: "A-1", Status: "todo"}}, []string{"todo", "done"})
		f.Err = want
		ctx := t.Context()
		calls := map[string]func() error{
			"Capabilities": func() error { _, err := f.Capabilities(ctx); return err },
			"Issue":        func() error { _, err := f.Issue(ctx, "A-1"); return err },
			"Search":       func() error { _, err := f.Search(ctx, tracker.Query{}); return err },
			"Children":     func() error { _, err := f.Children(ctx, "A-1"); return err },
			"Comment":      func() error { _, err := f.Comment(ctx, "A-1", tracker.Comment{Body: "x"}); return err },
			"SetStatus":    func() error { _, err := f.SetStatus(ctx, "A-1", "done"); return err },
		}
		for name, call := range calls {
			if err := call(); !errors.Is(err, want) {
				t.Errorf("%s with Err %v = %v", name, want, err)
			}
		}
		if len(f.Comments) != 0 || len(f.StatusCalls) != 0 {
			t.Errorf("a failing fake recorded calls: %v %v", f.Comments, f.StatusCalls)
		}
	}
}

func TestFakeCreateRecordsAndFails(t *testing.T) {
	lvl := 2
	f := fake.New(tracker.Caps{Create: true}, nil, nil)
	f.Types = []tracker.IssueType{{Name: "Epic", Level: &lvl}, {Name: "Task"}}
	f.FailCreateN = 2
	if _, err := f.Create(t.Context(), tracker.NewIssue{Title: "a"}); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := f.Create(t.Context(), tracker.NewIssue{Title: "b"}); err == nil {
		t.Fatal("second Create succeeded, want injected error")
	}
	if _, err := f.Create(t.Context(), tracker.NewIssue{Title: "c"}); err != nil {
		t.Fatalf("third Create: %v", err)
	}
	if len(f.Created) != 2 || f.Created[1].Title != "c" {
		t.Errorf("Created = %+v", f.Created)
	}
	types, err := f.IssueTypes(t.Context())
	if err != nil || len(types) != 2 || *types[0].Level != 2 || types[1].Level != nil {
		t.Errorf("IssueTypes = %+v, %v", types, err)
	}
	if _, err := fake.New(tracker.Caps{}, nil, nil).IssueTypes(t.Context()); !errors.Is(err, tracker.ErrUnsupported) {
		t.Errorf("IssueTypes without types = %v", err)
	}
}

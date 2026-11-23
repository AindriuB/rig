//go:build !windows

package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/tracker"
	"github.com/AindriuB/rig/internal/tracker/trackertest"
)

var ctx = context.Background()

func newTracker(t *testing.T, hierarchy bool) (*Tracker, string) {
	t.Helper()
	dir := fakeEnv(t, hierarchy)
	tr, err := New(Options{Project: fakeProject, Host: "gitlab.example"})
	if err != nil {
		t.Fatal(err)
	}
	return tr, dir
}

// AC1: the contract passes with the work-item hierarchy on and off.
func TestContract(t *testing.T) {
	for _, hier := range []bool{true, false} {
		name := "hierarchy off"
		if hier {
			name = "hierarchy on"
		}
		t.Run(name, func(t *testing.T) {
			trackertest.Run(t, func(t *testing.T) (tracker.Tracker, trackertest.Seed) {
				tr, _ := newTracker(t, hier)
				return tr, trackertest.Seed{Key: "3", Status: "opened", NextStatus: "closed",
					ParentKey: "1", ChildKey: "2", Label: "rig", MissingKey: "99"}
			})
			tr, _ := newTracker(t, hier)
			caps, err := tr.Capabilities(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if caps.Parent != hier || caps.Children != hier {
				t.Errorf("caps = %+v, want Parent and Children %v", caps, hier)
			}
			if _, err := tr.Children(ctx, "1"); errors.Is(err, tracker.ErrUnsupported) == hier {
				t.Errorf("Children err = %v with hierarchy %v", err, hier)
			}
		})
	}
}

func TestIssueFields(t *testing.T) {
	tr, _ := newTracker(t, true)
	is, err := tr.Issue(ctx, "2")
	if err != nil {
		t.Fatal(err)
	}
	if is.Status != "opened" || is.Type != "issue" || is.Rev == "" || is.Parent == nil || is.Parent.Key != "1" {
		t.Errorf("issue = %+v", is)
	}
	c, err := tr.Comment(ctx, "2", tracker.Comment{Body: "hi"})
	if err != nil || !strings.HasPrefix(c.URL, issueURL(2)+"#note_") {
		t.Errorf("comment = %+v, %v", c, err)
	}
}

func TestSetStatusNamesTheStates(t *testing.T) {
	tr, _ := newTracker(t, false)
	_, err := tr.SetStatus(ctx, "1", "in progress")
	if !errors.Is(err, tracker.ErrNoTransition) || !strings.Contains(err.Error(), "opened") || !strings.Contains(err.Error(), "closed") {
		t.Errorf("err = %v", err)
	}
	res, err := tr.SetStatus(ctx, "1", "open")
	if err != nil || res.Changed {
		t.Errorf("open on an open issue = %+v, %v", res, err)
	}
}

func TestAuthFailure(t *testing.T) {
	tr, dir := newTracker(t, false)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"noauth":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Capabilities(ctx); !errors.Is(err, tracker.ErrAuth) {
		t.Errorf("Capabilities = %v, want ErrAuth", err)
	}
}

// AC2: hostile text reaches glab only as one -f argv element or stdin JSON.
func TestHostileTextIsOneArgument(t *testing.T) {
	hostile := []string{"a'b\"c", "@/etc/passwd", "$(x)", "line1\nline2", "&per_page=1", "../../x?y=1#z"}
	for _, h := range hostile {
		tr, dir := newTracker(t, true)
		if _, err := tr.Search(ctx, tracker.Query{Text: h, Labels: []string{h, "b"}}); err != nil {
			t.Fatalf("Search(%q): %v", h, err)
		}
		if _, err := tr.Comment(ctx, "1", tracker.Comment{Body: h}); err != nil {
			t.Fatalf("Comment(%q): %v", h, err)
		}
		if _, err := tr.SetStatus(ctx, "1", h); err == nil {
			t.Fatalf("SetStatus(%q) succeeded", h)
		}
		seen := 0
		for _, c := range readCalls(t, dir) {
			for i, a := range c.Args {
				switch {
				case strings.Contains(a, h) && !strings.Contains(h, "\n"):
					// Only as the value of a -f field.
					if i == 0 || c.Args[i-1] != "-f" || !(strings.HasPrefix(a, "search=") || strings.HasPrefix(a, "labels=") || strings.HasPrefix(a, "body=")) {
						t.Errorf("hostile %q in argv %q at %d (%q)", h, a, i, c.Args)
					}
					seen++
				case strings.Contains(h, "\n") && strings.Contains(a, "line2"):
					if c.Args[i-1] != "-f" {
						t.Errorf("hostile %q in argv %q", h, a)
					}
					seen++
				}
				if strings.HasPrefix(a, "-F") || a == "--field" || a == "--raw-field" {
					t.Errorf("glab call uses %q: %q", a, c.Args)
				}
			}
			if c.Stdin != "" {
				var v map[string]any
				if err := json.Unmarshal([]byte(c.Stdin), &v); err != nil {
					t.Errorf("stdin is not JSON: %q", c.Stdin)
				}
				if strings.Contains(c.Stdin, "per_page=1") {
					t.Errorf("hostile text in a GraphQL body: %q", c.Stdin)
				}
			}
		}
		if seen == 0 {
			t.Errorf("hostile %q never reached glab", h)
		}
	}
}

// AC3: malformed keys never start glab.
func TestMalformedKeysNeverStartGlab(t *testing.T) {
	tr, dir := newTracker(t, true)
	for _, k := range []string{"g/p#1", "01", "0", "1x", "-1", "+1", "1 ", "99999999999999999999", "٣"} {
		if _, err := tr.Issue(ctx, k); err == nil {
			t.Errorf("Issue(%q) succeeded", k)
		}
		if _, err := tr.Children(ctx, k); err == nil {
			t.Errorf("Children(%q) succeeded", k)
		}
		if _, err := tr.Comment(ctx, k, tracker.Comment{Body: "x"}); err == nil {
			t.Errorf("Comment(%q) succeeded", k)
		}
		if _, err := tr.SetStatus(ctx, k, "closed"); err == nil {
			t.Errorf("SetStatus(%q) succeeded", k)
		}
	}
	if calls := readCalls(t, dir); len(calls) != 0 {
		t.Errorf("glab was started: %v", calls)
	}
}

// AC4: RIG_JIRA_TOKEN in the parent environment never reaches glab.
func TestTokenNotInherited(t *testing.T) {
	t.Setenv("RIG_JIRA_TOKEN", "secret")
	tr, dir := newTracker(t, true)
	if _, err := tr.Capabilities(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Issue(ctx, "1"); err != nil {
		t.Fatal(err)
	}
	calls := readCalls(t, dir)
	if len(calls) == 0 {
		t.Fatal("no calls")
	}
	for _, c := range calls {
		if c.TokenSeen {
			t.Errorf("glab saw RIG_JIRA_TOKEN: %q", c.Args)
		}
	}
}

//go:build !windows

package github

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/tracker"
	"github.com/AindriuB/rig/internal/tracker/trackertest"
)

var ctx = context.Background()

func seedState(org bool) fakeState {
	return fakeState{
		Owner: "o", Name: "r", OwnerOrg: org,
		Issues: map[string]fakeIssue{
			"1": {Title: "parent", Body: "the parent", State: "OPEN", Updated: 1, Labels: []string{"rig"}, Type: "Feature"},
			"2": {Title: "child", State: "OPEN", Updated: 2, Labels: []string{"rig"}, Type: "Task", Parent: 1},
			"3": {Title: "other", State: "OPEN", Updated: 3, Labels: []string{"x"}},
			"4": {Title: "a pull request", State: "OPEN", Updated: 4, PR: true},
		},
	}
}

func newTracker(t *testing.T, st fakeState) *Tracker {
	t.Helper()
	fakeGH(t, st)
	tr, err := New(Options{Repo: "o/r", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// AC1: the contract passes with an organisation owner (types on) and with a
// user owner (types off).
func TestContract(t *testing.T) {
	for _, org := range []bool{true, false} {
		name := map[bool]string{true: "org", false: "user"}[org]
		t.Run(name, func(t *testing.T) {
			trackertest.Run(t, func(t *testing.T) (tracker.Tracker, trackertest.Seed) {
				st := seedState(org)
				if !org {
					for k, is := range st.Issues {
						is.Type = ""
						st.Issues[k] = is
					}
				}
				tr := newTracker(t, st)
				caps, err := tr.Capabilities(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if caps.Types != org || !caps.Parent || !caps.Children || !caps.Search || !caps.Comment || !caps.SetStatus {
					t.Fatalf("caps = %+v", caps)
				}
				if len(caps.Levels) != 0 {
					t.Fatalf("Levels = %v, want none", caps.Levels)
				}
				return tr, trackertest.Seed{Key: "1", Status: "open", NextStatus: "closed",
					ParentKey: "1", ChildKey: "2", Label: "rig", MissingKey: "999"}
			})
		})
	}
}

func TestIssueMapping(t *testing.T) {
	tr := newTracker(t, seedState(true))
	is, err := tr.Issue(ctx, "2")
	if err != nil {
		t.Fatal(err)
	}
	if is.Key != "2" || is.Title != "child" || is.Status != "open" || is.Type != "Task" ||
		is.Assignee != "ann" || is.Rev == "" || is.URL != "https://github.com/o/r/issues/2" ||
		is.Parent == nil || is.Parent.Key != "1" || is.Parent.Type != "Feature" ||
		len(is.Acceptance) != 0 || len(is.Labels) != 1 || is.Labels[0] != "rig" {
		t.Fatalf("issue = %+v parent=%+v", is, is.Parent)
	}
	top, _ := tr.Issue(ctx, "1")
	if top.Parent != nil || top.Description != "the parent" {
		t.Fatalf("top = %+v", top)
	}
}

func TestSetStatusCaseInsensitiveAndNoCallWhenSame(t *testing.T) {
	st := seedState(true)
	_, calls := fakeGH(t, st)
	tr, _ := New(Options{Repo: "o/r"})
	if res, err := tr.SetStatus(ctx, "1", "OPEN"); err != nil || res.Changed || res.From != "open" || res.To != "open" {
		t.Fatalf("same = %+v, %v", res, err)
	}
	for _, c := range calls() {
		if strings.Contains(strings.Join(c, " "), "PATCH") {
			t.Fatalf("a no-op SetStatus wrote: %q", c)
		}
	}
	_, err := tr.SetStatus(ctx, "1", "in progress")
	if !errors.Is(err, tracker.ErrNoTransition) || !strings.Contains(err.Error(), "open") || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("err = %v", err)
	}
	if res, err := tr.SetStatus(ctx, "1", "Closed"); err != nil || !res.Changed || res.To != "closed" {
		t.Fatalf("close = %+v, %v", res, err)
	}
}

// AC2: hostile text reaches gh only as one argv element or as stdin JSON.
func TestHostileTextStaysData(t *testing.T) {
	hostile := "a\"b 'c' @/etc/passwd $(x) `y` }\n{ ; rm -rf /"
	st := seedState(true)
	_, calls := fakeGH(t, st)
	tr, err := New(Options{Repo: "o/r"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Search(ctx, tracker.Query{Text: hostile, Labels: []string{hostile}}); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Comment(ctx, "1", tracker.Comment{Body: hostile}); err != nil {
		t.Fatal(err)
	}
	var sawQ, sawStdin bool
	for _, c := range calls() {
		var endpoint string
		for i, a := range c {
			if strings.HasPrefix(a, "STDIN=") || strings.HasPrefix(a, "TOKEN=") {
				continue
			}
			isFlagValue := i > 0 && (c[i-1] == "-f" || c[i-1] == "-F" || c[i-1] == "--raw-field")
			if !isFlagValue && !strings.HasPrefix(a, "-") && i > 0 && c[i-1] != "--method" && c[i-1] != "--hostname" && c[i-1] != "-H" && a != "api" {
				endpoint = a
			}
			// -F coerces and reads files: only validated numbers go through it.
			if c[i] == "-F" {
				k, v, _ := strings.Cut(c[i+1], "=")
				if k != "number" && k != "per_page" || strings.Trim(v, "0123456789") != "" {
					t.Errorf("-F with %q", c[i+1])
				}
			}
			if strings.Contains(a, "etc/passwd") {
				if !isFlagValue || !strings.HasPrefix(c[i-1], "-f") && c[i-1] != "--raw-field" {
					t.Errorf("hostile text outside a -f value: %q", c)
				}
				if !strings.HasPrefix(a, "q=") {
					t.Errorf("hostile text under a key other than q: %q", a)
				}
				sawQ = true
			}
		}
		if strings.Contains(endpoint, "etc") || strings.ContainsAny(endpoint, "\"'$`{}\n ") {
			t.Errorf("endpoint %q carries untrusted text: %q", endpoint, c)
		}
		if strings.Contains(strings.Join(c, "\x00"), "etc/passwd") && strings.Contains(strings.Join(c, " "), "query=") {
			t.Errorf("hostile text next to a GraphQL document: %q", c)
		}
		if in := c[len(c)-2]; strings.HasPrefix(in, "STDIN=") && strings.Contains(in, "etc/passwd") {
			if !strings.HasPrefix(in, `STDIN={"body":"`) {
				t.Errorf("stdin is not a JSON body: %q", in)
			}
			sawStdin = true
		}
	}
	if !sawQ || !sawStdin {
		t.Fatalf("sawQ=%v sawStdin=%v", sawQ, sawStdin)
	}
}

// AC3: a malformed key never starts gh.
func TestMalformedKeysNeverStartGh(t *testing.T) {
	_, calls := fakeGH(t, seedState(true))
	tr, _ := New(Options{Repo: "o/r"})
	for _, k := range []string{"o/r#1", "01", "1x", "0", "-1", "+1", "99999999999999999999", "٣", " 1"} {
		if _, err := tr.Issue(ctx, k); err == nil || errors.Is(err, tracker.ErrNotFound) {
			t.Errorf("Issue(%q) = %v", k, err)
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
	if got := calls(); len(got) != 0 {
		t.Fatalf("gh was started: %q", got)
	}
}

func TestNewRefusesBadRepo(t *testing.T) {
	for _, r := range []string{"", "o", "o/", "/r", "o/r/x", "o/r#1", "o/../r", "o r/x", "-o/r", "o/r?x"} {
		if _, err := New(Options{Repo: r}); err == nil {
			t.Errorf("New(%q) succeeded", r)
		}
	}
	if _, err := New(Options{Repo: "o/r", Host: "-evil"}); err == nil {
		t.Error("New with host -evil succeeded")
	}
}

func TestHostnameFlag(t *testing.T) {
	_, calls := fakeGH(t, seedState(true))
	tr, _ := New(Options{Repo: "o/r", Host: "ghe.example.com"})
	if _, err := tr.Issue(ctx, "1"); err != nil {
		t.Fatal(err)
	}
	c := calls()[0]
	if c[1] != "--hostname" || c[2] != "ghe.example.com" {
		t.Fatalf("argv = %q", c)
	}
}

// AC4: a RIG_JIRA_TOKEN in rig's environment never reaches gh.
func TestTokenNotSeenByGh(t *testing.T) {
	t.Setenv("RIG_JIRA_TOKEN", "s3cret-token")
	_, calls := fakeGH(t, seedState(true))
	tr, _ := New(Options{Repo: "o/r"})
	_, _ = tr.Capabilities(ctx)
	_, _ = tr.Issue(ctx, "1")
	_, _ = tr.Search(ctx, tracker.Query{})
	_, _ = tr.Children(ctx, "1")
	_, _ = tr.Comment(ctx, "1", tracker.Comment{Body: "x"})
	_, _ = tr.SetStatus(ctx, "1", "closed")
	got := calls()
	if len(got) < 7 {
		t.Fatalf("only %d calls", len(got))
	}
	for _, c := range got {
		if tok := c[len(c)-1]; tok != "TOKEN=" {
			t.Errorf("gh saw the token: %q", c)
		}
	}
}

// AC5: a pull request number is not an issue; a failed login is ErrAuth.
func TestPullRequestAndAuth(t *testing.T) {
	st := seedState(true)
	tr := newTracker(t, st)
	if _, err := tr.Issue(ctx, "4"); !errors.Is(err, tracker.ErrNotFound) {
		t.Errorf("Issue(PR) = %v, want ErrNotFound", err)
	}
	if _, err := tr.Children(ctx, "4"); !errors.Is(err, tracker.ErrNotFound) {
		t.Errorf("Children(PR) = %v, want ErrNotFound", err)
	}

	st.AuthFail = true
	path, _ := fakeGH(t, st)
	_ = path
	tr, _ = New(Options{Repo: "o/r"})
	if _, err := tr.Capabilities(ctx); !errors.Is(err, tracker.ErrAuth) {
		t.Errorf("Capabilities = %v, want ErrAuth", err)
	}
	if _, err := tr.Issue(ctx, "1"); !errors.Is(err, tracker.ErrAuth) {
		t.Errorf("Issue = %v, want ErrAuth", err)
	}
}

func TestSearchDefaultLimit(t *testing.T) {
	st := fakeState{Owner: "o", Name: "r", Issues: map[string]fakeIssue{}}
	for i := 1; i <= 60; i++ {
		st.Issues[strconv.Itoa(i)] = fakeIssue{Title: "t", State: "OPEN"}
	}
	tr := newTracker(t, st)
	got, err := tr.Search(ctx, tracker.Query{})
	if err != nil || len(got) != 50 {
		t.Fatalf("len = %d, %v", len(got), err)
	}
}

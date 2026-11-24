package jira

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/AindriuB/rig/internal/tracker"
	"github.com/AindriuB/rig/internal/tracker/trackertest"
)

var ctx = context.Background()

func seed() trackertest.Seed {
	return trackertest.Seed{
		Key: "RIG-2", Status: "To Do", NextStatus: "In Progress",
		ParentKey: "RIG-2", ChildKey: "RIG-3", Label: "rig", MissingKey: "RIG-99",
	}
}

func adapter(t *testing.T, s *stub, edit func(*Config)) *Adapter {
	t.Helper()
	cfg := s.config()
	if edit != nil {
		edit(&cfg)
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// AC1: both dialects pass the contract against recorded responses.
func TestContractV3(t *testing.T) {
	trackertest.Run(t, func(t *testing.T) (tracker.Tracker, trackertest.Seed) {
		s := newStub(t, 3)
		s.pageCap = 2
		return adapter(t, s, nil), seed()
	})
}

func TestContractV2(t *testing.T) {
	trackertest.Run(t, func(t *testing.T) (tracker.Tracker, trackertest.Seed) {
		s := newStub(t, 2)
		s.pageCap = 2
		return adapter(t, s, nil), seed()
	})
}

// 051 item 4: CheckProject tells a project that exists from one that does not,
// and an empty one from one with issues.
func TestCheckProject(t *testing.T) {
	for _, version := range []int{3, 2} {
		for _, c := range []struct {
			project string
			want    tracker.ProjectInfo
		}{
			{"RIG", tracker.ProjectInfo{Key: "RIG", Exists: true}},
			{"EMPTY", tracker.ProjectInfo{Key: "EMPTY", Exists: true, Empty: true}},
			{"NOPE", tracker.ProjectInfo{Key: "NOPE"}},
		} {
			s := newStub(t, version)
			a := adapter(t, s, func(cfg *Config) { cfg.Project = c.project })
			got, err := a.CheckProject(ctx)
			if err != nil || got != c.want {
				t.Errorf("v%d %s: %+v, %v; want %+v", version, c.project, got, err, c.want)
			}
		}
	}
	a := adapter(t, newStub(t, 3), func(cfg *Config) { cfg.Project = "" })
	if _, err := a.CheckProject(ctx); !errors.Is(err, tracker.ErrUnsupported) {
		t.Errorf("no project: %v", err)
	}
	var _ tracker.ProjectChecker = a
}

func TestIssueMapping(t *testing.T) {
	for _, version := range []int{3, 2} {
		s := newStub(t, version)
		a := adapter(t, s, func(c *Config) { c.AcceptanceField = "customfield_10100" })
		is, err := a.Issue(ctx, "RIG-2")
		if err != nil {
			t.Fatalf("v%d: %v", version, err)
		}
		if is.Title != "Cache the session lookup" || is.Type != "Story" || is.Status != "To Do" || is.Priority != "Medium" {
			t.Errorf("v%d: %+v", version, is)
		}
		if !strings.HasPrefix(is.Description, "Use the **session cache**.") {
			t.Errorf("v%d description %q", version, is.Description)
		}
		if want := []string{"Lookup is cached for 5 minutes", "Cache is cleared on logout"}; !slices.Equal(is.Acceptance, want) {
			t.Errorf("v%d acceptance %q", version, is.Acceptance)
		}
		if !slices.Equal(is.Labels, []string{"rig"}) || is.Assignee != "" || is.Rev != "2025-03-02T10:00:00.000+0000" {
			t.Errorf("v%d: %+v", version, is)
		}
		if is.URL != s.srv.URL+"/browse/RIG-2" {
			t.Errorf("v%d URL %q", version, is.URL)
		}
		if is.Parent == nil || is.Parent.Key != "RIG-1" || is.Parent.URL != s.srv.URL+"/browse/RIG-1" {
			t.Errorf("v%d parent %+v", version, is.Parent)
		}
		if version == 3 && (is.Parent.Title != "Faster login" || is.Parent.Type != "Epic") {
			t.Errorf("v3 parent %+v", is.Parent)
		}
		epic, err := a.Issue(ctx, "RIG-1")
		if err != nil || epic.Assignee != "Ann Lee" || epic.Parent != nil || len(epic.Acceptance) != 0 {
			t.Errorf("v%d epic %+v, %v", version, epic, err)
		}
		sub, err := a.Issue(ctx, "RIG-3")
		if err != nil || sub.Parent == nil || sub.Parent.Key != "RIG-2" || sub.Description != "" {
			t.Errorf("v%d sub-task %+v, %v", version, sub, err)
		}
	}
}

// AC7: acceptanceField maps Acceptance; unset leaves it empty.
func TestAcceptanceUnset(t *testing.T) {
	for _, version := range []int{3, 2} {
		s := newStub(t, version)
		is, err := adapter(t, s, nil).Issue(ctx, "RIG-2")
		if err != nil || is.Acceptance == nil || len(is.Acceptance) != 0 {
			t.Errorf("v%d: acceptance %#v, %v", version, is.Acceptance, err)
		}
		for _, r := range s.reqs {
			if strings.Contains(r, "customfield_10100") {
				t.Errorf("v%d: asked for the acceptance field: %s", version, r)
			}
		}
	}
}

func TestAcceptanceShapes(t *testing.T) {
	a := &Adapter{}
	for _, c := range []struct {
		raw  string
		d    dialect
		want []string
	}{
		{`"one\n\n- two\n3. three\n"`, v3, []string{"one", "two", "three"}},
		{`"* a\n* b"`, v2, []string{"a", "b"}},
		{`["x", "y"]`, v3, []string{"x", "y"}},
		{`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"p1"}]},{"type":"paragraph","content":[{"type":"text","text":"p2"}]}]}`, v3, []string{"p1", "p2"}},
		{`null`, v3, nil}, {`42`, v3, nil}, {`{"value":"x"}`, v3, nil},
	} {
		got, err := a.acceptance([]byte(c.raw), c.d)
		if err != nil && c.raw != `{"value":"x"}` {
			t.Errorf("%s: %v", c.raw, err)
		}
		if !slices.Equal(got, c.want) && len(got)+len(c.want) > 0 {
			t.Errorf("%s: got %q want %q", c.raw, got, c.want)
		}
	}
}

// AC2: auto reads serverInfo once per instance; an explicit dialect never does.
func TestAutoDialect(t *testing.T) {
	for _, c := range []struct {
		version int
		path    string
	}{{3, "/rest/api/3/issue/RIG-2"}, {2, "/rest/api/2/issue/RIG-2"}} {
		s := newStub(t, c.version)
		a := adapter(t, s, func(cfg *Config) { cfg.API = "auto" })
		for i := 0; i < 3; i++ {
			if _, err := a.Issue(ctx, "RIG-2"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := a.Search(ctx, tracker.Query{}); err != nil {
			t.Fatal(err)
		}
		if n := s.count("/rest/api/2/serverInfo"); n != 1 {
			t.Errorf("v%d: serverInfo hit %d times", c.version, n)
		}
		if s.count(c.path) != 3 {
			t.Errorf("v%d: issue not read through %s: %v", c.version, c.path, s.paths())
		}
		// A second instance asks again; the cache is per instance.
		b := adapter(t, s, func(cfg *Config) { cfg.API = "auto" })
		if _, err := b.Capabilities(ctx); err != nil {
			t.Fatal(err)
		}
		if n := s.count("/rest/api/2/serverInfo"); n != 2 {
			t.Errorf("v%d: second instance: serverInfo hit %d times", c.version, n)
		}
	}
	for _, api := range []string{"v2", "v3"} {
		version := int(api[1] - '0')
		s := newStub(t, version)
		a := adapter(t, s, func(cfg *Config) { cfg.API = api })
		if _, err := a.Issue(ctx, "RIG-2"); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Capabilities(ctx); err != nil {
			t.Fatal(err)
		}
		if n := s.count("/rest/api/2/serverInfo"); n != 0 {
			t.Errorf("%s: serverInfo hit %d times", api, n)
		}
	}
}

// AC3: a redirect is refused, to another host and to the same one, and the
// second server never sees Authorization. Both servers are on 127.0.0.1, whose
// host name is the same: net/http would send Authorization to the other port,
// so the refusal here is the adapter's own.
func TestRedirectRefused(t *testing.T) {
	var mu sync.Mutex
	var otherAuth []string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		otherAuth = append(otherAuth, r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
	}))
	defer other.Close()
	var sameSeen []string
	var site *httptest.Server
	site = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sameSeen = append(sameSeen, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/rest/api/3/issue/RIG-1":
			http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
		case "/rest/api/3/issue/RIG-2":
			http.Redirect(w, r, site.URL+"/rest/api/3/elsewhere", http.StatusTemporaryRedirect)
		}
	}))
	defer site.Close()
	a, err := New(Config{Site: site.URL, User: "u@x.io", Token: token, API: "v3"})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"RIG-1", "RIG-2"} {
		_, err := a.Issue(ctx, key)
		if err == nil || !strings.Contains(err.Error(), "refusing a redirect") {
			t.Fatalf("%s: err = %v", key, err)
		}
		if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "Authorization") {
			t.Errorf("%s: error holds a credential: %v", key, err)
		}
	}
	if !strings.Contains(func() string { _, e := a.Issue(ctx, "RIG-1"); return e.Error() }(), strings.TrimPrefix(other.URL, "http://")) {
		t.Error("the error does not name the host redirected to")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(otherAuth) != 0 {
		t.Errorf("the other server was reached: %v", otherAuth)
	}
	if slices.Contains(sameSeen, "/rest/api/3/elsewhere") {
		t.Error("the same-host redirect was followed")
	}
}

type recordTransport struct {
	mu    sync.Mutex
	hosts []string
	next  http.RoundTripper
}

func (r *recordTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.hosts = append(r.hosts, req.URL.Host)
	r.mu.Unlock()
	return r.next.RoundTrip(req)
}

// AC4: http to a host that is not loopback is refused before any request, and
// every request of a full run goes to the site's host.
func TestTokenGoesToSiteOnly(t *testing.T) {
	rt := &recordTransport{next: http.DefaultTransport}
	for _, site := range []string{"http://jira.example.com", "ftp://jira.example.com", "https://u:p@jira.example.com", "jira.example.com", ""} {
		if _, err := New(Config{Site: site, Token: token, HTTPClient: &http.Client{Transport: rt}}); err == nil {
			t.Errorf("New(%q) succeeded", site)
		}
	}
	if len(rt.hosts) != 0 {
		t.Fatalf("requests before refusal: %v", rt.hosts)
	}
	for _, version := range []int{3, 2} {
		s := newStub(t, version)
		rt := &recordTransport{next: s.srv.Client().Transport}
		a := adapter(t, s, func(c *Config) {
			c.API = "auto"
			c.HTTPClient = &http.Client{Transport: rt}
		})
		if _, err := a.Capabilities(ctx); err != nil {
			t.Fatal(err)
		}
		a.Issue(ctx, "RIG-2")
		a.Children(ctx, "RIG-1")
		a.Comment(ctx, "RIG-2", tracker.Comment{Body: "hi"})
		a.SetStatus(ctx, "RIG-2", "Done")
		host := strings.TrimPrefix(s.srv.URL, "https://")
		for _, h := range rt.hosts {
			if h != host {
				t.Errorf("v%d: request to %q, site is %q", version, h, host)
			}
		}
		if len(rt.hosts) < 5 {
			t.Errorf("v%d: only %d requests", version, len(rt.hosts))
		}
	}
	if _, err := New(Config{Site: "http://127.0.0.1:1", Token: token}); err != nil {
		t.Errorf("loopback http refused: %v", err)
	}
	if _, err := New(Config{Site: "https://acme.atlassian.net"}); err == nil {
		t.Error("no token accepted")
	}
}

func TestAuthHeaders(t *testing.T) {
	s3 := newStub(t, 3)
	if _, err := adapter(t, s3, nil).Capabilities(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s3.auths[0], "Basic ") {
		t.Errorf("v3 auth %q", s3.auths[0])
	}
	s2 := newStub(t, 2)
	if _, err := adapter(t, s2, nil).Capabilities(ctx); err != nil {
		t.Fatal(err)
	}
	if s2.auths[0] != "Bearer "+token {
		t.Errorf("v2 auth %q", s2.auths[0])
	}
}

// Capabilities reaches Jira and reports a rejected login as ErrAuth, which
// carries no credential.
func TestAuthFailure(t *testing.T) {
	for _, version := range []int{3, 2} {
		s := newStub(t, version)
		a := adapter(t, s, func(c *Config) { c.Token = "WRONG-TOKEN-77" })
		_, err := a.Capabilities(ctx)
		if !errors.Is(err, tracker.ErrAuth) {
			t.Fatalf("v%d: %v", version, err)
		}
		if _, err := a.Issue(ctx, "RIG-1"); !errors.Is(err, tracker.ErrAuth) || strings.Contains(err.Error(), "WRONG-TOKEN-77") {
			t.Errorf("v%d: Issue: %v", version, err)
		}
		if s.count("/rest/api/"+string(rune('0'+version))+"/myself") != 1 {
			t.Errorf("v%d: Capabilities did not reach myself: %v", version, s.paths())
		}
	}
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer forbidden.Close()
	a, _ := New(Config{Site: forbidden.URL, Token: token, API: "v2"})
	if _, err := a.Capabilities(ctx); !errors.Is(err, tracker.ErrAuth) {
		t.Errorf("403: %v", err)
	}
}

// AC6: no transition to the status -> ErrNoTransition naming the ones there
// are, and nothing is posted.
func TestSetStatusNoTransition(t *testing.T) {
	for _, version := range []int{3, 2} {
		s := newStub(t, version)
		a := adapter(t, s, nil)
		_, err := a.SetStatus(ctx, "RIG-2", "Review")
		if !errors.Is(err, tracker.ErrNoTransition) {
			t.Fatalf("v%d: %v", version, err)
		}
		for _, want := range []string{"In Progress", "Done", "Review"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("v%d: error %q does not name %q", version, err, want)
			}
		}
		if s.posted != 0 {
			t.Errorf("v%d: posted %d times", version, s.posted)
		}
		// The current status is not a transition and makes no call.
		before := len(s.reqs)
		res, err := a.SetStatus(ctx, "RIG-2", "to do")
		if err != nil || res.Changed || res.From != "To Do" || res.To != "To Do" {
			t.Errorf("v%d: same status %+v, %v", version, res, err)
		}
		if got := s.reqs[before:]; len(got) != 1 {
			t.Errorf("v%d: same status made calls %v", version, got)
		}
	}
}

// AC9: Epic Link is discovered once per instance; epicField skips discovery.
func TestEpicLinkDiscovery(t *testing.T) {
	s := newStub(t, 2)
	a := adapter(t, s, nil)
	for i := 0; i < 3; i++ {
		is, err := a.Issue(ctx, "RIG-2")
		if err != nil || is.Parent == nil || is.Parent.Key != "RIG-1" {
			t.Fatalf("Issue: %+v, %v", is, err)
		}
	}
	kids, err := a.Children(ctx, "RIG-1")
	if err != nil || len(kids) != 1 || kids[0].Key != "RIG-2" || kids[0].Type != "Story" {
		t.Fatalf("Children(epic) = %+v, %v", kids, err)
	}
	if n := s.count("/rest/api/2/field"); n != 1 {
		t.Errorf("field endpoint hit %d times", n)
	}
	if !strings.Contains(strings.Join(s.reqs, "\n"), "cf%5B10014%5D+%3D+RIG-1+OR+parent+%3D+RIG-1") {
		t.Errorf("epic children JQL: %v", s.reqs)
	}

	s = newStub(t, 2)
	a = adapter(t, s, func(c *Config) { c.EpicField = "customfield_10014" })
	if is, err := a.Issue(ctx, "RIG-2"); err != nil || is.Parent == nil || is.Parent.Key != "RIG-1" {
		t.Fatalf("Issue: %+v, %v", is, err)
	}
	if _, err := a.Children(ctx, "RIG-1"); err != nil {
		t.Fatal(err)
	}
	if n := s.count("/rest/api/2/field"); n != 0 {
		t.Errorf("epicField set, but the field endpoint was hit %d times", n)
	}
}

func TestChildrenV3UsesParent(t *testing.T) {
	s := newStub(t, 3)
	a := adapter(t, s, nil)
	kids, err := a.Children(ctx, "RIG-1")
	if err != nil || len(kids) != 1 || kids[0].Key != "RIG-2" {
		t.Fatalf("Children(epic) = %+v, %v", kids, err)
	}
	if s.count("/rest/api/2/field") != 0 {
		t.Error("v3 looked up the Epic Link field")
	}
}

func TestSearchPagingAndLimit(t *testing.T) {
	for _, version := range []int{3, 2} {
		s := newStub(t, version)
		s.pageCap = 2
		a := adapter(t, s, nil)
		all, err := a.Search(ctx, tracker.Query{})
		if err != nil || len(all) != 5 {
			t.Fatalf("v%d: Search = %d issues, %v", version, len(all), err)
		}
		if n := len(s.reqs); n != 3 {
			t.Errorf("v%d: %d search requests for 5 issues at 2 a page", version, n)
		}
		two, err := a.Search(ctx, tracker.Query{Limit: 3})
		if err != nil || len(two) != 3 || two[0].Key != "RIG-1" || two[0].Title != "Faster login" || two[0].Type != "Epic" {
			t.Errorf("v%d: Search(limit 3) = %+v, %v", version, two, err)
		}
	}
}

func TestBuildJQL(t *testing.T) {
	a := &Adapter{cfg: Config{Project: "RIG"}}
	for _, c := range []struct {
		q    tracker.Query
		want string
	}{
		{tracker.Query{}, `project = RIG`},
		{tracker.Query{Labels: []string{"a", "b c"}}, `project = RIG AND labels = "a" AND labels = "b c"`},
		{tracker.Query{Labels: []string{`q"x\y`}}, `project = RIG AND labels = "q\"x\\y"`},
		{tracker.Query{Text: "status = Done"}, `status = Done`},
		{tracker.Query{Text: "status = Done OR x = 1", Labels: []string{"l"}}, `(status = Done OR x = 1) AND labels = "l"`},
		{tracker.Query{Text: "status = Done ORDER BY created DESC", Labels: []string{"l"}}, `(status = Done) AND labels = "l" ORDER BY created DESC`},
		{tracker.Query{Text: "order by key"}, `project = RIG order by key`},
		{tracker.Query{Labels: []string{"a\nb"}}, `project = RIG AND labels = "a\nb"`},
	} {
		got, err := a.buildJQL(c.q)
		if err != nil || got != c.want {
			t.Errorf("buildJQL(%+v) = %q, %v; want %q", c.q, got, err, c.want)
		}
	}
	for _, q := range []tracker.Query{{Labels: []string{"a\x00b"}}, {Labels: []string{" "}}} {
		if _, err := a.buildJQL(q); err == nil {
			t.Errorf("buildJQL(%+v) succeeded", q)
		}
	}
	if _, err := (&Adapter{}).buildJQL(tracker.Query{}); err == nil {
		t.Error("an unbounded query with no project was built")
	}
}

func TestKeyShape(t *testing.T) {
	s := newStub(t, 3)
	a := adapter(t, s, nil)
	for _, k := range []string{"rig-1", "RIG-", "RIG-1x", "RIG1", "R-1 OR 1=1", "RIG-1/comment", "RIG-1\n", "-1", "RIG-1,RIG-2"} {
		if _, err := a.Issue(ctx, k); err == nil {
			t.Errorf("Issue(%q) succeeded", k)
		}
		if _, err := a.Children(ctx, k); err == nil {
			t.Errorf("Children(%q) succeeded", k)
		}
	}
	if len(s.reqs) != 0 {
		t.Errorf("a bad key reached the network: %v", s.reqs)
	}
}

func TestCommentBodies(t *testing.T) {
	const md = "**done** with `x`\n\n- a\n- b"
	s3 := newStub(t, 3)
	res, err := adapter(t, s3, nil).Comment(ctx, "RIG-2", tracker.Comment{Body: md})
	if err != nil || res.ID != "10001" || res.URL != s3.srv.URL+"/browse/RIG-2?focusedCommentId=10001" {
		t.Fatalf("Comment = %+v, %v", res, err)
	}
	doc, ok := s3.comments[0]["body"].(map[string]any)
	if !ok || doc["type"] != "doc" || doc["version"] != float64(1) {
		t.Errorf("v3 body is not an ADF document: %#v", s3.comments[0]["body"])
	}
	s2 := newStub(t, 2)
	if _, err := adapter(t, s2, nil).Comment(ctx, "RIG-2", tracker.Comment{Body: md}); err != nil {
		t.Fatal(err)
	}
	if got := s2.comments[0]["body"]; got != "*done* with {{x}}\n\n* a\n* b" {
		t.Errorf("v2 body = %#v", got)
	}
	if _, err := adapter(t, s2, nil).Comment(ctx, "RIG-2", tracker.Comment{Body: "  "}); err == nil {
		t.Error("an empty comment was posted")
	}
}

func TestResponseLimits(t *testing.T) {
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"key":"RIG-1","fields":{"summary":"`))
		w.Write([]byte(strings.Repeat("a", maxBody+10)))
		w.Write([]byte(`"}}`))
	}))
	defer big.Close()
	a, _ := New(Config{Site: big.URL, Token: token, API: "v2", EpicField: "customfield_1"})
	if _, err := a.Issue(ctx, "RIG-1"); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("oversized response: %v", err)
	}
	trailing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"key":"RIG-1","fields":{}} {"x":1}`))
	}))
	defer trailing.Close()
	a, _ = New(Config{Site: trailing.URL, Token: token, API: "v2", EpicField: "customfield_1"})
	if _, err := a.Issue(ctx, "RIG-1"); err == nil {
		t.Error("data after the response accepted")
	}
	wrong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"key":"RIG-7","fields":{}}`))
	}))
	defer wrong.Close()
	a, _ = New(Config{Site: wrong.URL, Token: token, API: "v2", EpicField: "customfield_1"})
	if _, err := a.Issue(ctx, "RIG-1"); err == nil {
		t.Error("an answer for another key accepted")
	}
}

func TestNewValidates(t *testing.T) {
	for _, c := range []Config{
		{Site: "https://a.io", Token: "t", API: "v4"},
		{Site: "https://a.io", Token: "t", Project: "rig"},
		{Site: "https://a.io", Token: "t", EpicField: "a b"},
		{Site: "https://a.io", Token: "t", AcceptanceField: "x/y"},
	} {
		if _, err := New(c); err == nil {
			t.Errorf("New(%+v) succeeded", c)
		}
	}
}

// F2: an issue the site answers under another key is a *MovedError.
func TestIssueMovedKey(t *testing.T) {
	s := newStub(t, 3)
	front := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		s.serve(rec, r)
		body := rec.Body.String()
		if strings.Contains(r.URL.Path, "/issue/RIG-2") && r.Method == http.MethodGet {
			body = strings.Replace(body, `"key":"RIG-2"`, `"key":"RIG-9"`, 1)
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(front.Close)
	cfg := s.config()
	cfg.Site, cfg.HTTPClient = front.URL, front.Client()
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Issue(ctx, "RIG-2")
	var m *MovedError
	if !errors.As(err, &m) || m.Asked != "RIG-2" || m.Got != "RIG-9" {
		t.Fatalf("err = %v, want *MovedError RIG-2 -> RIG-9", err)
	}
}

// Package jira is the tracker.Tracker adapter for Jira. One adapter speaks both
// dialects: Jira Cloud REST v3 (Basic auth with an email and API token) and
// Jira Server/Data Center REST v2 (Bearer auth with a personal access token).
//
// The token goes only to the configured site, over https (plain http only for
// a loopback address). Every redirect is refused, so the Authorization header
// can never follow one to another host. Errors name the host and the path,
// never a credential.
package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	authjira "github.com/AindriuB/rig/internal/auth/jira"
	"github.com/AindriuB/rig/internal/tracker"
)

const (
	// DefaultLimit is the Search result count when Query.Limit is 0.
	DefaultLimit = 50
	// maxBody caps one response body.
	maxBody = 8 << 20
	// pageSize is the most issues asked for in one search request.
	pageSize = 100
	// defaultTimeout bounds a request when the caller gave no client.
	defaultTimeout = 30 * time.Second
)

// Config configures an Adapter.
type Config struct {
	// Site is the Jira base URL, such as https://acme.atlassian.net.
	Site string
	// User is the account email for Cloud (Basic auth). It is empty for a
	// personal access token, which is sent as a Bearer credential.
	User string
	// Token is the API token or personal access token.
	Token string
	// API is "auto", "v2" or "v3"; empty means "auto".
	API string
	// Project is the project key a search with no query text is limited to.
	Project string
	// EpicField is the Epic Link field id on Server/DC. Empty means it is
	// found once by its name.
	EpicField string
	// AcceptanceField is the id of the field holding acceptance criteria.
	// Empty means issues carry none.
	AcceptanceField string
	// HTTPClient sends the requests; nil means a client with a timeout. Its
	// redirect policy is replaced.
	HTTPClient *http.Client
}

type dialect int

const (
	v2 dialect = iota + 2
	v3
)

func (d dialect) path() string { return "/rest/api/" + strconv.Itoa(int(d)) }

var (
	keyRE     = regexp.MustCompile(`^[A-Z][A-Z0-9_]*-[0-9]+$`)
	projectRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	fieldRE   = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	orderRE   = regexp.MustCompile(`(?i)\border\s+by\b`)
)

// Adapter is a Jira tracker.
type Adapter struct {
	cfg    Config
	base   *url.URL
	client *http.Client

	mu       sync.Mutex // guards the resolved dialect and the epic field
	dialect  dialect    // 0 until known
	epicDone bool
	epicID   string
}

var _ tracker.Tracker = (*Adapter)(nil)

// New returns an Adapter. It refuses a site the token may not go to (plain
// http to a host that is not loopback) before any request is made.
func New(cfg Config) (*Adapter, error) {
	base, err := authjira.Site(cfg.Site)
	if err != nil {
		return nil, fmt.Errorf("jira: %w", err)
	}
	if cfg.Token == "" {
		return nil, errors.New("jira: no token")
	}
	switch cfg.API {
	case "", "auto", "v2", "v3":
	default:
		return nil, fmt.Errorf("jira: api must be auto, v2 or v3, not %q", cfg.API)
	}
	if cfg.Project != "" && !projectRE.MatchString(cfg.Project) {
		return nil, fmt.Errorf("jira: %q is not a project key", cfg.Project)
	}
	for _, f := range []string{cfg.EpicField, cfg.AcceptanceField} {
		if f != "" && !fieldRE.MatchString(f) {
			return nil, fmt.Errorf("jira: %q is not a field id", f)
		}
	}
	var c http.Client
	if cfg.HTTPClient != nil {
		c = *cfg.HTTPClient
	} else {
		c.Timeout = defaultTimeout
	}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		from := ""
		if len(via) > 0 {
			from = via[len(via)-1].URL.Host
		}
		return fmt.Errorf("jira: refusing a redirect from %s to %s; the token is sent to the configured site only", from, req.URL.Host)
	}
	return &Adapter{cfg: cfg, base: base, client: &c}, nil
}

// ---------------------------------------------------------------------------
// HTTP

// do sends one request. path is already escaped and starts with "/". A 2xx
// answer is decoded into out when out is not nil.
func (a *Adapter) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	endpoint := a.base.Scheme + "://" + a.base.Host + strings.TrimRight(a.base.EscapedPath(), "/") + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("jira: %s %s: %w", method, path, err)
	}
	// The token goes to the site's host and no other.
	if !strings.EqualFold(u.Host, a.base.Host) || u.Scheme != a.base.Scheme {
		return fmt.Errorf("jira: refusing to send the token to %s", u.Host)
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("jira: %s %s: %w", method, path, err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return fmt.Errorf("jira: %s %s: %w", method, path, err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.cfg.User != "" {
		req.SetBasicAuth(a.cfg.User, a.cfg.Token)
	} else {
		req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		// A url.Error holds the URL and the cause, never a header.
		return fmt.Errorf("jira: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return fmt.Errorf("jira: %s %s: read response: %w", method, path, err)
	}
	if len(data) > maxBody {
		return fmt.Errorf("jira: %s %s: response is larger than %d bytes", method, path, maxBody)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: HTTP %d from %s", tracker.ErrAuth, resp.StatusCode, u.Host)
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w: %s %s", tracker.ErrNotFound, method, path)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return fmt.Errorf("jira: %s %s: HTTP %d", method, path, resp.StatusCode)
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("jira: %s %s: decode response: %w", method, path, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("jira: %s %s: unexpected data after the response", method, path)
	}
	return nil
}

// resolve returns the dialect, reading /rest/api/2/serverInfo once for "auto".
func (a *Adapter) resolve(ctx context.Context) (dialect, error) {
	switch a.cfg.API {
	case "v2":
		return v2, nil
	case "v3":
		return v3, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.dialect != 0 {
		return a.dialect, nil
	}
	var info struct {
		DeploymentType string `json:"deploymentType"`
	}
	if err := a.do(ctx, http.MethodGet, "/rest/api/2/serverInfo", nil, nil, &info); err != nil {
		return 0, err
	}
	a.dialect = v2
	if strings.EqualFold(info.DeploymentType, "Cloud") {
		a.dialect = v3
	}
	return a.dialect, nil
}

// epicFieldID returns the Server/DC Epic Link field id: the configured one, or
// the one found once by its name. It is empty when the instance has none.
func (a *Adapter) epicFieldID(ctx context.Context) (string, error) {
	if a.cfg.EpicField != "" {
		return a.cfg.EpicField, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.epicDone {
		return a.epicID, nil
	}
	var fields []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := a.do(ctx, http.MethodGet, "/rest/api/2/field", nil, nil, &fields); err != nil {
		return "", err
	}
	for _, f := range fields {
		if f.Name == "Epic Link" && fieldRE.MatchString(f.ID) {
			a.epicID = f.ID
			break
		}
	}
	a.epicDone = true
	return a.epicID, nil
}

// ---------------------------------------------------------------------------
// Keys and JQL

func checkKey(key string) error {
	if err := tracker.ValidateKey(key); err != nil {
		return err
	}
	if !keyRE.MatchString(key) {
		return fmt.Errorf("issue key %q is not a Jira key such as PROJ-123", key)
	}
	return nil
}

func (a *Adapter) browse(key string) string {
	return a.base.Scheme + "://" + a.base.Host + strings.TrimRight(a.base.EscapedPath(), "/") + "/browse/" + url.PathEscape(key)
}

// jqlQuote returns s as a JQL string literal.
func jqlQuote(s string) (string, error) {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			return "", fmt.Errorf("jira: query text has a control character (%U)", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}

func (a *Adapter) buildJQL(q tracker.Query) (string, error) {
	text, order := strings.TrimSpace(q.Text), ""
	if loc := orderRE.FindStringIndex(text); loc != nil {
		text, order = strings.TrimSpace(text[:loc[0]]), " "+strings.TrimSpace(text[loc[0]:])
	}
	var clauses []string
	switch {
	case text != "" && len(q.Labels) > 0:
		clauses = append(clauses, "("+text+")")
	case text != "":
		clauses = append(clauses, text)
	case a.cfg.Project != "":
		// Jira Cloud refuses an unbounded query.
		clauses = append(clauses, "project = "+a.cfg.Project)
	}
	for _, l := range q.Labels {
		if strings.TrimSpace(l) == "" {
			return "", errors.New("jira: empty label")
		}
		lit, err := jqlQuote(l)
		if err != nil {
			return "", err
		}
		clauses = append(clauses, "labels = "+lit)
	}
	if len(clauses) == 0 {
		return "", errors.New("jira: a search needs query text, a label or a configured project")
	}
	return strings.Join(clauses, " AND ") + order, nil
}

// ---------------------------------------------------------------------------
// Wire types

type nameObj struct {
	Name string `json:"name"`
}

type userObj struct {
	DisplayName string `json:"displayName"`
	Name        string `json:"name"`
}

type issueResp struct {
	Key    string                     `json:"key"`
	Fields map[string]json.RawMessage `json:"fields"`
}

// field decodes fields[name] into dst; a missing or null field is left as is.
func (r issueResp) field(name string, dst any) error {
	raw, ok := r.Fields[name]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("jira: %s of %s: %w", name, r.Key, err)
	}
	return nil
}

type parentField struct {
	Key    string `json:"key"`
	Fields struct {
		Summary   string  `json:"summary"`
		IssueType nameObj `json:"issuetype"`
	} `json:"fields"`
}

func (a *Adapter) ref(r issueResp) (tracker.IssueRef, error) {
	ref := tracker.IssueRef{Key: r.Key, URL: a.browse(r.Key)}
	var typ nameObj
	if err := r.field("summary", &ref.Title); err != nil {
		return ref, err
	}
	if err := r.field("issuetype", &typ); err != nil {
		return ref, err
	}
	ref.Type = typ.Name
	return ref, nil
}

// ---------------------------------------------------------------------------
// Tracker

// Capabilities checks the login with GET myself and reports what Jira supports.
func (a *Adapter) Capabilities(ctx context.Context) (tracker.Caps, error) {
	d, err := a.resolve(ctx)
	if err != nil {
		return tracker.Caps{}, err
	}
	if err := a.do(ctx, http.MethodGet, d.path()+"/myself", nil, nil, nil); err != nil {
		return tracker.Caps{}, err
	}
	return tracker.Caps{
		Parent: true, Children: true, Types: true, Search: true, Comment: true, SetStatus: true,
		Levels: []string{"epic", "story", "sub-task"},
	}, nil
}

// Issue returns one issue.
func (a *Adapter) Issue(ctx context.Context, key string) (tracker.Issue, error) {
	if err := checkKey(key); err != nil {
		return tracker.Issue{}, err
	}
	d, err := a.resolve(ctx)
	if err != nil {
		return tracker.Issue{}, err
	}
	epicID := ""
	if d == v2 {
		if epicID, err = a.epicFieldID(ctx); err != nil {
			return tracker.Issue{}, err
		}
	}
	fields := []string{"summary", "description", "priority", "assignee", "labels", "issuetype", "status", "parent", "updated"}
	for _, f := range []string{epicID, a.cfg.AcceptanceField} {
		if f != "" {
			fields = append(fields, f)
		}
	}
	var r issueResp
	q := url.Values{"fields": {strings.Join(fields, ",")}}
	if err := a.do(ctx, http.MethodGet, d.path()+"/issue/"+url.PathEscape(key), q, nil, &r); err != nil {
		return tracker.Issue{}, err
	}
	if r.Key != key {
		return tracker.Issue{}, fmt.Errorf("jira: asked for %s, got %q", key, r.Key)
	}
	return a.toIssue(r, d, epicID)
}

func (a *Adapter) toIssue(r issueResp, d dialect, epicID string) (tracker.Issue, error) {
	is := tracker.Issue{Key: r.Key, URL: a.browse(r.Key), Labels: []string{}, Acceptance: []string{}}
	var (
		prio, typ, status nameObj
		assignee          userObj
		parent            parentField
	)
	for _, f := range []struct {
		name string
		dst  any
	}{
		{"summary", &is.Title}, {"priority", &prio}, {"assignee", &assignee}, {"labels", &is.Labels},
		{"issuetype", &typ}, {"status", &status}, {"parent", &parent}, {"updated", &is.Rev},
	} {
		if err := r.field(f.name, f.dst); err != nil {
			return tracker.Issue{}, err
		}
	}
	if is.Labels == nil {
		is.Labels = []string{}
	}
	is.Priority, is.Type, is.Status = prio.Name, typ.Name, status.Name
	is.Assignee = assignee.DisplayName
	if is.Assignee == "" {
		is.Assignee = assignee.Name
	}
	var err error
	if is.Description, err = a.text(r.Fields["description"], d); err != nil {
		return tracker.Issue{}, err
	}
	if id := a.cfg.AcceptanceField; id != "" {
		if is.Acceptance, err = a.acceptance(r.Fields[id], d); err != nil {
			return tracker.Issue{}, err
		}
	}
	switch {
	case parent.Key != "":
		is.Parent = &tracker.IssueRef{Key: parent.Key, URL: a.browse(parent.Key), Title: parent.Fields.Summary, Type: parent.Fields.IssueType.Name}
	case epicID != "":
		var epic string
		if err := r.field(epicID, &epic); err == nil && keyRE.MatchString(epic) {
			is.Parent = &tracker.IssueRef{Key: epic, URL: a.browse(epic)}
		}
	}
	return is, nil
}

// text converts a description or comment field to Markdown.
func (a *Adapter) text(raw json.RawMessage, d dialect) (string, error) {
	if d == v3 {
		return adfToMarkdown(raw)
	}
	var s string
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("jira: decode description: %w", err)
	}
	return wikiToMarkdown(s), nil
}

var itemMarkerRE = regexp.MustCompile(`^(?:[-*+]|\d+[.)])\s+`)

// acceptance maps the acceptance field to items, one per list item or line. A
// field of a shape other than text, a document or a list of strings gives none.
func (a *Adapter) acceptance(raw json.RawMessage, d dialect) ([]string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return []string{}, nil
	}
	var text string
	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("jira: decode acceptance: %w", err)
		}
		text = s
		if d == v2 {
			text = wikiToMarkdown(s)
		}
	case '{':
		md, err := adfToMarkdown(raw)
		if err != nil {
			return nil, err
		}
		text = md
	case '[':
		var list []string
		if err := json.Unmarshal(raw, &list); err != nil {
			return []string{}, nil
		}
		text = strings.Join(list, "\n")
	default:
		return []string{}, nil
	}
	items := []string{}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(itemMarkerRE.ReplaceAllString(strings.TrimSpace(line), ""))
		if line != "" {
			items = append(items, line)
		}
	}
	return items, nil
}

// Search returns issues matching q.
func (a *Adapter) Search(ctx context.Context, q tracker.Query) ([]tracker.IssueRef, error) {
	jql, err := a.buildJQL(q)
	if err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	return a.searchRefs(ctx, jql, limit)
}

func (a *Adapter) searchRefs(ctx context.Context, jql string, limit int) ([]tracker.IssueRef, error) {
	d, err := a.resolve(ctx)
	if err != nil {
		return nil, err
	}
	out := []tracker.IssueRef{}
	token, start := "", 0
	for len(out) < limit {
		q := url.Values{
			"jql":        {jql},
			"fields":     {"summary,issuetype"},
			"maxResults": {strconv.Itoa(min(pageSize, limit-len(out)))},
		}
		var page struct {
			Issues        []issueResp `json:"issues"`
			Total         int         `json:"total"`
			NextPageToken string      `json:"nextPageToken"`
			IsLast        bool        `json:"isLast"`
		}
		path := d.path() + "/search"
		if d == v3 {
			path += "/jql"
			if token != "" {
				q.Set("nextPageToken", token)
			}
		} else {
			q.Set("startAt", strconv.Itoa(start))
		}
		if err := a.do(ctx, http.MethodGet, path, q, nil, &page); err != nil {
			return nil, err
		}
		for _, r := range page.Issues {
			ref, err := a.ref(r)
			if err != nil {
				return nil, err
			}
			out = append(out, ref)
		}
		if len(page.Issues) == 0 {
			break
		}
		if d == v3 {
			if page.IsLast || page.NextPageToken == "" {
				break
			}
			token = page.NextPageToken
		} else {
			start += len(page.Issues)
			if start >= page.Total {
				break
			}
		}
	}
	return out[:min(len(out), limit)], nil
}

// Children returns the issues directly under key: on Cloud the issues whose
// parent it is; on Server/DC also the issues in an epic through Epic Link.
func (a *Adapter) Children(ctx context.Context, key string) ([]tracker.IssueRef, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	d, err := a.resolve(ctx)
	if err != nil {
		return nil, err
	}
	// Look the issue up first: a search for the children of a missing issue
	// is a JQL error, not a not-found.
	var r issueResp
	if err := a.do(ctx, http.MethodGet, d.path()+"/issue/"+url.PathEscape(key), url.Values{"fields": {"issuetype"}}, nil, &r); err != nil {
		return nil, err
	}
	jql := "parent = " + key
	if d == v2 {
		var typ nameObj
		if err := r.field("issuetype", &typ); err != nil {
			return nil, err
		}
		if strings.EqualFold(typ.Name, "epic") {
			id, err := a.epicFieldID(ctx)
			if err != nil {
				return nil, err
			}
			link := `"Epic Link"`
			if n, ok := strings.CutPrefix(id, "customfield_"); ok && n != "" {
				link = "cf[" + n + "]"
			}
			jql = link + " = " + key + " OR parent = " + key
		}
	}
	return a.searchRefs(ctx, jql, 1000)
}

// Comment adds a comment, converting Markdown to the dialect's markup.
func (a *Adapter) Comment(ctx context.Context, key string, c tracker.Comment) (tracker.CommentResult, error) {
	if err := checkKey(key); err != nil {
		return tracker.CommentResult{}, err
	}
	if strings.TrimSpace(c.Body) == "" {
		return tracker.CommentResult{}, errors.New("jira: comment is empty")
	}
	d, err := a.resolve(ctx)
	if err != nil {
		return tracker.CommentResult{}, err
	}
	var body any = markdownToWiki(c.Body)
	if d == v3 {
		body = markdownToADF(c.Body)
	}
	var resp struct {
		ID string `json:"id"`
	}
	path := d.path() + "/issue/" + url.PathEscape(key) + "/comment"
	if err := a.do(ctx, http.MethodPost, path, nil, map[string]any{"body": body}, &resp); err != nil {
		return tracker.CommentResult{}, err
	}
	if resp.ID == "" {
		return tracker.CommentResult{}, errors.New("jira: the comment has no id in the response")
	}
	return tracker.CommentResult{ID: resp.ID, URL: a.browse(key) + "?focusedCommentId=" + url.QueryEscape(resp.ID)}, nil
}

// SetStatus moves an issue to status by looking up the transition that leads
// there. With no such transition nothing is posted and the error names the
// statuses the issue can move to.
func (a *Adapter) SetStatus(ctx context.Context, key, status string) (tracker.StatusResult, error) {
	if err := checkKey(key); err != nil {
		return tracker.StatusResult{}, err
	}
	d, err := a.resolve(ctx)
	if err != nil {
		return tracker.StatusResult{}, err
	}
	issue := d.path() + "/issue/" + url.PathEscape(key)
	var r issueResp
	if err := a.do(ctx, http.MethodGet, issue, url.Values{"fields": {"status"}}, nil, &r); err != nil {
		return tracker.StatusResult{}, err
	}
	var cur nameObj
	if err := r.field("status", &cur); err != nil {
		return tracker.StatusResult{}, err
	}
	if strings.EqualFold(cur.Name, status) {
		return tracker.StatusResult{From: cur.Name, To: cur.Name}, nil
	}
	var ts struct {
		Transitions []struct {
			ID   string  `json:"id"`
			Name string  `json:"name"`
			To   nameObj `json:"to"`
		} `json:"transitions"`
	}
	if err := a.do(ctx, http.MethodGet, issue+"/transitions", nil, nil, &ts); err != nil {
		return tracker.StatusResult{}, err
	}
	var avail []string
	for _, t := range ts.Transitions {
		to := t.To.Name
		if to == "" {
			to = t.Name
		}
		if strings.EqualFold(to, status) {
			body := map[string]any{"transition": map[string]string{"id": t.ID}}
			if err := a.do(ctx, http.MethodPost, issue+"/transitions", nil, body, nil); err != nil {
				return tracker.StatusResult{}, err
			}
			return tracker.StatusResult{From: cur.Name, To: to, Changed: true}, nil
		}
		avail = append(avail, to)
	}
	list := "none"
	if len(avail) > 0 {
		list = strings.Join(avail, ", ")
	}
	return tracker.StatusResult{}, fmt.Errorf("%w: %s is %q and has no transition to %q; available: %s", tracker.ErrNoTransition, key, cur.Name, status, list)
}

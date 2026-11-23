// Package gitlab implements tracker.Tracker over the issues of one GitLab
// project by running `glab api`. It uses the login `glab auth` already holds;
// rig never sees a token.
//
// Calls assumed (the plan author could not run glab):
//   - glab api [--hostname H] -X GET user               the login check
//   - glab api -X GET projects/<escaped path>/issues/<iid>   one issue (REST)
//   - glab api -X GET projects/<p>/issues -f k=v ...    search; -X GET makes glab
//     send the -f fields as a query string, not a body
//   - glab api -X POST projects/<p>/issues/<iid>/notes -f body=...
//   - glab api -X PUT projects/<p>/issues/<iid> -f state_event=close|reopen
//   - glab api --input - graphql                        work-item hierarchy; the
//     body {"query":...,"variables":{...}} arrives on stdin
//
// Untrusted text (search, labels, a comment body) only ever travels as one
// `-f key=value` argv element or inside the JSON on stdin. It never enters a
// path, a hand-built query string or a GraphQL document.
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AindriuB/rig/internal/proc"
	"github.com/AindriuB/rig/internal/tracker"
)

// Options configure a Tracker.
type Options struct {
	// Project is the project path, "group/.../name". It comes from
	// configuration, never from user text.
	Project string
	// Host is the GitLab host; "" or "gitlab.com" uses glab's default.
	Host string
	// Dir is the directory glab runs in.
	Dir string
	// Timeout limits each glab call; 0 means 60 seconds.
	Timeout time.Duration
}

// Tracker is a tracker.Tracker for one GitLab project.
type Tracker struct {
	path    string // url.PathEscape of the project path
	project string
	host    string
	dir     string
	timeout time.Duration

	mu        sync.Mutex
	probed    bool
	hierarchy bool
}

var _ tracker.Tracker = (*Tracker)(nil)

const defaultLimit = 50

// New returns a Tracker. It refuses a project path that is not group/name.
func New(o Options) (*Tracker, error) {
	if strings.TrimSpace(o.Project) == "" || strings.HasPrefix(o.Project, "/") || strings.HasSuffix(o.Project, "/") ||
		strings.ContainsAny(o.Project, " \t\r\n?#%") || strings.Contains(o.Project, "..") {
		return nil, errors.New("gitlab tracker: project must be a path like group/name")
	}
	t := &Tracker{path: url.PathEscape(o.Project), project: o.Project, dir: o.Dir, timeout: o.Timeout}
	if o.Host != "" && o.Host != "gitlab.com" {
		t.host = o.Host
	}
	if t.timeout == 0 {
		t.timeout = 60 * time.Second
	}
	return t, nil
}

// validateKey applies tracker.ValidateKey and the GitLab rule: a key is an
// issue iid, decimal digits with no leading zero that fit in an int64.
func validateKey(key string) error {
	if err := tracker.ValidateKey(key); err != nil {
		return err
	}
	for _, r := range key {
		if r < '0' || r > '9' {
			return fmt.Errorf("issue key %q is not an issue number", key)
		}
	}
	if key[0] == '0' {
		return fmt.Errorf("issue key %q has a leading zero", key)
	}
	if _, err := strconv.ParseInt(key, 10, 64); err != nil {
		return fmt.Errorf("issue key %q does not fit in 64 bits", key)
	}
	return nil
}

var statusRe = regexp.MustCompile(`\b(401|403|404)\b`)
var secretRe = regexp.MustCompile(`(glpat-|gloas-|glptt-)[A-Za-z0-9_\-]+`)

// run starts glab api with args. input, when non-nil, is the stdin JSON.
func (t *Tracker) run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	full := []string{"api"}
	if t.host != "" {
		full = append(full, "--hostname", t.host)
	}
	full = append(full, args...)
	o := proc.Options{Dir: t.dir, Timeout: t.timeout, ScrubTokens: true}
	if input != nil {
		o.Stdin = bytes.NewReader(input)
	}
	res, err := proc.Run(ctx, o, "glab", full...)
	if err == nil {
		return res.Stdout, nil
	}
	var ee *proc.ExitError
	if !errors.As(err, &ee) {
		return nil, fmt.Errorf("gitlab tracker: %w", err)
	}
	text := string(res.Stderr)
	if len(res.Stdout) > 0 {
		text += " " + string(res.Stdout[:min(len(res.Stdout), 300)])
	}
	switch m := statusRe.FindString(text); m {
	case "404":
		return res.Stdout, fmt.Errorf("gitlab tracker: %w", tracker.ErrNotFound)
	case "401", "403":
		return res.Stdout, fmt.Errorf("gitlab tracker: %w (HTTP %s)", tracker.ErrAuth, m)
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(res.Stderr)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, secretRe.ReplaceAllString(l, "[redacted]"))
		}
	}
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	return res.Stdout, fmt.Errorf("gitlab tracker: glab failed: %s", strings.Join(lines, " "))
}

func (t *Tracker) get(ctx context.Context, v any, endpoint string, fields ...string) error {
	args := []string{"-X", "GET", endpoint}
	for _, f := range fields {
		args = append(args, "-f", f)
	}
	out, err := t.run(ctx, nil, args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("gitlab tracker: unreadable JSON from %s: %w", endpoint, err)
	}
	return nil
}

var errGraphQL = errors.New("gitlab tracker: GraphQL error")

// graphql runs one constant query with variables and decodes data into v. A
// response carrying errors is returned wrapping errGraphQL.
func (t *Tracker) graphql(ctx context.Context, query string, vars map[string]any, v any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	out, err := t.run(ctx, body, "--header", "Content-Type: application/json", "--input", "-", "graphql")
	var resp struct {
		Data   json.RawMessage
		Errors []struct{ Message string }
	}
	if err != nil {
		// glab exits 1 on a GraphQL errors body; auth failures stay auth failures.
		if !errors.Is(err, tracker.ErrAuth) && json.Unmarshal(out, &resp) == nil && len(resp.Errors) > 0 {
			return fmt.Errorf("%w: %s", errGraphQL, resp.Errors[0].Message)
		}
		return err
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return fmt.Errorf("gitlab tracker: unreadable GraphQL JSON: %w", err)
	}
	if len(resp.Errors) > 0 {
		return fmt.Errorf("%w: %s", errGraphQL, resp.Errors[0].Message)
	}
	if err := json.Unmarshal(resp.Data, v); err != nil {
		return fmt.Errorf("gitlab tracker: unreadable GraphQL data: %w", err)
	}
	return nil
}

type issueJSON struct {
	IID       int64    `json:"iid"`
	WebURL    string   `json:"web_url"`
	Title     string   `json:"title"`
	Desc      string   `json:"description"`
	State     string   `json:"state"`
	Labels    []string `json:"labels"`
	IssueType string   `json:"issue_type"`
	Updated   string   `json:"updated_at"`
	Assignee  *struct {
		Username string `json:"username"`
	} `json:"assignee"`
}

func (i issueJSON) ref() tracker.IssueRef {
	return tracker.IssueRef{Key: strconv.FormatInt(i.IID, 10), URL: i.WebURL, Title: i.Title, Type: i.IssueType}
}

func (t *Tracker) rest(iid string) string { return "projects/" + t.path + "/issues/" + iid }

func (t *Tracker) fetch(ctx context.Context, key string) (issueJSON, error) {
	var is issueJSON
	err := t.get(ctx, &is, t.rest(key))
	return is, err
}

// probeQuery asks which widgets the project's work item types carry. It is a
// constant document; the project path travels as a variable.
const probeQuery = `query($path: ID!) { project(fullPath: $path) { workItemTypes { nodes { name widgetDefinitions { type } } } } }`

// hierarchyQuery reads one work item's parent and children.
const hierarchyQuery = `query($path: ID!, $iid: String!) { project(fullPath: $path) { workItems(iid: $iid, first: 1) { nodes { widgets { type ... on WorkItemWidgetHierarchy { parent { iid webUrl title workItemType { name } } children(first: 100) { nodes { iid webUrl title workItemType { name } } } } } } } } }`

// Capabilities proves the login with GET user, then probes once whether the
// instance and project expose the work-item hierarchy widget.
func (t *Tracker) Capabilities(ctx context.Context) (tracker.Caps, error) {
	out, err := t.run(ctx, nil, "-X", "GET", "user")
	if err != nil {
		return tracker.Caps{}, err
	}
	var u struct {
		Username string `json:"username"`
	}
	if json.Unmarshal(out, &u) != nil || u.Username == "" {
		return tracker.Caps{}, fmt.Errorf("gitlab tracker: %w: user lookup returned no user", tracker.ErrAuth)
	}
	h, err := t.hasHierarchy(ctx)
	if err != nil {
		return tracker.Caps{}, err
	}
	return tracker.Caps{Parent: h, Children: h, Types: true, Search: true, Comment: true, SetStatus: true}, nil
}

func (t *Tracker) hasHierarchy(ctx context.Context) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.probed {
		return t.hierarchy, nil
	}
	var data struct {
		Project *struct {
			WorkItemTypes struct {
				Nodes []struct {
					WidgetDefinitions []struct{ Type string } `json:"widgetDefinitions"`
				}
			} `json:"workItemTypes"`
		}
	}
	err := t.graphql(ctx, probeQuery, map[string]any{"path": t.project}, &data)
	switch {
	case errors.Is(err, errGraphQL):
		// The instance has no work-item types: no hierarchy.
	case err != nil:
		return false, err
	case data.Project != nil:
		for _, n := range data.Project.WorkItemTypes.Nodes {
			for _, w := range n.WidgetDefinitions {
				if w.Type == "HIERARCHY" {
					t.hierarchy = true
				}
			}
		}
	}
	t.probed = true
	return t.hierarchy, nil
}

type workItemRef struct {
	IID          string `json:"iid"`
	WebURL       string `json:"webUrl"`
	Title        string `json:"title"`
	WorkItemType struct {
		Name string `json:"name"`
	} `json:"workItemType"`
}

// workItem converts a work item to an IssueRef, or reports false for one that is not
// an issue of a project (an epic lives in a group and has no key here).
func (t *Tracker) workItem(w workItemRef) (tracker.IssueRef, bool) {
	if validateKey(w.IID) != nil {
		return tracker.IssueRef{}, false
	}
	u, err := url.Parse(w.WebURL)
	if err != nil {
		return tracker.IssueRef{}, false
	}
	rest, ok := cutPrefixFold(u.Path, "/"+t.project+"/-/")
	if !ok {
		return tracker.IssueRef{}, false
	}
	n, ok := strings.CutPrefix(rest, "issues/")
	if !ok {
		n, ok = strings.CutPrefix(rest, "work_items/")
	}
	if !ok || n != w.IID {
		return tracker.IssueRef{}, false
	}
	return tracker.IssueRef{Key: w.IID, URL: w.WebURL, Title: w.Title, Type: strings.ToLower(w.WorkItemType.Name)}, true
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return s[len(prefix):], true
}

// family returns the parent and children of one issue. found is false when the
// work item does not exist.
func (t *Tracker) family(ctx context.Context, key string) (parent *tracker.IssueRef, children []tracker.IssueRef, found bool, err error) {
	var data struct {
		Project *struct {
			WorkItems struct {
				Nodes []struct {
					Widgets []struct {
						Type   string
						Parent *workItemRef `json:"parent"`
						Kids   *struct {
							Nodes []workItemRef
						} `json:"children"`
					}
				}
			} `json:"workItems"`
		}
	}
	if err = t.graphql(ctx, hierarchyQuery, map[string]any{"path": t.project, "iid": key}, &data); err != nil {
		return nil, nil, false, err
	}
	if data.Project == nil || len(data.Project.WorkItems.Nodes) == 0 {
		return nil, nil, false, nil
	}
	for _, w := range data.Project.WorkItems.Nodes[0].Widgets {
		if w.Parent != nil {
			if r, ok := t.workItem(*w.Parent); ok {
				parent = &r
			}
		}
		if w.Kids != nil {
			for _, k := range w.Kids.Nodes {
				if r, ok := t.workItem(k); ok {
					children = append(children, r)
				}
			}
		}
	}
	return parent, children, true, nil
}

// Issue returns one issue.
func (t *Tracker) Issue(ctx context.Context, key string) (tracker.Issue, error) {
	if err := validateKey(key); err != nil {
		return tracker.Issue{}, err
	}
	is, err := t.fetch(ctx, key)
	if err != nil {
		return tracker.Issue{}, err
	}
	out := tracker.Issue{
		Key: key, URL: is.WebURL, Title: is.Title, Description: is.Desc,
		Labels: is.Labels, Type: is.IssueType, Status: is.State, Rev: is.Updated,
	}
	if is.Assignee != nil {
		out.Assignee = is.Assignee.Username
	}
	h, err := t.hasHierarchy(ctx)
	if err != nil {
		return tracker.Issue{}, err
	}
	if h {
		parent, _, _, err := t.family(ctx, key)
		if err != nil {
			return tracker.Issue{}, err
		}
		out.Parent = parent
	}
	return out, nil
}

// Search lists issues by text and labels. Both travel as -f fields.
func (t *Tracker) Search(ctx context.Context, q tracker.Query) ([]tracker.IssueRef, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	fields := []string{"per_page=" + strconv.Itoa(limit)}
	if q.Text != "" {
		fields = append(fields, "search="+q.Text)
	}
	if len(q.Labels) > 0 {
		fields = append(fields, "labels="+strings.Join(q.Labels, ","))
	}
	var list []issueJSON
	if err := t.get(ctx, &list, "projects/"+t.path+"/issues", fields...); err != nil {
		return nil, err
	}
	refs := make([]tracker.IssueRef, 0, len(list))
	for _, i := range list {
		refs = append(refs, i.ref())
	}
	if len(refs) > limit {
		refs = refs[:limit]
	}
	return refs, nil
}

// Children returns the issues directly under key.
func (t *Tracker) Children(ctx context.Context, key string) ([]tracker.IssueRef, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	h, err := t.hasHierarchy(ctx)
	if err != nil {
		return nil, err
	}
	if !h {
		return nil, fmt.Errorf("gitlab tracker: work-item hierarchy is not available here: %w", tracker.ErrUnsupported)
	}
	_, kids, found, err := t.family(ctx, key)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("gitlab tracker: issue %s: %w", key, tracker.ErrNotFound)
	}
	return kids, nil
}

// Comment posts a note. The body is sent as a raw -f field.
func (t *Tracker) Comment(ctx context.Context, key string, c tracker.Comment) (tracker.CommentResult, error) {
	if err := validateKey(key); err != nil {
		return tracker.CommentResult{}, err
	}
	is, err := t.fetch(ctx, key)
	if err != nil {
		return tracker.CommentResult{}, err
	}
	out, err := t.run(ctx, nil, "-X", "POST", t.rest(key)+"/notes", "-f", "body="+c.Body)
	if err != nil {
		return tracker.CommentResult{}, err
	}
	var n struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(out, &n); err != nil || n.ID == 0 {
		return tracker.CommentResult{}, errors.New("gitlab tracker: the note reply had no id")
	}
	id := strconv.FormatInt(n.ID, 10)
	return tracker.CommentResult{ID: id, URL: is.WebURL + "#note_" + id}, nil
}

// SetStatus opens or closes an issue; GitLab has no other states.
func (t *Tracker) SetStatus(ctx context.Context, key, status string) (tracker.StatusResult, error) {
	if err := validateKey(key); err != nil {
		return tracker.StatusResult{}, err
	}
	want, event := "", ""
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "opened", "open":
		want, event = "opened", "reopen"
	case "closed":
		want, event = "closed", "close"
	default:
		return tracker.StatusResult{}, fmt.Errorf("gitlab tracker: %w %q: GitLab issues are \"opened\" or \"closed\"", tracker.ErrNoTransition, status)
	}
	is, err := t.fetch(ctx, key)
	if err != nil {
		return tracker.StatusResult{}, err
	}
	if is.State == want {
		return tracker.StatusResult{From: is.State, To: want}, nil
	}
	if _, err := t.run(ctx, nil, "-X", "PUT", t.rest(key), "-f", "state_event="+event); err != nil {
		return tracker.StatusResult{}, err
	}
	return tracker.StatusResult{From: is.State, To: want, Changed: true}, nil
}

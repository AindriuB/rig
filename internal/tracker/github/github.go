// Package github implements tracker.Tracker over the issues of one GitHub
// repository by running the `gh` CLI. It uses the login `gh auth` already
// holds; rig never sees a token.
//
// Every call is `gh api` through internal/proc as an argument vector, with
// ScrubTokens on so a tracker token in rig's environment never reaches gh.
// Untrusted text (a query, a label, a comment body) is never put into an
// endpoint path or a GraphQL document: it travels as a `-f key=value` string
// field (which gh never reads from a file or coerces) or as a JSON body on
// stdin. `-F` is used only for numbers this package has validated.
//
// The plan author could not run gh against GitHub, so these gh calls are
// ASSUMED and are tested only against a fake gh:
//
//   - `gh api user` succeeds exactly when the login works.
//   - `gh api users/OWNER` returns {"type":"Organization"|"User"}; issue types
//     exist only for organisations.
//   - GraphQL (`gh api graphql`, header GraphQL-Features: sub_issues,issue_types)
//     has Repository.issue(number) with issueType, parent and
//     subIssues(first:) fields, and issue(number) does not resolve for a pull
//     request ("Could not resolve to an Issue").
//   - REST `GET search/issues` takes q and per_page and its items carry
//     number, html_url, title and an optional type.name.
//   - REST `POST repos/O/R/issues/N/comments` and `PATCH repos/O/R/issues/N`
//     take a JSON body and answer with id/html_url.
//
// Limits: labels, assignees and sub-issues are read 100 at a time, with no
// paging, and a search returns at most 100 issues.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/AindriuB/rig/internal/proc"
	"github.com/AindriuB/rig/internal/tracker"
)

// Options configure a Tracker.
type Options struct {
	// Repo is "owner/name".
	Repo string
	// Host is the GitHub host; "" means github.com.
	Host string
	// Dir is the directory gh runs in.
	Dir string
	// Timeout limits each gh call; 0 means 60 seconds.
	Timeout time.Duration
}

// Tracker is the GitHub issues adapter.
type Tracker struct {
	owner, name, host, dir string
	timeout                time.Duration
}

var _ tracker.Tracker = (*Tracker)(nil)

var repoPart = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// New checks o and returns a Tracker. It starts nothing.
func New(o Options) (*Tracker, error) {
	owner, name, ok := strings.Cut(o.Repo, "/")
	if !ok || !repoPart.MatchString(owner) || !repoPart.MatchString(name) || strings.HasSuffix(name, ".") {
		return nil, fmt.Errorf("github tracker: repository %q is not owner/name", o.Repo)
	}
	if o.Host != "" && !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:-]*$`).MatchString(o.Host) {
		return nil, fmt.Errorf("github tracker: host %q is not a host name", o.Host)
	}
	to := o.Timeout
	if to <= 0 {
		to = 60 * time.Second
	}
	return &Tracker{owner: owner, name: name, host: o.Host, dir: o.Dir, timeout: to}, nil
}

const defaultLimit = 50
const maxLimit = 100

// features asks GitHub for the sub-issue and issue-type GraphQL fields.
const features = "GraphQL-Features: sub_issues,issue_types"

const issueQuery = `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){issue(number:$number){` +
	`number url title body state updatedAt labels(first:100){nodes{name}} assignees(first:1){nodes{login}} ` +
	`issueType{name} parent{number url title issueType{name} repository{nameWithOwner}}}}}`

const childrenQuery = `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){issue(number:$number){` +
	`subIssues(first:100){nodes{number url title issueType{name} repository{nameWithOwner}}}}}}`

// api runs `gh api [--hostname H] flags... endpoint`, with body as stdin.
func (t *Tracker) api(ctx context.Context, body []byte, endpoint string, flags ...string) (proc.Result, error) {
	args := []string{"api"}
	if t.host != "" {
		args = append(args, "--hostname", t.host)
	}
	args = append(args, flags...)
	args = append(args, endpoint)
	o := proc.Options{Dir: t.dir, Timeout: t.timeout, ScrubTokens: true}
	if body != nil {
		o.Stdin = bytes.NewReader(body)
	}
	res, err := proc.Run(ctx, o, "gh", args...)
	if err != nil {
		return res, classify(err, res.Stderr)
	}
	return res, nil
}

// classify turns a gh failure into an error carrying the last lines gh
// printed, wrapping a port sentinel when the failure is one.
func classify(err error, stderr []byte) error {
	var ee *proc.ExitError
	if !errors.As(err, &ee) {
		return err // gh missing, timed out or cancelled
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(stderr)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	msg := "gh failed: " + strings.Join(lines, " ")
	low := strings.ToLower(msg)
	switch {
	case containsAny(low, "http 401", "bad credentials", "auth login", "not logged in", "not logged into"):
		return fmt.Errorf("%w: %s", tracker.ErrAuth, msg)
	case containsAny(low, "could not resolve", "http 404", "not found"):
		return fmt.Errorf("%w: %s", tracker.ErrNotFound, msg)
	}
	return errors.New(msg)
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

// checkKey refuses anything but a plain decimal issue number: no sign, no
// leading zero, at least 1, at most int64.
func checkKey(key string) (int64, error) {
	if err := tracker.ValidateKey(key); err != nil {
		return 0, err
	}
	if key[0] < '1' || key[0] > '9' || strings.Trim(key, "0123456789") != "" {
		return 0, fmt.Errorf("github issue key %q is not an issue number", key)
	}
	n, err := strconv.ParseInt(key, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("github issue key %q does not fit an issue number", key)
	}
	return n, nil
}

// Capabilities proves the login with `gh api user` and reads the owner's kind:
// issue types exist only on organisation-owned repositories.
func (t *Tracker) Capabilities(ctx context.Context) (tracker.Caps, error) {
	if _, err := t.api(ctx, nil, "user"); err != nil {
		return tracker.Caps{}, err
	}
	res, err := t.api(ctx, nil, "users/"+t.owner)
	if err != nil {
		return tracker.Caps{}, err
	}
	var u struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(res.Stdout, &u); err != nil {
		return tracker.Caps{}, fmt.Errorf("gh api users/%s: unreadable JSON: %w", t.owner, err)
	}
	return tracker.Caps{
		Parent: true, Children: true, Search: true, Comment: true, SetStatus: true,
		Types:  u.Type == "Organization",
		Levels: []string{},
	}, nil
}

type nameJSON struct {
	Name string `json:"name"`
}

type refJSON struct {
	Number     int64     `json:"number"`
	URL        string    `json:"url"`
	Title      string    `json:"title"`
	IssueType  *nameJSON `json:"issueType"`
	Repository *struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
}

// inRepo reports whether r belongs to this repository. A ref with no
// repository is refused: a bare number would point at the wrong issue.
func (t *Tracker) inRepo(r refJSON) bool {
	return r.Repository != nil && strings.EqualFold(r.Repository.NameWithOwner, t.owner+"/"+t.name)
}

func (r refJSON) ref() tracker.IssueRef {
	out := tracker.IssueRef{Key: strconv.FormatInt(r.Number, 10), URL: r.URL, Title: r.Title}
	if r.IssueType != nil {
		out.Type = r.IssueType.Name
	}
	return out
}

// graphql runs a query for one issue number.
func (t *Tracker) graphql(ctx context.Context, query string, n int64) (json.RawMessage, error) {
	res, err := t.api(ctx, nil, "graphql",
		"-H", features,
		"-f", "query="+query,
		"-f", "owner="+t.owner,
		"-f", "name="+t.name,
		"-F", "number="+strconv.FormatInt(n, 10))
	if err != nil {
		return nil, err
	}
	var out struct {
		Data struct {
			Repository *struct {
				Issue json.RawMessage `json:"issue"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		return nil, fmt.Errorf("gh api graphql: unreadable JSON: %w", err)
	}
	if out.Data.Repository == nil || len(out.Data.Repository.Issue) == 0 || string(out.Data.Repository.Issue) == "null" {
		return nil, fmt.Errorf("%w: issue %d", tracker.ErrNotFound, n)
	}
	return out.Data.Repository.Issue, nil
}

// Issue returns one issue. A pull request number is not an issue.
func (t *Tracker) Issue(ctx context.Context, key string) (tracker.Issue, error) {
	n, err := checkKey(key)
	if err != nil {
		return tracker.Issue{}, err
	}
	raw, err := t.graphql(ctx, issueQuery, n)
	if err != nil {
		return tracker.Issue{}, err
	}
	var g struct {
		refJSON
		Body      string `json:"body"`
		State     string `json:"state"`
		UpdatedAt string `json:"updatedAt"`
		Labels    struct {
			Nodes []nameJSON `json:"nodes"`
		} `json:"labels"`
		Assignees struct {
			Nodes []struct {
				Login string `json:"login"`
			} `json:"nodes"`
		} `json:"assignees"`
		Parent *refJSON `json:"parent"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		return tracker.Issue{}, fmt.Errorf("gh api graphql: unreadable issue: %w", err)
	}
	is := tracker.Issue{
		Key: key, URL: g.URL, Title: g.Title, Description: g.Body,
		Acceptance: []string{}, Labels: []string{},
		Status: strings.ToLower(g.State), Rev: g.UpdatedAt,
	}
	if g.IssueType != nil {
		is.Type = g.IssueType.Name
	}
	for _, l := range g.Labels.Nodes {
		is.Labels = append(is.Labels, l.Name)
	}
	if len(g.Assignees.Nodes) > 0 {
		is.Assignee = g.Assignees.Nodes[0].Login
	}
	if g.Parent != nil && t.inRepo(*g.Parent) {
		p := g.Parent.ref()
		is.Parent = &p
	}
	return is, nil
}

// Search runs the search API for issues of this repository. Q.Text and every
// label go into one q value; none reaches a path or a GraphQL document.
func (t *Tracker) Search(ctx context.Context, q tracker.Query) ([]tracker.IssueRef, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	limit = min(limit, maxLimit)
	terms := []string{"repo:" + t.owner + "/" + t.name, "is:issue"}
	for _, l := range q.Labels {
		l = strings.NewReplacer("\r", " ", "\n", " ", `\`, `\\`, `"`, `\"`).Replace(l)
		terms = append(terms, `label:"`+l+`"`)
	}
	if s := strings.TrimSpace(q.Text); s != "" {
		terms = append(terms, s)
	}
	res, err := t.api(ctx, nil, "search/issues",
		"--method", "GET",
		"-f", "q="+strings.Join(terms, " "),
		"-F", "per_page="+strconv.Itoa(limit))
	if err != nil {
		return nil, err
	}
	var out struct {
		Items []struct {
			Number  int64     `json:"number"`
			HTMLURL string    `json:"html_url"`
			RepoURL string    `json:"repository_url"`
			Title   string    `json:"title"`
			Type    *nameJSON `json:"type"`
			PR      *struct{} `json:"pull_request"`
		} `json:"items"`
	}
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		return nil, fmt.Errorf("gh api search/issues: unreadable JSON: %w", err)
	}
	// A repo: qualifier in Q.Text is ORed with ours, so items of other
	// repositories can come back; their numbers mean nothing here.
	own := "/repos/" + t.owner + "/" + t.name
	refs := []tracker.IssueRef{}
	for _, it := range out.Items {
		if it.PR != nil || len(refs) >= limit {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(strings.TrimRight(it.RepoURL, "/")), strings.ToLower(own)) {
			continue
		}
		r := tracker.IssueRef{Key: strconv.FormatInt(it.Number, 10), URL: it.HTMLURL, Title: it.Title}
		if it.Type != nil {
			r.Type = it.Type.Name
		}
		refs = append(refs, r)
	}
	return refs, nil
}

// Children returns the sub-issues of key.
func (t *Tracker) Children(ctx context.Context, key string) ([]tracker.IssueRef, error) {
	n, err := checkKey(key)
	if err != nil {
		return nil, err
	}
	raw, err := t.graphql(ctx, childrenQuery, n)
	if err != nil {
		return nil, err
	}
	var g struct {
		SubIssues struct {
			Nodes []refJSON `json:"nodes"`
		} `json:"subIssues"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		return nil, fmt.Errorf("gh api graphql: unreadable sub-issues: %w", err)
	}
	refs := []tracker.IssueRef{}
	for _, c := range g.SubIssues.Nodes {
		if !t.inRepo(c) {
			continue
		}
		refs = append(refs, c.ref())
	}
	return refs, nil
}

// Comment posts c.Body as a comment; the body travels as a JSON body on stdin.
func (t *Tracker) Comment(ctx context.Context, key string, c tracker.Comment) (tracker.CommentResult, error) {
	n, err := checkKey(key)
	if err != nil {
		return tracker.CommentResult{}, err
	}
	// The comments endpoint also takes a pull request number; an issue lookup
	// refuses one, so nothing is posted on a pull request.
	if _, err := t.graphql(ctx, `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){issue(number:$number){number}}}`, n); err != nil {
		return tracker.CommentResult{}, err
	}
	body, err := json.Marshal(map[string]string{"body": c.Body})
	if err != nil {
		return tracker.CommentResult{}, err
	}
	res, err := t.api(ctx, body, "repos/"+t.owner+"/"+t.name+"/issues/"+strconv.FormatInt(n, 10)+"/comments",
		"--method", "POST", "--input", "-")
	if err != nil {
		return tracker.CommentResult{}, err
	}
	var out struct {
		ID      json.Number `json:"id"`
		HTMLURL string      `json:"html_url"`
	}
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		return tracker.CommentResult{}, fmt.Errorf("gh api comments: unreadable JSON: %w", err)
	}
	return tracker.CommentResult{ID: out.ID.String(), URL: out.HTMLURL}, nil
}

// SetStatus opens or closes an issue. Other statuses are ErrNoTransition; a
// status the issue already has makes no call.
func (t *Tracker) SetStatus(ctx context.Context, key, status string) (tracker.StatusResult, error) {
	n, err := checkKey(key)
	if err != nil {
		return tracker.StatusResult{}, err
	}
	to := strings.ToLower(strings.TrimSpace(status))
	if to != "open" && to != "closed" {
		return tracker.StatusResult{}, fmt.Errorf("%w: GitHub issues are open or closed, not %q", tracker.ErrNoTransition, status)
	}
	cur, err := t.Issue(ctx, key)
	if err != nil {
		return tracker.StatusResult{}, err
	}
	if cur.Status == to {
		return tracker.StatusResult{From: cur.Status, To: to}, nil
	}
	body, _ := json.Marshal(map[string]string{"state": to})
	if _, err := t.api(ctx, body, "repos/"+t.owner+"/"+t.name+"/issues/"+strconv.FormatInt(n, 10),
		"--method", "PATCH", "--input", "-"); err != nil {
		return tracker.StatusResult{}, err
	}
	return tracker.StatusResult{From: cur.Status, To: to, Changed: true}, nil
}

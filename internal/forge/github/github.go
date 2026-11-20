// Package github implements forge.Forge by running the `gh` CLI. It uses the
// login `gh auth` already holds; kit never sees a token.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/AindriuB/workspace/cli/internal/forge"
	"github.com/AindriuB/workspace/cli/internal/proc"
)

// Client runs gh in one repository directory.
type Client struct {
	Dir string
	// Timeout limits each call except the watch inside WaitChecks, which
	// only its context limits.
	Timeout time.Duration
}

var _ forge.Forge = (*Client)(nil)

// New returns a client for the repository at dir ("" for the current one).
func New(dir string) *Client { return &Client{Dir: dir, Timeout: 60 * time.Second} }

const prFields = "number,url,title,state,isDraft,headRefName,headRefOid,baseRefName,mergeable,statusCheckRollup"

const defaultLimit = 100

func (c *Client) gh(ctx context.Context, args ...string) (proc.Result, error) {
	return proc.Run(ctx, proc.Options{Dir: c.Dir, Timeout: c.Timeout}, "gh", args...)
}

// fail turns a gh failure into an error carrying the last lines gh printed.
func fail(err error, stderr ...[]byte) error {
	var ee *proc.ExitError
	if !errors.As(err, &ee) {
		return err // gh missing, or timed out
	}
	var lines []string
	for _, s := range stderr {
		for _, l := range strings.Split(strings.TrimSpace(string(s)), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, l)
			}
		}
	}
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	return fmt.Errorf("gh failed: %s", strings.Join(lines, " "))
}

type prJSON struct {
	Number            int          `json:"number"`
	URL               string       `json:"url"`
	Title             string       `json:"title"`
	State             string       `json:"state"`
	IsDraft           bool         `json:"isDraft"`
	HeadRefName       string       `json:"headRefName"`
	HeadRefOid        string       `json:"headRefOid"`
	BaseRefName       string       `json:"baseRefName"`
	Mergeable         string       `json:"mergeable"`
	StatusCheckRollup []rollupJSON `json:"statusCheckRollup"`
}

// rollupJSON is a check run (name, status, conclusion, detailsUrl) or a
// commit status (context, state, targetUrl).
type rollupJSON struct {
	Name       string `json:"name"`
	Context    string `json:"context"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
	DetailsURL string `json:"detailsUrl"`
	TargetURL  string `json:"targetUrl"`
}

func (r rollupJSON) check() forge.Check {
	if r.Context != "" || (r.Name == "" && r.State != "") {
		return forge.Check{Name: r.Context, URL: r.TargetURL, State: outcome(r.State)}
	}
	c := forge.Check{Name: r.Name, URL: r.DetailsURL, State: forge.CheckPending}
	if r.Status == "" || r.Status == "COMPLETED" {
		c.State = outcome(r.Conclusion)
	}
	return c
}

// outcome maps a check run's conclusion or a commit status's state.
func outcome(s string) forge.CheckState {
	switch s {
	case "SUCCESS":
		return forge.CheckPass
	case "FAILURE", "ERROR", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE":
		return forge.CheckFail
	case "CANCELLED":
		return forge.CheckCancel
	case "SKIPPED", "NEUTRAL":
		return forge.CheckSkipped
	}
	return forge.CheckPending
}

func (p prJSON) neutral() forge.PullRequest {
	pr := forge.PullRequest{
		Number: p.Number, URL: p.URL, Title: p.Title, Draft: p.IsDraft,
		Head: p.HeadRefName, HeadSHA: p.HeadRefOid, Base: p.BaseRefName,
		State: forge.StateOpen, Mergeable: forge.MergeableUnknown,
		Checks: []forge.Check{},
	}
	switch p.State {
	case "CLOSED":
		pr.State = forge.StateClosed
	case "MERGED":
		pr.State = forge.StateMerged
	}
	switch p.Mergeable {
	case "MERGEABLE":
		pr.Mergeable = forge.MergeableYes
	case "CONFLICTING":
		pr.Mergeable = forge.MergeableNo
	}
	for _, r := range p.StatusCheckRollup {
		pr.Checks = append(pr.Checks, r.check())
	}
	return pr
}

// ViewPR runs `gh pr view [ref] --json …`.
func (c *Client) ViewPR(ctx context.Context, ref string) (forge.PullRequest, error) {
	args := []string{"pr", "view"}
	if ref != "" {
		if strings.HasPrefix(ref, "-") {
			return forge.PullRequest{}, fmt.Errorf("invalid pull request reference %q", ref)
		}
		args = append(args, ref)
	}
	res, err := c.gh(ctx, append(args, "--json", prFields)...)
	if err != nil {
		if s := string(res.Stderr); strings.Contains(s, "no pull requests found") ||
			strings.Contains(s, "Could not resolve to a PullRequest") {
			return forge.PullRequest{}, forge.ErrNotFound
		}
		return forge.PullRequest{}, fail(err, res.Stderr)
	}
	var p prJSON
	if err := json.Unmarshal(res.Stdout, &p); err != nil {
		return forge.PullRequest{}, fmt.Errorf("gh pr view: unreadable JSON: %w", err)
	}
	return p.neutral(), nil
}

// ListPRs runs `gh pr list --json …`.
func (c *Client) ListPRs(ctx context.Context, opts forge.ListOptions) ([]forge.PullRequest, error) {
	state, limit := opts.State, opts.Limit
	if state == "" {
		state = forge.StateOpen
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	args := []string{"pr", "list", "--state", string(state), "--limit", strconv.Itoa(limit)}
	if opts.Head != "" {
		args = append(args, "--head", opts.Head)
	}
	if opts.Mine {
		args = append(args, "--author", "@me")
	}
	res, err := c.gh(ctx, append(args, "--json", prFields)...)
	if err != nil {
		return nil, fail(err, res.Stderr)
	}
	var ps []prJSON
	if err := json.Unmarshal(res.Stdout, &ps); err != nil {
		return nil, fmt.Errorf("gh pr list: unreadable JSON: %w", err)
	}
	out := make([]forge.PullRequest, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.neutral())
	}
	return out, nil
}

// CreatePR runs `gh pr create --head …`. With an explicit head, gh opens the
// pull request for a branch already on GitHub and pushes nothing.
func (c *Client) CreatePR(ctx context.Context, in forge.CreatePRInput) (forge.PullRequest, error) {
	if in.Head == "" {
		return forge.PullRequest{}, errors.New("a pull request needs an explicit head branch")
	}
	args := []string{"pr", "create", "--head", in.Head, "--title", in.Title, "--body", in.Body}
	if in.Base != "" {
		args = append(args, "--base", in.Base)
	}
	if in.Draft {
		args = append(args, "--draft")
	}
	res, err := c.gh(ctx, args...)
	if err != nil {
		return forge.PullRequest{}, fail(err, res.Stderr)
	}
	// gh prints the new pull request's URL as its last line.
	lines := strings.Fields(string(res.Stdout))
	if len(lines) == 0 {
		return forge.PullRequest{}, errors.New("gh pr create printed no URL")
	}
	return c.ViewPR(ctx, lines[len(lines)-1])
}

// Checks returns the pull request's checks, from the same rollup ViewPR reads.
func (c *Client) Checks(ctx context.Context, pr int) ([]forge.Check, error) {
	p, err := c.ViewPR(ctx, strconv.Itoa(pr))
	return p.Checks, err
}

// watch runs a blocking gh command that only ctx limits.
func (c *Client) watch(ctx context.Context, args ...string) (proc.Result, error) {
	res, err := proc.Run(ctx, proc.Options{Dir: c.Dir}, "gh", args...)
	if errors.Is(err, proc.ErrTimeout) {
		return res, context.DeadlineExceeded
	}
	return res, err
}

// failedJQ selects a pull request's failed and cancelled checks as
// name<TAB>link lines.
const failedJQ = `.[] | select(.bucket == "fail" or .bucket == "cancel") | [.name, .link] | @tsv`

// failedJobsJQ selects a run's failed job names.
const failedJobsJQ = `.jobs[] | select(.conclusion == "failure" or .conclusion == "cancelled" or .conclusion == "timed_out") | .name`

// WaitChecks blocks on `gh pr checks --watch --fail-fast` or `gh run watch`.
// A watch that exits non-zero is either red CI or a gh error; a second query
// tells them apart, and a gh error is returned as an error.
func (c *Client) WaitChecks(ctx context.Context, t forge.WaitTarget) (forge.WaitResult, error) {
	if t.Run != 0 {
		return c.waitRun(ctx, strconv.FormatInt(t.Run, 10))
	}
	pr := strconv.Itoa(t.PR)
	w, err := c.watch(ctx, "pr", "checks", pr, "--watch", "--fail-fast")
	if err == nil {
		return forge.WaitResult{Green: true}, nil
	}
	var ee *proc.ExitError
	if !errors.As(err, &ee) {
		return forge.WaitResult{}, err
	}
	q, qerr := c.gh(ctx, "pr", "checks", pr, "--json", "name,bucket,link", "--jq", failedJQ)
	if qerr != nil {
		return forge.WaitResult{}, fail(qerr, w.Stderr, q.Stderr)
	}
	var failed []forge.Check
	for _, line := range lines(q.Stdout) {
		name, link, _ := strings.Cut(line, "\t")
		failed = append(failed, forge.Check{Name: name, URL: link, State: forge.CheckFail})
	}
	if len(failed) == 0 {
		return forge.WaitResult{}, fail(err, w.Stderr, q.Stderr)
	}
	return forge.WaitResult{Failed: failed}, nil
}

func (c *Client) waitRun(ctx context.Context, run string) (forge.WaitResult, error) {
	w, err := c.watch(ctx, "run", "watch", run, "--exit-status")
	if err == nil {
		return forge.WaitResult{Green: true}, nil
	}
	var ee *proc.ExitError
	if !errors.As(err, &ee) {
		return forge.WaitResult{}, err
	}
	q, qerr := c.gh(ctx, "run", "view", run, "--json", "conclusion", "--jq", ".conclusion")
	if qerr != nil {
		return forge.WaitResult{}, fail(qerr, w.Stderr, q.Stderr)
	}
	res := forge.WaitResult{Conclusion: strings.TrimSpace(string(q.Stdout))}
	switch res.Conclusion {
	case "failure", "cancelled", "timed_out", "startup_failure", "action_required":
	default:
		return forge.WaitResult{}, fail(err, w.Stderr, q.Stderr)
	}
	// The job names are detail: without them the verdict still stands.
	if j, err := c.gh(ctx, "run", "view", run, "--json", "jobs", "--jq", failedJobsJQ); err == nil {
		for _, name := range lines(j.Stdout) {
			res.Failed = append(res.Failed, forge.Check{Name: name, State: forge.CheckFail})
		}
	}
	return res, nil
}

var jobLinkRE = regexp.MustCompile(`/actions/runs/[0-9]+/job/([0-9]+)`)

// FailedLogs returns failed-job log lines shaped like `gh run view
// --log-failed`: job<TAB>step<TAB>line.
//
// A pull request's watch stops at the first failed check while sibling jobs
// may still run, and `gh run view` serves no log until the whole run is
// complete. The jobs logs API serves a finished job's log at once, so the
// pull request path reads that, by the job id in each check's link. Checks
// with no Actions job link have no log here.
//
// A log gh cannot serve yet is not an error: the result is just empty.
func (c *Client) FailedLogs(ctx context.Context, t forge.WaitTarget, failed []forge.Check) ([]forge.LogLine, error) {
	if t.Run != 0 {
		res, err := c.gh(ctx, "run", "view", strconv.FormatInt(t.Run, 10), "--log-failed")
		if err != nil {
			return nil, nil
		}
		var out []forge.LogLine
		for _, line := range lines(res.Stdout) {
			job, _, _ := strings.Cut(line, "\t")
			out = append(out, forge.LogLine{Job: job, Text: line})
		}
		return out, nil
	}
	var out []forge.LogLine
	linked := false
	for _, ch := range failed {
		m := jobLinkRE.FindStringSubmatch(ch.URL)
		if m == nil {
			continue
		}
		linked = true
		path := "repos/{owner}/{repo}/actions/jobs/" + m[1] + "/logs"
		// gh refuses API output with terminal escapes unless told
		// otherwise; older gh has neither the check nor the flag.
		res, err := c.gh(ctx, "api", "--allow-escape-sequences", path)
		if err != nil {
			if res, err = c.gh(ctx, "api", path); err != nil {
				continue
			}
		}
		for _, line := range lines(res.Stdout) {
			out = append(out, forge.LogLine{Job: ch.Name, Text: ch.Name + "\t-\t" + line})
		}
	}
	if !linked {
		return nil, forge.ErrNoLogs
	}
	return out, nil
}

// lines splits output into lines, dropping the empty one after a final newline.
func lines(b []byte) []string {
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

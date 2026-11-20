// Package gitlab implements forge.Forge by running the `glab` CLI. It uses
// the login `glab auth` already holds; rig never sees a token.
//
// A merge request is the forge's pull request, and the jobs of its head
// pipeline are its checks. glab has no blocking watch that reports a verdict
// by exit code, so WaitChecks polls; the polling happens here, inside one rig
// process, not in the agent's turns.
package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/AindriuB/rig/internal/forge"
	"github.com/AindriuB/rig/internal/proc"
)

// Client runs glab in one repository directory.
type Client struct {
	Dir string
	// Timeout limits each glab call.
	Timeout time.Duration
	// Poll is the pause between looks at a running pipeline.
	Poll time.Duration
}

var _ forge.Forge = (*Client)(nil)

// New returns a client for the repository at dir ("" for the current one).
func New(dir string) *Client {
	return &Client{Dir: dir, Timeout: 60 * time.Second, Poll: 15 * time.Second}
}

const defaultLimit = 100

func (c *Client) glab(ctx context.Context, args ...string) (proc.Result, error) {
	return proc.Run(ctx, proc.Options{Dir: c.Dir, Timeout: c.Timeout}, "glab", args...)
}

// fail turns a glab failure into an error carrying the last lines it printed.
func fail(err error, stderr []byte) error {
	var ee *proc.ExitError
	if !errors.As(err, &ee) {
		return err // glab missing, or timed out
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
	return fmt.Errorf("glab failed: %s", strings.Join(lines, " "))
}

// api runs `glab api <endpoint>` and decodes the JSON into v. In an endpoint,
// glab replaces :id with the current project's id.
func (c *Client) api(ctx context.Context, endpoint string, v any) error {
	res, err := c.glab(ctx, "api", endpoint)
	if err != nil {
		return fail(err, res.Stderr)
	}
	if err := json.Unmarshal(res.Stdout, v); err != nil {
		return fmt.Errorf("glab api %s: unreadable JSON: %w", endpoint, err)
	}
	return nil
}

type mrJSON struct {
	IID            int           `json:"iid"`
	WebURL         string        `json:"web_url"`
	Title          string        `json:"title"`
	State          string        `json:"state"`
	Draft          bool          `json:"draft"`
	WorkInProgress bool          `json:"work_in_progress"`
	SourceBranch   string        `json:"source_branch"`
	TargetBranch   string        `json:"target_branch"`
	SHA            string        `json:"sha"`
	MergeCommitSHA string        `json:"merge_commit_sha"`
	HasConflicts   bool          `json:"has_conflicts"`
	MergeStatus    string        `json:"merge_status"`
	HeadPipeline   *pipelineJSON `json:"head_pipeline"`
}

type pipelineJSON struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	WebURL string `json:"web_url"`
}

type jobJSON struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	WebURL       string `json:"web_url"`
	AllowFailure bool   `json:"allow_failure"`
}

// outcome maps a pipeline's or a job's status.
func outcome(status string) forge.CheckState {
	switch status {
	case "success":
		return forge.CheckPass
	case "failed":
		return forge.CheckFail
	case "canceled", "canceling":
		return forge.CheckCancel
	case "skipped":
		return forge.CheckSkipped
	}
	// created, waiting_for_resource, preparing, pending, running,
	// scheduled, and manual: a pipeline blocked on a manual job is not done.
	return forge.CheckPending
}

func (j jobJSON) check() forge.Check {
	c := forge.Check{Name: j.Name, URL: j.WebURL, State: outcome(j.Status)}
	// A job the pipeline is allowed to lose does not count against the
	// merge request: a failed one, or an optional manual one nobody
	// started. A manual job that is not allowed to fail blocks the
	// pipeline, so it stays pending.
	if j.AllowFailure && (c.State.Failed() || j.Status == "manual") {
		c.State = forge.CheckSkipped
	}
	return c
}

func (m mrJSON) neutral() forge.PullRequest {
	pr := forge.PullRequest{
		Number: m.IID, URL: m.WebURL, Title: m.Title,
		Draft: m.Draft || m.WorkInProgress,
		Head:  m.SourceBranch, HeadSHA: m.SHA, Base: m.TargetBranch,
		State: forge.StateOpen, Mergeable: forge.MergeableUnknown,
		Checks: []forge.Check{},
	}
	switch m.State {
	case "closed":
		pr.State = forge.StateClosed
	case "merged":
		pr.State = forge.StateMerged
		pr.MergeSHA = m.MergeCommitSHA
	}
	switch {
	case m.HasConflicts:
		pr.Mergeable = forge.MergeableNo
	case m.MergeStatus == "can_be_merged":
		pr.Mergeable = forge.MergeableYes
	}
	return pr
}

// view runs `glab mr view [ref] --output json`.
func (c *Client) view(ctx context.Context, ref string) (mrJSON, error) {
	args := []string{"mr", "view"}
	if ref != "" {
		if strings.HasPrefix(ref, "-") {
			return mrJSON{}, fmt.Errorf("invalid merge request reference %q", ref)
		}
		args = append(args, ref)
	}
	res, err := c.glab(ctx, append(args, "--output", "json")...)
	if err != nil {
		s := strings.ToLower(string(res.Stderr))
		if strings.Contains(s, "no open merge request") || strings.Contains(s, "no merge request") ||
			strings.Contains(s, "404") {
			return mrJSON{}, forge.ErrNotFound
		}
		return mrJSON{}, fail(err, res.Stderr)
	}
	var m mrJSON
	if err := json.Unmarshal(res.Stdout, &m); err != nil {
		return mrJSON{}, fmt.Errorf("glab mr view: unreadable JSON: %w", err)
	}
	return m, nil
}

// jobs lists a pipeline's jobs, oldest first.
func (c *Client) jobs(ctx context.Context, pipeline int64) ([]jobJSON, error) {
	var jobs []jobJSON
	err := c.api(ctx, fmt.Sprintf("projects/:id/pipelines/%d/jobs?per_page=100", pipeline), &jobs)
	// The API lists the newest job first.
	for i, j := 0, len(jobs)-1; i < j; i, j = i+1, j-1 {
		jobs[i], jobs[j] = jobs[j], jobs[i]
	}
	return jobs, err
}

// ViewPR returns a merge request with the jobs of its head pipeline as its
// checks.
func (c *Client) ViewPR(ctx context.Context, ref string) (forge.PullRequest, error) {
	m, err := c.view(ctx, ref)
	if err != nil {
		return forge.PullRequest{}, err
	}
	pr := m.neutral()
	if m.HeadPipeline == nil {
		return pr, nil
	}
	jobs, err := c.jobs(ctx, m.HeadPipeline.ID)
	if err != nil {
		return forge.PullRequest{}, err
	}
	for _, j := range jobs {
		pr.Checks = append(pr.Checks, j.check())
	}
	pr.Checks = reconcile(pr.Checks, *m.HeadPipeline)
	return pr, nil
}

// reconcile makes the checks agree with the pipeline's own status, which is
// the authority. The job list can miss what decided it: a job past the first
// hundred, or a trigger job whose child pipeline failed (the jobs endpoint
// leaves those out). When the jobs alone would roll up better than the
// pipeline, a check named "pipeline" carries the difference. It also carries
// a successful pipeline's verdict when the jobs roll up to nothing, as they
// do for a pipeline of trigger jobs only.
func reconcile(checks []forge.Check, p pipelineJSON) []forge.Check {
	state, ci := outcome(p.Status), forge.Rollup(checks)
	if (state.Failed() && ci != forge.CIRed) ||
		(state == forge.CheckPending && ci != forge.CIRed && ci != forge.CIRunning) ||
		(state == forge.CheckPass && ci == forge.CINone) {
		checks = append(checks, forge.Check{Name: "pipeline", URL: p.WebURL, State: state})
	}
	return checks
}

// ListPRs runs `glab mr list --output json`. The list carries no pipeline, so
// each merge request is then viewed once; its pipeline is reported as a
// single check named "pipeline" rather than job by job, to keep that to one
// call each.
func (c *Client) ListPRs(ctx context.Context, opts forge.ListOptions) ([]forge.PullRequest, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	args := []string{"mr", "list", "--output", "json", "--per-page", strconv.Itoa(limit)}
	switch opts.State {
	case forge.StateClosed:
		args = append(args, "--closed")
	case forge.StateMerged:
		args = append(args, "--merged")
	}
	if opts.Head != "" {
		args = append(args, "--source-branch", opts.Head)
	}
	if opts.Mine {
		var me struct {
			Username string `json:"username"`
		}
		if err := c.api(ctx, "user", &me); err != nil {
			return nil, err
		}
		if me.Username == "" {
			return nil, errors.New("glab api user returned no username")
		}
		args = append(args, "--author", me.Username)
	}
	res, err := c.glab(ctx, args...)
	if err != nil {
		return nil, fail(err, res.Stderr)
	}
	var ms []mrJSON
	if err := json.Unmarshal(res.Stdout, &ms); err != nil {
		return nil, fmt.Errorf("glab mr list: unreadable JSON: %w", err)
	}
	out := make([]forge.PullRequest, 0, len(ms))
	for _, m := range ms {
		pr := m.neutral()
		// Only an open merge request's pipeline is worth a call.
		// A failed view is an error, not "no checks": a merge request
		// with a red pipeline must never be listed as having none.
		if pr.State == forge.StateOpen {
			full, err := c.view(ctx, strconv.Itoa(m.IID))
			if err != nil {
				return nil, fmt.Errorf("merge request !%d: %w", m.IID, err)
			}
			pr = full.neutral()
			if p := full.HeadPipeline; p != nil {
				pr.Checks = append(pr.Checks, forge.Check{Name: "pipeline", URL: p.WebURL, State: outcome(p.Status)})
			}
		}
		out = append(out, pr)
	}
	return out, nil
}

// CreatePR runs `glab mr create` for a branch already on GitLab.
// `--push=false` makes sure glab pushes nothing, and `--yes` with a title and
// description leaves it nothing to ask.
func (c *Client) CreatePR(ctx context.Context, in forge.CreatePRInput) (forge.PullRequest, error) {
	if in.Head == "" {
		return forge.PullRequest{}, errors.New("a merge request needs an explicit source branch")
	}
	args := []string{"mr", "create", "--source-branch", in.Head, "--title", in.Title,
		"--description", in.Body, "--yes", "--push=false"}
	if in.Base != "" {
		args = append(args, "--target-branch", in.Base)
	}
	if in.Draft {
		args = append(args, "--draft")
	}
	res, err := c.glab(ctx, args...)
	if err != nil {
		return forge.PullRequest{}, fail(err, res.Stderr)
	}
	return c.ViewPR(ctx, in.Head)
}

// Checks returns the jobs of the merge request's head pipeline.
func (c *Client) Checks(ctx context.Context, pr int) ([]forge.Check, error) {
	p, err := c.ViewPR(ctx, strconv.Itoa(pr))
	return p.Checks, err
}

// WaitChecks polls the merge request's head pipeline, or one pipeline by id,
// until it finishes or ctx ends. No pipeline at all is an error, as "no
// checks" is on GitHub; so is a pipeline that was skipped or is blocked on a
// manual job, since neither will ever turn green or red by waiting.
func (c *Client) WaitChecks(ctx context.Context, t forge.WaitTarget) (forge.WaitResult, error) {
	for {
		p, err := c.pipeline(ctx, t)
		if err != nil {
			if ctx.Err() != nil {
				return forge.WaitResult{}, ctx.Err()
			}
			return forge.WaitResult{}, err
		}
		switch p.Status {
		case "success":
			return forge.WaitResult{Green: true}, nil
		case "failed", "canceled":
			res := forge.WaitResult{Conclusion: p.Status}
			// The job names are detail: without them the verdict stands.
			jobs, _ := c.jobs(ctx, p.ID)
			for _, j := range jobs {
				if ch := j.check(); ch.State.Failed() {
					res.Failed = append(res.Failed, ch)
				}
			}
			if t.Run == 0 {
				res.Conclusion = ""
				if len(res.Failed) == 0 {
					res.Failed = []forge.Check{{Name: "pipeline", URL: p.WebURL, State: outcome(p.Status)}}
				}
			}
			return res, nil
		case "skipped", "manual":
			return forge.WaitResult{}, fmt.Errorf("glab failed: pipeline %d is %s and will not finish by waiting", p.ID, p.Status)
		}
		select {
		case <-ctx.Done():
			return forge.WaitResult{}, ctx.Err()
		case <-time.After(c.Poll):
		}
	}
}

// pipeline fetches the pipeline a wait is about.
func (c *Client) pipeline(ctx context.Context, t forge.WaitTarget) (pipelineJSON, error) {
	if t.Run != 0 {
		var p pipelineJSON
		err := c.api(ctx, fmt.Sprintf("projects/:id/pipelines/%d", t.Run), &p)
		return p, err
	}
	m, err := c.view(ctx, strconv.Itoa(t.PR))
	if errors.Is(err, forge.ErrNotFound) {
		return pipelineJSON{}, fmt.Errorf("glab failed: no merge request !%d", t.PR)
	}
	if err != nil {
		return pipelineJSON{}, err
	}
	if m.HeadPipeline == nil {
		return pipelineJSON{}, fmt.Errorf("glab failed: no pipeline reported on merge request !%d", t.PR)
	}
	return *m.HeadPipeline, nil
}

var jobLinkRE = regexp.MustCompile(`/-/jobs/([0-9]+)`)

// FailedLogs returns each failed job's trace as job<TAB>-<TAB>line. A check
// with no job link has no trace; when none has one, it returns ErrNoLogs. A
// trace glab cannot serve is not an error: the result is just shorter.
func (c *Client) FailedLogs(ctx context.Context, _ forge.WaitTarget, failed []forge.Check) ([]forge.LogLine, error) {
	var out []forge.LogLine
	linked := false
	for _, ch := range failed {
		m := jobLinkRE.FindStringSubmatch(ch.URL)
		if m == nil {
			continue
		}
		linked = true
		res, err := c.glab(ctx, "api", "projects/:id/jobs/"+m[1]+"/trace")
		if err != nil {
			continue
		}
		s := strings.TrimSuffix(string(res.Stdout), "\n")
		if s == "" {
			continue
		}
		for _, line := range strings.Split(s, "\n") {
			out = append(out, forge.LogLine{Job: ch.Name, Text: ch.Name + "\t-\t" + line})
		}
	}
	if !linked {
		return nil, forge.ErrNoLogs
	}
	return out, nil
}

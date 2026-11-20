// Package fake is an in-memory forge for tests. It records every call and
// returns what the test set up.
package fake

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/AindriuB/workspace/cli/internal/forge"
)

// Forge is a forge.Forge backed by a slice of pull requests.
type Forge struct {
	mu sync.Mutex

	PRs []forge.PullRequest
	// Wait and WaitErr are what WaitChecks returns. WaitBlock makes it
	// block until its context ends instead.
	Wait      forge.WaitResult
	WaitErr   error
	WaitBlock bool
	// Logs and LogsErr are what FailedLogs returns.
	Logs    []forge.LogLine
	LogsErr error
	// Err, when set, is returned by every other method.
	Err error

	// Calls lists the methods called, in order, as "Method arg".
	Calls   []string
	Created []forge.CreatePRInput
}

var _ forge.Forge = (*Forge)(nil)

func (f *Forge) record(format string, a ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, fmt.Sprintf(format, a...))
}

// ViewPR finds a pull request by number, URL or head branch.
func (f *Forge) ViewPR(_ context.Context, ref string) (forge.PullRequest, error) {
	f.record("ViewPR %s", ref)
	if f.Err != nil {
		return forge.PullRequest{}, f.Err
	}
	for _, pr := range f.PRs {
		if strconv.Itoa(pr.Number) == ref || pr.URL == ref || pr.Head == ref {
			return pr, nil
		}
	}
	return forge.PullRequest{}, forge.ErrNotFound
}

func (f *Forge) ListPRs(_ context.Context, opts forge.ListOptions) ([]forge.PullRequest, error) {
	f.record("ListPRs %+v", opts)
	if f.Err != nil {
		return nil, f.Err
	}
	state := opts.State
	if state == "" {
		state = forge.StateOpen
	}
	var out []forge.PullRequest
	for _, pr := range f.PRs {
		if pr.State != state || (opts.Head != "" && pr.Head != opts.Head) {
			continue
		}
		if opts.Limit > 0 && len(out) == opts.Limit {
			break
		}
		out = append(out, pr)
	}
	return out, nil
}

func (f *Forge) CreatePR(_ context.Context, in forge.CreatePRInput) (forge.PullRequest, error) {
	f.record("CreatePR %s", in.Head)
	if f.Err != nil {
		return forge.PullRequest{}, f.Err
	}
	if in.Head == "" {
		return forge.PullRequest{}, fmt.Errorf("fake: CreatePR needs a head branch")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(f.PRs) + 1
	pr := forge.PullRequest{
		Number: n, URL: fmt.Sprintf("https://forge.example/pr/%d", n),
		Title: in.Title, State: forge.StateOpen, Draft: in.Draft,
		Head: in.Head, Base: in.Base, Mergeable: forge.MergeableUnknown,
	}
	f.PRs = append(f.PRs, pr)
	f.Created = append(f.Created, in)
	return pr, nil
}

func (f *Forge) Checks(ctx context.Context, pr int) ([]forge.Check, error) {
	f.record("Checks %d", pr)
	if f.Err != nil {
		return nil, f.Err
	}
	for _, p := range f.PRs {
		if p.Number == pr {
			return p.Checks, nil
		}
	}
	return nil, forge.ErrNotFound
}

func (f *Forge) WaitChecks(ctx context.Context, t forge.WaitTarget) (forge.WaitResult, error) {
	f.record("WaitChecks %+v", t)
	if f.WaitBlock {
		<-ctx.Done()
		return forge.WaitResult{}, ctx.Err()
	}
	return f.Wait, f.WaitErr
}

func (f *Forge) FailedLogs(_ context.Context, t forge.WaitTarget, failed []forge.Check) ([]forge.LogLine, error) {
	f.record("FailedLogs %+v", t)
	return f.Logs, f.LogsErr
}

// Package forge is the port between kit's commands and a code-hosting
// service. Its types are neutral: nothing here names GitHub or GitLab. An
// adapter (forge/github, forge/gitlab, forge/fake) implements Forge for one
// service and translates that service's vocabulary into these types.
package forge

import (
	"context"
	"errors"
	"strings"
)

// Forge is what kit needs from a code-hosting service.
type Forge interface {
	// ViewPR returns one pull request. ref is a number, a URL or a head
	// branch; "" means the current branch's pull request.
	ViewPR(ctx context.Context, ref string) (PullRequest, error)
	ListPRs(ctx context.Context, opts ListOptions) ([]PullRequest, error)
	// CreatePR opens a pull request for a head branch that is already on
	// the forge. It never pushes.
	CreatePR(ctx context.Context, in CreatePRInput) (PullRequest, error)
	Checks(ctx context.Context, pr int) ([]Check, error)
	// WaitChecks blocks until the target's CI finishes or ctx ends.
	WaitChecks(ctx context.Context, target WaitTarget) (WaitResult, error)
	// FailedLogs returns the log lines of the failed checks, oldest first.
	// It returns ErrNoLogs when the forge holds no log for any of them.
	FailedLogs(ctx context.Context, target WaitTarget, failed []Check) ([]LogLine, error)
}

var (
	// ErrNotFound reports that no such pull request exists.
	ErrNotFound = errors.New("pull request not found")
	// ErrNoLogs reports that the failed checks have no log on the forge,
	// for example checks posted by an outside CI provider.
	ErrNoLogs = errors.New("no logs for these checks")
)

// PRState is where a pull request is in its life.
type PRState string

const (
	StateOpen   PRState = "open"
	StateClosed PRState = "closed"
	StateMerged PRState = "merged"
)

// Mergeable says whether a pull request merges cleanly into its base.
type Mergeable string

const (
	MergeableYes     Mergeable = "mergeable"
	MergeableNo      Mergeable = "conflicting"
	MergeableUnknown Mergeable = "unknown"
)

// CheckState is the outcome of one CI check.
type CheckState string

const (
	CheckPass    CheckState = "pass"
	CheckFail    CheckState = "fail"
	CheckCancel  CheckState = "cancel"
	CheckPending CheckState = "pending"
	CheckSkipped CheckState = "skipped"
)

// Failed reports whether the check counts against its pull request.
func (s CheckState) Failed() bool { return s == CheckFail || s == CheckCancel }

// Check is one CI check on a pull request.
type Check struct {
	Name  string     `json:"name"`
	State CheckState `json:"state"`
	URL   string     `json:"url,omitempty"`
}

// PullRequest is a pull request, or a GitLab merge request.
type PullRequest struct {
	Number  int     `json:"number"`
	URL     string  `json:"url"`
	Title   string  `json:"title"`
	State   PRState `json:"state"`
	Draft   bool    `json:"draft"`
	Head    string  `json:"head"`
	HeadSHA string  `json:"headSha"`
	Base    string  `json:"base"`
	// MergeSHA is the commit a merged pull request landed as, else "".
	MergeSHA  string    `json:"mergeSha,omitempty"`
	Mergeable Mergeable `json:"mergeable"`
	Checks    []Check   `json:"checks"`
}

// CI is the overall state of a set of checks.
type CI string

const (
	CIRed     CI = "red"
	CIRunning CI = "running"
	CIGreen   CI = "green"
	CINone    CI = "none"
)

// Rollup folds checks into one state: red if any failed or was cancelled,
// else running if any is unfinished, else green, or none with no checks.
// Every adapter's output goes through this one rule.
func Rollup(checks []Check) CI {
	if len(checks) == 0 {
		return CINone
	}
	running := false
	for _, c := range checks {
		if c.State.Failed() {
			return CIRed
		}
		running = running || c.State == CheckPending
	}
	if running {
		return CIRunning
	}
	return CIGreen
}

// ListOptions narrow ListPRs. The zero value lists open pull requests.
type ListOptions struct {
	State PRState // "" means open
	Head  string
	Mine  bool // only the authenticated user's
	Limit int  // 0 means the adapter's default
}

// CreatePRInput describes a pull request to open.
type CreatePRInput struct {
	Head  string // required
	Base  string // "" means the repository's default branch
	Title string
	Body  string
	Draft bool
}

// WaitTarget names what to wait on: a pull request's checks, or one CI run.
// Exactly one field is set.
type WaitTarget struct {
	PR  int
	Run int64
}

// WaitResult is how a wait ended.
type WaitResult struct {
	Green bool
	// Conclusion is the forge's own word for a failed run, such as
	// "failure" or "cancelled". It is empty for a pull request.
	Conclusion string
	Failed     []Check
}

// LogLine is one line of a failed check's log.
type LogLine struct {
	Job  string // the check or job the line belongs to
	Text string // the whole line as printed, starting with Job
}

// Kind names a forge adapter.
type Kind string

const (
	GitHub Kind = "github"
	GitLab Kind = "gitlab"
)

// KindForHost guesses the forge from a remote's host. It knows the two public
// hosts and reads a self-hosted name such as gitlab.example.com; for anything
// else it reports false and the repository must say in kit.json.
func KindForHost(host string) (Kind, bool) {
	host = strings.ToLower(host)
	switch {
	case host == "github.com" || strings.HasSuffix(host, ".github.com"):
		return GitHub, true
	case host == "gitlab.com" || strings.HasSuffix(host, ".gitlab.com"):
		return GitLab, true
	}
	for _, label := range strings.Split(host, ".") {
		switch {
		case strings.HasPrefix(label, "github") || strings.HasPrefix(label, "ghe"):
			return GitHub, true
		case strings.HasPrefix(label, "gitlab"):
			return GitLab, true
		}
	}
	return "", false
}

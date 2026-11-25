package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/AindriuB/rig/internal/proc"
)

// Actions an update run can end in.
const (
	ActionUpdated   = "updated"
	ActionUpToDate  = "up-to-date"
	ActionAvailable = "available"
	ActionRefused   = "refused"
	ActionFailed    = "failed"
)

const versionTimeout = 10 * time.Second

// Options is one update run.
type Options struct {
	Exe     string  // the running rig
	Current string  // its version, without a leading v
	To      string  // --to; empty means the latest release
	Check   bool    // report only, change nothing
	Client  *Client // the release source
	GOOS    string  // default runtime.GOOS
	GOARCH  string  // default runtime.GOARCH

	// RunVersion runs the staged binary's `version` and returns its output.
	// Nil runs it through internal/proc with a 10 s timeout.
	RunVersion func(ctx context.Context, path string) (string, error)
	verify     func(*Download) error // tests substitute the keys
}

// Result is what an update run did. Message is the one-line text form.
type Result struct {
	Current string
	Target  string
	Action  string
	Path    string
	Check   string // the failed check, else empty
	Message string
}

// ExitCode maps the outcome to rig's exit code.
func (r Result) ExitCode() int {
	switch r.Action {
	case ActionUpdated, ActionUpToDate:
		return 0
	case ActionRefused, ActionAvailable:
		return 1
	}
	return 2
}

func trimV(s string) string { return strings.TrimPrefix(s, "v") }

// Update replaces o.Exe with a signed release. Anything but ActionUpdated
// leaves the installed file untouched.
func Update(ctx context.Context, o Options) Result {
	res := Result{Current: o.Current, Path: o.Exe}
	fail := func(check, format string, a ...any) Result {
		res.Action, res.Check, res.Message = ActionFailed, check, fmt.Sprintf(format, a...)
		return res
	}
	refuse := func(format string, a ...any) Result {
		res.Action, res.Message = ActionRefused, fmt.Sprintf(format, a...)
		return res
	}
	upToDate := func() Result {
		res.Action, res.Message = ActionUpToDate, fmt.Sprintf("rig %s is up to date", o.Current)
		return res
	}
	unsigned := func() Result {
		if o.Check {
			return upToDate() // not an available update
		}
		return refuse("rig %s is before 0.5.2 and is not signed; it cannot be installed", res.Target)
	}
	tag := ""
	if o.To != "" {
		tag = "v" + trimV(o.To)
		res.Target = trimV(o.To)
		ok, err := SignedEra(tag)
		if err != nil {
			if o.Check {
				return fail("", "%s is not a valid version: %v", o.To, err)
			}
			return refuse("%s is not a valid version: %v", o.To, err)
		}
		if !ok {
			return unsigned()
		}
	}
	rel, err := o.Client.Lookup(ctx, tag)
	if err != nil {
		return fail("", "%v", err)
	}
	res.Target = trimV(rel.Tag)
	ok, err := SignedEra(res.Target)
	if err != nil {
		return fail("", "release tag %s: %v", rel.Tag, err)
	}
	if !ok {
		return unsigned()
	}
	cmp, err := CompareVersions(res.Target, o.Current)
	if err != nil {
		return fail("", "%v", err)
	}
	switch {
	case cmp == 0, cmp < 0 && o.Check:
		return upToDate()
	case cmp < 0 && o.To == "":
		return refuse("latest release %s is older than the running rig %s; use --to to install it anyway", res.Target, o.Current)
	}
	if o.Check {
		res.Action, res.Message = ActionAvailable, fmt.Sprintf("rig %s is available (running %s)", res.Target, o.Current)
		return res
	}

	target, err := filepath.EvalSymlinks(o.Exe)
	if err != nil {
		return fail("", "resolving %s: %v", o.Exe, err)
	}
	lock, err := tryLock(target)
	if err != nil {
		var nw *NotWritableError
		switch {
		case errors.Is(err, errLockBusy):
			res.Action, res.Check = ActionRefused, "lock"
			res.Message = fmt.Sprintf("another update of %s is in progress", o.Exe)
			return res
		case errors.As(err, &nw):
			return refuse("cannot replace %s: %v", o.Exe, err)
		}
		return fail("", "%v", err)
	}
	defer lock.release()

	goos, goarch := o.GOOS, o.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	d, err := o.Client.Fetch(ctx, rel, goos, goarch)
	if err != nil {
		var ce *CheckError
		if errors.As(err, &ce) {
			return fail(ce.Check, "%s check failed: %s", ce.Check, ce.Detail)
		}
		return fail("", "%v", err)
	}
	verify := o.verify
	if verify == nil {
		verify = (*Download).Verify
	}
	if err := verify(d); err != nil {
		var ce *CheckError
		if errors.As(err, &ce) {
			return fail(ce.Check, "%s check failed: %s", ce.Check, ce.Detail)
		}
		return fail("signature", "signature check failed: %v", err)
	}
	st, err := Stage(o.Exe, d.Binary)
	if err != nil {
		var nw *NotWritableError
		if errors.As(err, &nw) {
			return refuse("cannot replace %s: %v", o.Exe, err)
		}
		return fail("", "staging %s: %v", o.Exe, err)
	}
	run := o.RunVersion
	if run == nil {
		run = runStagedVersion
	}
	out, err := run(ctx, st.Path())
	if err != nil {
		_ = st.Discard()
		return fail("version", "version check failed: the staged binary did not run: %v", err)
	}
	if got := strings.TrimSpace(out); got != res.Target {
		_ = st.Discard()
		return fail("version", "version check failed: the staged binary reports %q, expected %q", got, res.Target)
	}
	if err := st.Commit(); err != nil {
		_ = st.Discard()
		return fail("", "replacing %s: %v", o.Exe, err)
	}
	res.Action = ActionUpdated
	res.Message = fmt.Sprintf("rig updated %s -> %s (%s)", o.Current, res.Target, o.Exe)
	return res
}

func runStagedVersion(ctx context.Context, path string) (string, error) {
	r, err := proc.Run(ctx, proc.Options{Timeout: versionTimeout, ScrubTokens: true}, path, "version")
	if err != nil {
		return "", err
	}
	return string(r.Stdout), nil
}

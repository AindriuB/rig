// Package exec is a tracker adapter that runs a user program once per
// operation. The program reads one JSON line on stdin and writes one JSON
// object on stdout (protocol v1), so any tracker can be added without
// changing rig.
//
// Request: {"v":1,"op":<op>, ...args}. The ops are caps (no args), issue
// {key}, search {query}, children {key}, comment {key, comment} and status
// {key, status}.
//
// Response: {"v":1,"ok":true} plus one payload (caps, issue, issues, comment
// or status, in the port's JSON shape), or {"v":1,"ok":false,"error":"...",
// "code":"..."}. The codes not-found, unsupported, auth and no-transition map
// to the port's sentinels; no code is a plain error and an unknown code is a
// plain error that names it.
package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/AindriuB/rig/internal/proc"
	"github.com/AindriuB/rig/internal/tracker"
)

const (
	// Version is the protocol version this adapter speaks.
	Version = 1
	// DefaultTimeout bounds one program run when Options.Timeout is 0.
	DefaultTimeout = 30 * time.Second
	// MaxOutput is the most stdout, in bytes, the adapter accepts.
	MaxOutput = 4 << 20
	// stderrTail is how much of the program's stderr an error carries.
	stderrTail = 2 << 10
)

// Options configure a Tracker.
type Options struct {
	// Dir is the program's working directory ("" is the current one).
	Dir string
	// Timeout bounds each run of the program; 0 means DefaultTimeout.
	Timeout time.Duration
}

// Tracker implements tracker.Tracker by running a program per operation.
type Tracker struct {
	argv    []string
	opts    Options
	mu      sync.Mutex
	caps    tracker.Caps
	haveCap bool
}

var _ tracker.Tracker = (*Tracker)(nil)

// New returns a Tracker that runs argv. It refuses an empty argv.
func New(argv []string, opts Options) (*Tracker, error) {
	if len(argv) == 0 || argv[0] == "" {
		return nil, errors.New("exec tracker: empty command")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	return &Tracker{argv: append([]string(nil), argv...), opts: opts}, nil
}

type request struct {
	V       int              `json:"v"`
	Op      string           `json:"op"`
	Key     string           `json:"key,omitempty"`
	Query   *tracker.Query   `json:"query,omitempty"`
	Comment *tracker.Comment `json:"comment,omitempty"`
	Status  *string          `json:"status,omitempty"`
}

type response struct {
	V      *int            `json:"v"`
	OK     *bool           `json:"ok"`
	Error  string          `json:"error"`
	Code   string          `json:"code"`
	Caps   json.RawMessage `json:"caps"`
	Issue  json.RawMessage `json:"issue"`
	Issues json.RawMessage `json:"issues"`
	Cmt    json.RawMessage `json:"comment"`
	Status json.RawMessage `json:"status"`
}

// call runs the program once and decodes the payload chosen by pick into out.
func (t *Tracker) call(ctx context.Context, req request, pick func(*response) json.RawMessage, out any) error {
	req.V = Version
	line, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("exec tracker %s: %w", req.Op, err)
	}
	line = append(line, '\n')
	res, err := proc.Run(ctx, proc.Options{
		Dir:         t.opts.Dir,
		ScrubTokens: true,
		Timeout:     t.opts.Timeout,
		Stdin:       bytes.NewReader(line),
	}, t.argv[0], t.argv[1:]...)
	if err != nil {
		return fmt.Errorf("exec tracker %s: %w%s", req.Op, err, tail(res.Stderr))
	}
	if len(res.Stdout) > MaxOutput {
		return fmt.Errorf("exec tracker %s: output is larger than the %d byte limit%s", req.Op, MaxOutput, tail(res.Stderr))
	}
	dec := json.NewDecoder(bytes.NewReader(res.Stdout))
	var r response
	if err := dec.Decode(&r); err != nil {
		return fmt.Errorf("exec tracker %s: stdout is not one JSON object: %v%s", req.Op, err, tail(res.Stderr))
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("exec tracker %s: stdout has more than one JSON object%s", req.Op, tail(res.Stderr))
	}
	if r.V == nil || *r.V != Version {
		return fmt.Errorf("exec tracker %s: unsupported protocol version (want %d)%s", req.Op, Version, tail(res.Stderr))
	}
	if r.OK == nil {
		return fmt.Errorf("exec tracker %s: response has no ok field%s", req.Op, tail(res.Stderr))
	}
	if !*r.OK {
		return responseError(req.Op, r)
	}
	raw := pick(&r)
	if len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return fmt.Errorf("exec tracker %s: ok response is missing its payload%s", req.Op, tail(res.Stderr))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("exec tracker %s: bad payload: %v%s", req.Op, err, tail(res.Stderr))
	}
	return nil
}

func responseError(op string, r response) error {
	msg := r.Error
	if msg == "" {
		msg = "failed"
	}
	var sentinel error
	switch r.Code {
	case "":
		return fmt.Errorf("exec tracker %s: %s", op, msg)
	case "not-found":
		sentinel = tracker.ErrNotFound
	case "unsupported":
		sentinel = tracker.ErrUnsupported
	case "auth":
		sentinel = tracker.ErrAuth
	case "no-transition":
		sentinel = tracker.ErrNoTransition
	default:
		return fmt.Errorf("exec tracker %s: %s (unknown error code %q)", op, msg, r.Code)
	}
	return fmt.Errorf("exec tracker %s: %s: %w", op, msg, sentinel)
}

// tail formats the last part of stderr for an error message.
func tail(b []byte) string {
	if len(b) > stderrTail {
		b = b[len(b)-stderrTail:]
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return ""
	}
	return "\nstderr: " + s
}

// Capabilities runs the caps op once per Tracker and caches a success.
func (t *Tracker) Capabilities(ctx context.Context) (tracker.Caps, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.haveCap {
		return t.caps, nil
	}
	var c tracker.Caps
	err := t.call(ctx, request{Op: "caps"}, func(r *response) json.RawMessage { return r.Caps }, &c)
	if err != nil {
		return tracker.Caps{}, err
	}
	t.caps, t.haveCap = c, true
	return c, nil
}

// need returns ErrUnsupported unless ok(caps) holds.
func (t *Tracker) need(ctx context.Context, op string, ok func(tracker.Caps) bool) error {
	c, err := t.Capabilities(ctx)
	if err != nil {
		return err
	}
	if !ok(c) {
		return fmt.Errorf("exec tracker %s: %w", op, tracker.ErrUnsupported)
	}
	return nil
}

// Issue returns one issue.
func (t *Tracker) Issue(ctx context.Context, key string) (tracker.Issue, error) {
	if err := tracker.ValidateKey(key); err != nil {
		return tracker.Issue{}, err
	}
	var is tracker.Issue
	err := t.call(ctx, request{Op: "issue", Key: key}, func(r *response) json.RawMessage { return r.Issue }, &is)
	return is, err
}

// Search returns issues matching q.
func (t *Tracker) Search(ctx context.Context, q tracker.Query) ([]tracker.IssueRef, error) {
	if err := t.need(ctx, "search", func(c tracker.Caps) bool { return c.Search }); err != nil {
		return nil, err
	}
	if q.Labels == nil {
		q.Labels = []string{}
	}
	return t.refs(ctx, request{Op: "search", Query: &q})
}

// Children returns the issues directly under key.
func (t *Tracker) Children(ctx context.Context, key string) ([]tracker.IssueRef, error) {
	if err := tracker.ValidateKey(key); err != nil {
		return nil, err
	}
	if err := t.need(ctx, "children", func(c tracker.Caps) bool { return c.Children }); err != nil {
		return nil, err
	}
	return t.refs(ctx, request{Op: "children", Key: key})
}

func (t *Tracker) refs(ctx context.Context, req request) ([]tracker.IssueRef, error) {
	var refs []tracker.IssueRef
	if err := t.call(ctx, req, func(r *response) json.RawMessage { return r.Issues }, &refs); err != nil {
		return nil, err
	}
	return refs, nil
}

// Comment adds a comment to an issue.
func (t *Tracker) Comment(ctx context.Context, key string, c tracker.Comment) (tracker.CommentResult, error) {
	if err := tracker.ValidateKey(key); err != nil {
		return tracker.CommentResult{}, err
	}
	if err := t.need(ctx, "comment", func(c tracker.Caps) bool { return c.Comment }); err != nil {
		return tracker.CommentResult{}, err
	}
	var res tracker.CommentResult
	err := t.call(ctx, request{Op: "comment", Key: key, Comment: &c}, func(r *response) json.RawMessage { return r.Cmt }, &res)
	return res, err
}

// SetStatus moves an issue to status.
func (t *Tracker) SetStatus(ctx context.Context, key, status string) (tracker.StatusResult, error) {
	if err := tracker.ValidateKey(key); err != nil {
		return tracker.StatusResult{}, err
	}
	if err := t.need(ctx, "status", func(c tracker.Caps) bool { return c.SetStatus }); err != nil {
		return tracker.StatusResult{}, err
	}
	var res tracker.StatusResult
	err := t.call(ctx, request{Op: "status", Key: key, Status: &status}, func(r *response) json.RawMessage { return r.Status }, &res)
	return res, err
}

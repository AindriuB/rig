// Package proc runs a subprocess from an argument vector under a deadline.
//
// Nothing here builds a shell string: the program and each argument reach the
// kernel as given. When the deadline passes or the context is cancelled, the
// child and everything it started are killed, so a caller never waits on a
// stuck grandchild. On Unix the child leads its own process group and the
// group is killed. On Windows the child is put in a Job Object and the job is
// terminated. One narrow race remains on Windows: a descendant spawned between
// Start and the job assignment is not in the job and is not killed.
package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ErrTimeout reports that the deadline passed before the program exited.
var ErrTimeout = errors.New("timed out")

// ErrNotFound reports that the program is not on PATH.
var ErrNotFound = errors.New("not found on PATH")

// maxOutput caps each captured stream. Output past the cap is dropped.
const maxOutput = 64 << 20

// waitDelay is how long Run waits for output pipes to close after the program
// exits or is killed, in case something it started still holds them open.
const waitDelay = 2 * time.Second

// Options configure one run. The zero value runs in the current directory
// with the parent's environment, no stdin and no deadline of its own.
type Options struct {
	Dir string
	Env []string // added to the parent's environment
	// ScrubTokens removes every RIG_*_TOKEN variable from the inherited
	// environment before Env is applied. Names match case-insensitively on
	// Windows and exactly elsewhere. A variable named in Env is still set.
	ScrubTokens bool
	// EnvAllow, when non-nil, limits the inherited environment to variables
	// whose names equal an entry or start with the prefix of a "PREFIX*"
	// entry (case-insensitive on Windows). Env is still appended unfiltered.
	// Nil inherits the whole parent environment.
	EnvAllow []string
	Timeout  time.Duration // 0: only ctx limits the run
	// Grace, on Unix, makes a deadline or cancel send SIGTERM to the process
	// group first and SIGKILL once Grace has passed. Zero kills at once.
	// Windows ignores it.
	Grace time.Duration
	// JobBreakaway, on Windows, lets processes in Run's job leave it (see
	// Spawn). Unix ignores it.
	JobBreakaway bool
	Stdin        io.Reader
	// Stdout and Stderr redirect Interactive's output; Run ignores them.
	Stdout io.Writer
	Stderr io.Writer
}

// Result is what a finished program left behind.
type Result struct {
	Stdout []byte
	Stderr []byte
	Code   int
}

// ExitError reports a program that ran and exited non-zero.
type ExitError struct {
	Name   string
	Code   int
	Stderr []byte
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("%s exited %d", e.Name, e.Code)
}

// Run starts name with args and waits for it. A non-zero exit returns the
// captured output and an *ExitError; a passed deadline returns ErrTimeout.
func Run(ctx context.Context, o Options, name string, args ...string) (Result, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return Result{}, fmt.Errorf("%s %w", name, ErrNotFound)
	}
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = o.Dir
	cmd.Env = buildEnv(o, os.Environ())
	cmd.Stdin = o.Stdin
	stdout, stderr := &capped{}, &capped{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = waitDelay + o.Grace
	afterStart, done := killTree(cmd, o)
	defer done()

	if err = cmd.Start(); err == nil {
		afterStart()
		err = cmd.Wait()
	}
	res := Result{Stdout: stdout.buf.Bytes(), Stderr: stderr.buf.Bytes()}
	if cmd.ProcessState != nil {
		res.Code = cmd.ProcessState.ExitCode()
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return res, fmt.Errorf("%s %w", name, ErrTimeout)
	case ctx.Err() != nil:
		return res, ctx.Err()
	case err == nil:
		return res, nil
	case errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success():
		// The program finished; something it started kept a pipe open.
		return res, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) || (cmd.ProcessState != nil && !cmd.ProcessState.Success()) {
		return res, &ExitError{Name: name, Code: res.Code, Stderr: res.Stderr}
	}
	return res, fmt.Errorf("%s: %w", name, err)
}

// Interactive runs name with args attached to the user's terminal: Options.Stdin
// (default os.Stdin), Options.Stdout (default os.Stdout) and Options.Stderr
// (default os.Stderr). It returns the program's exit code. A non-zero exit is
// not an error here; the caller decides what it means. A missing program
// returns ErrNotFound and a passed deadline ErrTimeout.
//
// The program stays in rig's own process group, so it is in the terminal's
// foreground group: it can read the tty and receives Ctrl-C itself. For that
// reason, on a deadline or cancel only the direct child is killed, not
// anything it started.
func Interactive(ctx context.Context, o Options, name string, args ...string) (int, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return 0, fmt.Errorf("%s %w", name, ErrNotFound)
	}
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = o.Dir
	cmd.Env = buildEnv(o, os.Environ())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if o.Stdin != nil {
		cmd.Stdin = o.Stdin
	}
	if o.Stdout != nil {
		cmd.Stdout = o.Stdout
	}
	if o.Stderr != nil {
		cmd.Stderr = o.Stderr
	}
	cmd.WaitDelay = waitDelay
	cmd.Cancel = func() error { return cmd.Process.Kill() }

	err = cmd.Run()
	code := 0
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return code, fmt.Errorf("%s %w", name, ErrTimeout)
	case ctx.Err() != nil:
		return code, ctx.Err()
	case err == nil:
		return code, nil
	case errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success():
		return code, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) || (cmd.ProcessState != nil && !cmd.ProcessState.Success()) {
		return code, nil
	}
	return code, fmt.Errorf("%s: %w", name, err)
}

// Spawn starts name with args detached from the caller and returns its pid
// without waiting. The child has no stdin, stdout or stderr, leads its own
// session (Unix) or is detached and outside any job (Windows), and gets no
// RIG_HOOK_* variable, whatever o says. o.Timeout, Grace and Stdin are
// ignored: the child outlives the caller. On Windows, when the caller's job
// forbids breakaway, Spawn retries inside the job and reports
// breakawayRefused: the child is then detached but dies with that job.
func Spawn(o Options, name string, args ...string) (pid int, breakawayRefused bool, err error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return 0, false, fmt.Errorf("%s %w", name, ErrNotFound)
	}
	env := buildEnv(o, os.Environ())
	if env == nil {
		env = os.Environ()
	}
	clean := make([]string, 0, len(env))
	for _, kv := range env {
		n, _, _ := strings.Cut(kv, "=")
		if runtime.GOOS == "windows" {
			n = strings.ToUpper(n)
		}
		if strings.HasPrefix(n, "RIG_HOOK_") {
			continue
		}
		clean = append(clean, kv)
	}
	for breakaway := true; ; breakaway = false {
		cmd := exec.Command(path, args...)
		cmd.Dir = o.Dir
		cmd.Env = clean
		detach(cmd, breakaway)
		err := cmd.Start()
		if err == nil {
			go cmd.Wait() // reap the child if it ends while the caller lives
			return cmd.Process.Pid, !breakaway, nil
		}
		if !breakaway || !breakawayDenied(err) {
			return 0, false, fmt.Errorf("%s: %w", name, err)
		}
	}
}

// buildEnv returns the child's environment, or nil to inherit the parent's
// unchanged (no Env and no scrub).
func buildEnv(o Options, parent []string) []string {
	if len(o.Env) == 0 && !o.ScrubTokens && o.EnvAllow == nil {
		return nil
	}
	env := make([]string, 0, len(parent)+len(o.Env))
	for _, kv := range parent {
		if o.EnvAllow != nil && !envAllowed(kv, o.EnvAllow) {
			continue
		}
		if o.ScrubTokens && isTokenVar(kv) {
			continue
		}
		env = append(env, kv)
	}
	return append(env, o.Env...)
}

// envAllowed reports whether the "NAME=value" entry matches an allow entry:
// an exact name, or a prefix when the entry ends in "*".
func envAllowed(kv string, allow []string) bool {
	name, _, _ := strings.Cut(kv, "=")
	fold := runtime.GOOS == "windows"
	if fold {
		name = strings.ToUpper(name)
	}
	for _, a := range allow {
		if fold {
			a = strings.ToUpper(a)
		}
		if prefix, ok := strings.CutSuffix(a, "*"); ok {
			if strings.HasPrefix(name, prefix) {
				return true
			}
		} else if name == a {
			return true
		}
	}
	return false
}

// isTokenVar reports whether the "NAME=value" entry names a RIG_<X>_TOKEN.
func isTokenVar(kv string) bool {
	name, _, _ := strings.Cut(kv, "=")
	if runtime.GOOS == "windows" {
		name = strings.ToUpper(name)
	}
	return len(name) > len("RIG__TOKEN") && strings.HasPrefix(name, "RIG_") && strings.HasSuffix(name, "_TOKEN")
}

type capped struct{ buf bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if room := maxOutput - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

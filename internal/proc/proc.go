// Package proc runs a subprocess from an argument vector under a deadline.
//
// Nothing here builds a shell string: the program and each argument reach the
// kernel as given. When the deadline passes, the child and everything it
// started are killed, so a caller never waits on a stuck grandchild.
package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	Dir     string
	Env     []string      // added to the parent's environment
	Timeout time.Duration // 0: only ctx limits the run
	Stdin   io.Reader
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
	if len(o.Env) > 0 {
		cmd.Env = append(os.Environ(), o.Env...)
	}
	cmd.Stdin = o.Stdin
	stdout, stderr := &capped{}, &capped{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = waitDelay
	killTree(cmd)

	err = cmd.Run()
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
	if len(o.Env) > 0 {
		cmd.Env = append(os.Environ(), o.Env...)
	}
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

type capped struct{ buf bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if room := maxOutput - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

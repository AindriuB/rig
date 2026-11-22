package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AindriuB/rig/internal/proc"
	"github.com/AindriuB/rig/internal/store"
)

// Limit is how long a command gate or a hook may run.
const Limit = 30 * time.Second

// limit is Limit, shortened by tests.
var limit = Limit

// Runner runs a program from an argument vector with stdin, and returns an
// error when it does not exit 0.
type Runner interface {
	Run(ctx context.Context, argv []string, stdin []byte) error
}

// ProcRunner runs programs through internal/proc, never through a shell.
type ProcRunner struct {
	Dir string   // "": the current directory
	Env []string // added to the parent's environment
}

// Run runs argv under ctx and Limit. A non-zero exit's error carries the
// start of what the program wrote to stderr.
func (r ProcRunner) Run(ctx context.Context, argv []string, stdin []byte) error {
	if len(argv) == 0 || argv[0] == "" {
		return errors.New("empty argument vector")
	}
	_, err := proc.Run(ctx, proc.Options{Dir: r.Dir, Env: r.Env, Timeout: limit, Stdin: bytes.NewReader(stdin)}, argv[0], argv[1:]...)
	var ee *proc.ExitError
	if errors.As(err, &ee) {
		if msg := firstLine(ee.Stderr); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
	}
	return err
}

// firstLine returns the first non-blank line of b, cut to 200 bytes.
func firstLine(b []byte) string {
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if len(l) > 200 {
				l = l[:200] + "…"
			}
			return l
		}
	}
	return ""
}

// substitute replaces each argument that is exactly a placeholder with its
// value. The value stays one argument whatever it holds; an argument with a
// placeholder inside other text is passed as written.
func substitute(argv []string, values map[string]string) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if v, ok := values[a]; ok {
			out[i] = v
		} else {
			out[i] = a
		}
	}
	return out
}

func placeholderValues(t store.Task, state, reason, sha string) map[string]string {
	return map[string]string{
		"{short}": t.Short, "{id}": t.ID, "{slug}": t.Slug,
		"{state}": state, "{reason}": reason, "{sha}": sha,
	}
}

// run runs argv with stdin under Limit.
func run(ctx context.Context, r Runner, argv []string, stdin []byte) error {
	if r == nil {
		return errors.New("no runner to run programs with")
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	err := r.Run(ctx, argv, stdin)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) && !errors.Is(err, proc.ErrTimeout) {
		err = fmt.Errorf("%w after %s: %w", proc.ErrTimeout, limit, err)
	}
	return err
}

// hookInput is what a hook reads on stdin.
type hookInput struct {
	Event store.Event `json:"event"`
	Task  store.Task  `json:"task"`
}

// RunHooks runs, in file order, every hook the event matches: for a
// transition event, "leave:<from>", "enter:<to>" and "transition". t is the
// task after the event. Each hook gets the event and the task as JSON on
// stdin and Limit to run. A hook that fails does not stop the others; its
// error is returned, and the transition, already written, stands.
func (w *Workflow) RunHooks(ctx context.Context, r Runner, event store.Event, t store.Task) []error {
	if event.Kind != store.KindTransition {
		return nil
	}
	var d store.TransitionData
	if err := json.Unmarshal(event.Data, &d); err != nil {
		return []error{fmt.Errorf("hooks: read transition: %w", err)}
	}
	stdin, err := json.Marshal(hookInput{Event: event, Task: t})
	if err != nil {
		return []error{fmt.Errorf("hooks: %w", err)}
	}
	values := placeholderValues(t, d.To, d.Reason, d.Sha)
	var errs []error
	for i, h := range w.Hooks {
		if h.On != "transition" && h.On != "leave:"+d.From && h.On != "enter:"+d.To {
			continue
		}
		if len(h.Run) == 0 {
			continue
		}
		if err := run(ctx, r, substitute(h.Run, values), stdin); err != nil {
			errs = append(errs, fmt.Errorf("hook %d (%s, %s): %w", i, h.On, h.Run[0], err))
		}
	}
	return errs
}

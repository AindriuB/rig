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

// HookGrace is how long a hook that overran its timeout has, after SIGTERM,
// before it is killed.
const HookGrace = 5 * time.Second

// HookRun is what a hook run adds to a plain Runner.Run: its own time limit
// and environment.
type HookRun struct {
	Timeout  time.Duration
	EnvAllow []string // the parent's variables the hook may see
	Env      []string // set for the hook, after the allowlist
}

// HookRunner is a Runner that can also run a hook with its own limit and
// environment. A Runner that is not one runs hooks as Run does.
type HookRunner interface {
	Runner
	RunHook(ctx context.Context, argv []string, stdin []byte, h HookRun) error
}

// baseHookEnv is the parent environment every hook sees: the non-secret
// groups of the spec's D7.
var baseHookEnv = []string{
	// System.
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "TZ", "LANG", "LC_*", "TERM",
	// Windows.
	"SystemRoot", "SystemDrive", "windir", "ComSpec", "PATHEXT", "TEMP", "TMP",
	"USERPROFILE", "USERNAME", "HOMEDRIVE", "HOMEPATH", "APPDATA", "LOCALAPPDATA",
	"ProgramData", "ProgramFiles*", "COMPUTERNAME", "PROCESSOR_ARCHITECTURE", "NUMBER_OF_PROCESSORS",
	// Network.
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR",
	// Desktop session.
	"XDG_*", "DBUS_SESSION_BUS_ADDRESS",
	// Tool config.
	"GH_CONFIG_DIR", "GLAB_CONFIG_DIR", "GIT_CONFIG_GLOBAL", "GIT_SSH_COMMAND", "GIT_ASKPASS",
	// rig.
	"RIG_WORKFLOW",
}

// RunHook runs argv as a hook: under h.Timeout, then SIGTERM and HookGrace,
// with only h.EnvAllow of the parent's environment.
func (r ProcRunner) RunHook(ctx context.Context, argv []string, stdin []byte, h HookRun) error {
	if len(argv) == 0 || argv[0] == "" {
		return errors.New("empty argument vector")
	}
	env := append(append([]string(nil), r.Env...), h.Env...)
	o := proc.Options{Dir: r.Dir, Env: env, EnvAllow: h.EnvAllow, Timeout: h.Timeout,
		Grace: HookGrace, JobBreakaway: true, Stdin: bytes.NewReader(stdin)}
	_, err := proc.Run(ctx, o, argv[0], argv[1:]...)
	var ee *proc.ExitError
	if errors.As(err, &ee) {
		if msg := firstLine(ee.Stderr); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
	}
	return err
}

// hookInput is what a hook reads on stdin. Task is null and Effective absent
// for an event that belongs to no task.
type hookInput struct {
	Format    int         `json:"format"`
	Event     store.Event `json:"event"`
	Task      *TaskView   `json:"task"`
	Effective string      `json:"effective,omitempty"`
}

// hookMatches reports whether any of h's selectors matches the event. d is
// the event's transition data when it is a transition.
func (w *Workflow) hookMatches(h Hook, kind string, d store.TransitionData) bool {
	for _, sel := range h.On {
		switch {
		case sel == "transition":
			if kind == store.KindTransition {
				return true
			}
		case sel == "final":
			if kind == store.KindTransition && w.States[d.To].Final {
				return true
			}
		case strings.HasPrefix(sel, "enter:"):
			if kind == store.KindTransition && sel == "enter:"+d.To {
				return true
			}
		case strings.HasPrefix(sel, "leave:"):
			if kind == store.KindTransition && sel == "leave:"+d.From {
				return true
			}
		case strings.HasPrefix(sel, "kind:"):
			if k := sel[len("kind:"):]; k == "*" || k == kind {
				return true
			}
		}
	}
	return false
}

// RunHooks runs, in file order, every inline hook the event matches. t is
// the task after the event and headSha its branch head now ("" when the
// branch does not exist). Each hook gets {"format":1,"event":…,"task":…,
// "effective":…} on stdin, where task.state is the recorded state and
// effective the state after the sha-bound check, and runs for its timeout
// (Limit by default). A hook that fails does not stop the others; its error
// is returned, and the write, already made, stands. Queued hooks are not
// run.
func (w *Workflow) RunHooks(ctx context.Context, r Runner, event store.Event, t store.Task, headSha string) []error {
	return w.runHooks(ctx, r, event, &t, headSha)
}

// RunRepoHooks is RunHooks for an event that belongs to no task, such as a
// repository note.
func (w *Workflow) RunRepoHooks(ctx context.Context, r Runner, event store.Event) []error {
	return w.runHooks(ctx, r, event, nil, "")
}

func (w *Workflow) runHooks(ctx context.Context, r Runner, event store.Event, t *store.Task, headSha string) []error {
	var d store.TransitionData
	if event.Kind == store.KindTransition {
		if err := json.Unmarshal(event.Data, &d); err != nil {
			return []error{fmt.Errorf("hooks: read transition: %w", err)}
		}
	}
	in := hookInput{Format: 1, Event: event}
	var values map[string]string
	var taskID string
	if t != nil {
		v := w.View(*t, headSha)
		// task is the task as recorded right after the event, as rig events
		// shows it; effective is the state after the sha-bound check.
		in.Effective, taskID = v.State, t.ID
		v.State = v.RecordedState
		in.Task = &v
		state := t.State
		if event.Kind == store.KindTransition {
			state = d.To
		}
		values = placeholderValues(*t, state, d.Reason, d.Sha)
	} else {
		values = placeholderValues(store.Task{}, "", "", "")
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return []error{fmt.Errorf("hooks: %w", err)}
	}
	var errs []error
	for i, h := range w.Hooks {
		if h.Mode == ModeQueued || len(h.Run) == 0 || !w.hookMatches(h, event.Kind, d) {
			continue
		}
		name := w.HookName(i)
		env := []string{"RIG_HOOK_NAME=" + name, "RIG_HOOK_EVENT=" + event.ID, "RIG_ACTOR=hook:" + name}
		if taskID != "" {
			env = append(env, "RIG_HOOK_TASK="+taskID)
		}
		timeout := limit
		if h.Timeout != "" {
			if d, err := time.ParseDuration(h.Timeout); err == nil && d > 0 {
				timeout = d
			}
		}
		hr := HookRun{Timeout: timeout, Env: env,
			EnvAllow: append(append([]string(nil), baseHookEnv...), h.Env...)}
		if err := runHook(ctx, r, substitute(h.Run, values), stdin, hr); err != nil {
			errs = append(errs, fmt.Errorf("hook %d %s (%s, %s): %w", i, name, strings.Join(h.On, ","), h.Run[0], err))
		}
	}
	return errs
}

// runHook runs a hook under hr.Timeout. A runner that takes the hook's
// environment gets the grace period on top, to end the program gently.
func runHook(ctx context.Context, r Runner, argv []string, stdin []byte, hr HookRun) error {
	if r == nil {
		return errors.New("no runner to run programs with")
	}
	hrun, ok := r.(HookRunner)
	outer := hr.Timeout
	if ok {
		outer += HookGrace + 3*time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, outer)
	defer cancel()
	var err error
	if ok {
		err = hrun.RunHook(ctx, argv, stdin, hr)
	} else {
		err = r.Run(ctx, argv, stdin)
	}
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) && !errors.Is(err, proc.ErrTimeout) {
		err = fmt.Errorf("%w after %s: %w", proc.ErrTimeout, hr.Timeout, err)
	}
	return err
}

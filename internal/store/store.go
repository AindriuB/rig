package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Finding statuses.
const (
	StatusOpen     = "open"
	StatusFixed    = "fixed"
	StatusWontfix  = "wontfix"
	StatusDeferred = "deferred"
)

// validStatus reports whether s is a finding status.
func validStatus(s string) bool {
	switch s {
	case StatusOpen, StatusFixed, StatusWontfix, StatusDeferred:
		return true
	}
	return false
}

// Short id prefixes.
const (
	TaskPrefix    = "t-"
	FindingPrefix = "f-"
	NotePrefix    = "n-"
)

// WorkflowRef names the workflow, and its version, a task was created under.
type WorkflowRef struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}

// Task is the state a task log folds to.
type Task struct {
	ID       string         `json:"id"`
	Short    string         `json:"short"`
	Slug     string         `json:"slug"`
	Title    string         `json:"title"`
	Body     string         `json:"body"`
	Fields   map[string]any `json:"fields"`
	Workflow WorkflowRef    `json:"workflow"`
	// State is the state the log alone gives (the recorded state).
	State string `json:"state"`
	// Previous is the from-state of the latest transition.
	Previous string `json:"previous,omitempty"`
	// StateSha is the sha recorded by the transition into the current state,
	// or "" when that transition recorded none.
	StateSha string            `json:"stateSha,omitempty"`
	Shas     map[string]string `json:"shas"`
	Branch   string            `json:"branch"`
	// Parent is the id of the task's parent, or "".
	Parent string `json:"parent,omitempty"`
	// External is the issue the task is linked to, or nil.
	External     *External      `json:"external,omitempty"`
	PR           int            `json:"pr,omitempty"`
	Reason       string         `json:"reason,omitempty"`
	Counters     map[string]int `json:"counters"`
	OpenFindings int            `json:"openFindings"`
	// Skipped counts log lines and events rig could not read; the task's
	// state is then only what the readable part says.
	Skipped int       `json:"skipped,omitempty"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// External is a task's link to a tracker issue. SyncedAt, Rev and Snapshot
// come from the latest tracker snapshot.
type External struct {
	Kind      string         `json:"kind"`
	Host      string         `json:"host,omitempty"`
	Project   string         `json:"project,omitempty"`
	Key       string         `json:"key"`
	URL       string         `json:"url,omitempty"`
	Type      string         `json:"type,omitempty"`
	Owned     []string       `json:"owned,omitempty"`
	ParentKey string         `json:"parentKey,omitempty"`
	SyncedAt  time.Time      `json:"syncedAt,omitzero"`
	Rev       string         `json:"rev,omitempty"`
	Snapshot  map[string]any `json:"snapshot,omitempty"`
}

// Finding is one review finding on a task.
type Finding struct {
	ID       string         `json:"id"`
	Short    string         `json:"short"`
	Task     string         `json:"task"`
	Severity string         `json:"severity"`
	Status   string         `json:"status"`
	Key      string         `json:"key"`
	Summary  string         `json:"summary"`
	Path     string         `json:"path,omitempty"`
	Fields   map[string]any `json:"fields,omitempty"`
	// Counters are the task's counters when the finding was added.
	Counters map[string]int `json:"counters"`
	Created  time.Time      `json:"created"`
	Updated  time.Time      `json:"updated"`
}

// Note is an append-only record: a decision, what landed. Task is empty for
// a note that belongs to the repository.
type Note struct {
	ID      string    `json:"id"`
	Short   string    `json:"short"`
	Task    string    `json:"task,omitempty"`
	Kind    string    `json:"kind"`
	Text    string    `json:"text"`
	Created time.Time `json:"created"`
}

// NewTask is what CreateTask needs. Initial is the state the task starts in.
type NewTask struct {
	Slug     string
	Title    string
	Body     string
	Branch   string
	Fields   map[string]any
	Workflow WorkflowRef
	Initial  string
	// Parent is a task reference; CreateTask stores the parent's id.
	Parent string
	// External links the task to an issue. At most one task links an issue.
	External *External
	// Snapshot is the tracker's values for the owned fields.
	Snapshot map[string]any
	// BranchTemplate, when set, is expanded ({short}, {slug}, {id}, {key})
	// into the task's Branch, replacing Branch.
	BranchTemplate string
}

// TaskFilter narrows Tasks. An empty States matches every state.
type TaskFilter struct {
	States []string
}

// NoteFilter narrows Notes. Task is a task reference; Limit 0 means no limit.
type NoteFilter struct {
	Task  string
	Kind  string
	Limit int
}

// Store is the port every backend implements.
type Store interface {
	// CreateTask starts a new task log.
	CreateTask(ctx context.Context, t NewTask) (Task, error)
	// Task resolves ref (full ULID, short id or slug) to a task.
	Task(ctx context.Context, ref string) (Task, error)
	// Tasks lists tasks, newest first.
	Tasks(ctx context.Context, filter TaskFilter) ([]Task, error)
	// Update appends the events next returns, given the task as it is now.
	// next returns events with Kind and Data set; the store fills V, ID, Seq,
	// Time and Actor. If next returns an error, Update returns it and writes
	// nothing. A backend may call next more than once.
	Update(ctx context.Context, ref string, next func(Task, []Finding) ([]Event, error)) (Task, error)
	// UpdateEvents is Update that also returns the events it appended, as
	// stamped, in log order. A callback that returns none yields an empty slice.
	UpdateEvents(ctx context.Context, ref string, next func(Task, []Finding) ([]Event, error)) (Task, []Event, error)
	// CreateTaskEvent is CreateTask that also returns the created event.
	CreateTaskEvent(ctx context.Context, t NewTask) (Task, Event, error)
	// AddNoteEvent is AddNote that also returns the note's event.
	AddNoteEvent(ctx context.Context, n Note) (Note, Event, error)
	// Events returns a task's log in (seq, id) order.
	Events(ctx context.Context, ref string) ([]Event, error)
	// Findings returns a task's findings in the order they were added.
	Findings(ctx context.Context, ref string) ([]Finding, error)
	// AddNote appends a note to its task's log, or to the repository log when
	// n.Task is empty. n.Task may be any task reference.
	AddNote(ctx context.Context, n Note) (Note, error)
	// Notes lists notes, newest first.
	Notes(ctx context.Context, filter NoteFilter) ([]Note, error)
}

// ErrNotFound reports a reference that matches nothing.
var ErrNotFound = errors.New("not found")

// ErrRepeat reports a write whose Options.Once key is already in the log it
// would append to. Nothing was written.
var ErrRepeat = errors.New("repeat")

// HasOnce reports whether any event in log carries the once key key. An empty
// key is never a repeat.
func HasOnce(log []Event, key string) bool {
	if key == "" {
		return false
	}
	for _, e := range log {
		if e.Once == key {
			return true
		}
	}
	return false
}

// ErrLinked reports that CreateTask found a task already linked to the
// issue. The task returned with it is that task.
var ErrLinked = errors.New("issue already linked to a task")

// ErrAmbiguous reports a reference that matches more than one task.
var ErrAmbiguous = errors.New("ambiguous")

// AmbiguousError lists the task ids a reference matched.
type AmbiguousError struct {
	Ref     string
	Matches []string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("%q is ambiguous: matches %s", e.Ref, strings.Join(e.Matches, ", "))
}

// Is makes errors.Is(err, ErrAmbiguous) true.
func (e *AmbiguousError) Is(target error) bool { return target == ErrAmbiguous }

// Options inject the clock and the actor into a backend.
type Options struct {
	Now   func() time.Time // nil: time.Now
	Actor string           // "": "unknown"
	Cause *Cause           // nil: events carry no cause
	Once  string           // "": events carry no once key; else a write whose log has it is a repeat (ErrRepeat)
}

// WithDefaults fills the zero fields of o.
func (o Options) WithDefaults() Options {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Actor == "" {
		o.Actor = "unknown"
	}
	return o
}

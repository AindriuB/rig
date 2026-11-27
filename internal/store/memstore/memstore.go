// Package memstore is an in-memory store.Store, for tests. One mutex guards
// everything, so an Update's callback sees the task as it is and nothing can
// write between the callback and the append.
package memstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/AindriuB/rig/internal/store"
)

// Store keeps every task log, and the repository's notes log, in memory.
type Store struct {
	mu    sync.Mutex
	opts  store.Options
	logs  map[string][]store.Event // task id -> log in (seq, id) order
	notes []store.Event            // the repository's notes log
}

// New returns an empty store that stamps events with opts' clock and actor.
func New(opts store.Options) *Store {
	return &Store{opts: opts.WithDefaults(), logs: map[string][]store.Event{}}
}

var _ store.Store = (*Store)(nil)

// tasks folds every task log. The caller holds mu.
func (s *Store) tasks() []store.Task {
	out := make([]store.Task, 0, len(s.logs))
	for _, log := range s.logs {
		t, _, _, _ := store.Fold(log)
		out = append(out, t)
	}
	store.SortTasks(out)
	return out
}

// resolve returns the id of the task ref names. The caller holds mu.
func (s *Store) resolve(ref string) (string, error) {
	t, err := store.Resolve(ref, s.tasks())
	if err != nil {
		return "", err
	}
	return t.ID, nil
}

// CreateTask starts a new task log.
func (s *Store) CreateTask(ctx context.Context, n store.NewTask) (store.Task, error) {
	t, _, err := s.CreateTaskEvent(ctx, n)
	return t, err
}

// CreateTaskEvent is CreateTask that also returns the created event.
func (s *Store) CreateTaskEvent(ctx context.Context, n store.NewTask) (store.Task, store.Event, error) {
	if err := ctx.Err(); err != nil {
		return store.Task{}, store.Event{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.opts.Once != "" {
		for _, log := range s.logs {
			if len(log) > 0 && store.HasOnce(log[:1], s.opts.Once) {
				return store.Task{}, store.Event{}, store.ErrRepeat
			}
		}
	}
	all := s.tasks()
	if existing, err := store.PrepareCreate(&n, all); err != nil {
		return existing, store.Event{}, err
	}
	shorts := map[string]bool{}
	for _, t := range all {
		shorts[t.Short] = true
	}
	e, err := s.opts.Created(n, func(sh string) bool { return shorts[sh] })
	if err != nil {
		return store.Task{}, store.Event{}, err
	}
	s.logs[e.ID] = []store.Event{e}
	t, _, _, _ := store.Fold(s.logs[e.ID])
	return t, e, nil
}

// Task resolves ref to a task.
func (s *Store) Task(ctx context.Context, ref string) (store.Task, error) {
	if err := ctx.Err(); err != nil {
		return store.Task{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return store.Resolve(ref, s.tasks())
}

// Tasks lists tasks in the filter's states, newest first.
func (s *Store) Tasks(ctx context.Context, f store.TaskFilter) ([]store.Task, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.Task
	for _, t := range s.tasks() {
		if len(f.States) == 0 || slices.Contains(f.States, t.State) {
			out = append(out, t)
		}
	}
	return out, nil
}

// Update appends the events next returns. Nothing is written if next, or
// stamping its events, fails. next runs under the store's lock and must not
// call the store.
func (s *Store) Update(ctx context.Context, ref string, next func(store.Task, []store.Finding) ([]store.Event, error)) (store.Task, error) {
	t, _, err := s.UpdateEvents(ctx, ref, next)
	return t, err
}

// UpdateEvents is Update that also returns the events it appended, as
// stamped, in log order; none if next returned none.
func (s *Store) UpdateEvents(ctx context.Context, ref string, next func(store.Task, []store.Finding) ([]store.Event, error)) (store.Task, []store.Event, error) {
	if err := ctx.Err(); err != nil {
		return store.Task{}, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := s.resolve(ref)
	if err != nil {
		return store.Task{}, nil, err
	}
	if next == nil {
		return store.Task{}, nil, errors.New("update: no callback")
	}
	log := s.logs[id]
	if store.HasOnce(log, s.opts.Once) {
		return store.Task{}, nil, store.ErrRepeat
	}
	task, findings, _, _ := store.Fold(log)
	events, err := next(task, findings)
	if err != nil {
		return store.Task{}, nil, err
	}
	stamped, err := s.opts.Stamp(log, events)
	if err != nil {
		return store.Task{}, nil, fmt.Errorf("update %s: %w", task.Short, err)
	}
	if err := store.CheckRelink(id, stamped, s.tasks()); err != nil {
		return store.Task{}, nil, fmt.Errorf("update %s: %w", task.Short, err)
	}
	for _, e := range stamped {
		if e.Kind != store.KindParent {
			continue
		}
		var d store.ParentData
		if err := json.Unmarshal(e.Data, &d); err != nil {
			return store.Task{}, nil, fmt.Errorf("update %s: %w", task.Short, err)
		}
		if err := store.CheckParent(id, d.Task, s.tasks()); err != nil {
			return store.Task{}, nil, fmt.Errorf("update %s: %w", task.Short, err)
		}
	}
	s.logs[id] = append(slices.Clip(log), stamped...)
	task, _, _, _ = store.Fold(s.logs[id])
	return task, slices.Clone(stamped), nil
}

// Events returns a task's log in (seq, id) order.
func (s *Store) Events(ctx context.Context, ref string) ([]store.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := s.resolve(ref)
	if err != nil {
		return nil, err
	}
	return slices.Clone(s.logs[id]), nil
}

// Findings returns a task's findings in the order they were added.
func (s *Store) Findings(ctx context.Context, ref string) ([]store.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := s.resolve(ref)
	if err != nil {
		return nil, err
	}
	_, findings, _, _ := store.Fold(s.logs[id])
	return findings, nil
}

// AddNote appends a note to its task's log, or to the repository's log.
func (s *Store) AddNote(ctx context.Context, n store.Note) (store.Note, error) {
	note, _, err := s.AddNoteEvent(ctx, n)
	return note, err
}

// AddNoteEvent is AddNote that also returns the note's event.
func (s *Store) AddNoteEvent(ctx context.Context, n store.Note) (store.Note, store.Event, error) {
	if err := ctx.Err(); err != nil {
		return store.Note{}, store.Event{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, err := store.NewEvent(store.KindNote, store.NoteData{Kind: n.Kind, Text: n.Text})
	if err != nil {
		return store.Note{}, store.Event{}, fmt.Errorf("add note: %w", err)
	}
	log := s.notes
	var id string
	if n.Task != "" {
		if id, err = s.resolve(n.Task); err != nil {
			return store.Note{}, store.Event{}, err
		}
		log = s.logs[id]
	}
	if store.HasOnce(log, s.opts.Once) {
		return store.Note{}, store.Event{}, store.ErrRepeat
	}
	stamped, err := s.opts.Stamp(log, []store.Event{e})
	if err != nil {
		return store.Note{}, store.Event{}, fmt.Errorf("add note: %w", err)
	}
	log = append(slices.Clip(log), stamped...)
	if id != "" {
		s.logs[id] = log
	} else {
		s.notes = log
	}
	_, _, notes, _ := store.Fold(log)
	for _, note := range notes {
		if note.ID == stamped[0].ID {
			return note, stamped[0], nil
		}
	}
	return store.Note{}, store.Event{}, errors.New("add note: the note did not fold")
}

// Notes lists notes, newest first.
func (s *Store) Notes(ctx context.Context, f store.NoteFilter) ([]store.Note, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []store.Note
	if f.Task != "" {
		id, err := s.resolve(f.Task)
		if err != nil {
			return nil, err
		}
		_, _, all, _ = store.Fold(s.logs[id])
	} else {
		_, _, all, _ = store.Fold(s.notes)
		for _, log := range s.logs {
			_, _, notes, _ := store.Fold(log)
			all = append(all, notes...)
		}
	}
	return store.FilterNotes(all, f.Kind, f.Limit), nil
}

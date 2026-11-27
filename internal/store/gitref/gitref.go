// Package gitref is the git-ref store.Store backend.
//
// Each task is one ref, refs/rig/tasks/<ULID>, and the repository's notes are
// refs/rig/notes. A ref points at a commit whose tree holds one file,
// log.jsonl, with one event per line in append order. An append writes a new
// blob, tree and commit (parent: the previous tip) and moves the ref with a
// compare-and-swap, retrying from the read when another writer got there
// first. Only plumbing is used, so the index, HEAD and the working tree are
// never touched.
package gitref

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/AindriuB/rig/internal/git"
	"github.com/AindriuB/rig/internal/store"
)

const (
	// TasksPrefix is where task refs live.
	TasksPrefix = "refs/rig/tasks/"
	// NotesRef holds the notes that belong to no task.
	NotesRef = "refs/rig/notes"
	// LogFile is the one file in each commit's tree.
	LogFile = "log.jsonl"

	maxTries   = 8
	minBackoff = 10 * time.Millisecond
	maxBackoff = 200 * time.Millisecond
)

// ErrConflict reports that a write lost the compare-and-swap race on every
// try. Nothing was written.
var ErrConflict = errors.New("store conflict: the ref kept moving")

// plumbing is the part of git.Repo the store uses. Each call is one git
// process. Tests wrap it to count processes.
type plumbing interface {
	WriteBlob(ctx context.Context, data []byte) (string, error)
	MakeTree(ctx context.Context, entries []git.TreeEntry) (string, error)
	CommitTree(ctx context.Context, tree string, parents []string, message string) (string, error)
	UpdateRef(ctx context.Context, ref, newOID, oldOID string) error
	ResolveRef(ctx context.Context, ref string) (string, bool, error)
	ReadRefs(ctx context.Context, prefix string) ([]git.Ref, error)
	ReadBlobs(ctx context.Context, specs []string) ([]git.Blob, error)
	CommonDir(ctx context.Context) (string, error)
}

// Store keeps task logs as refs in one repository. Each write holds the
// store lock (see lock.go) for its read, callback and append, so writers in
// this and other processes take turns instead of spending their tries racing
// each other. The compare-and-swap protects every existing ref; uniqueness of
// external links for new tasks relies on the lock and is best-effort across a
// stale-lock break (duplicates then show as ErrAmbiguous on Resolve). Reads
// take no lock. An Update callback must not write through any Store of the same
// repository.
type Store struct {
	mu       sync.Mutex // held for the whole of each write, around the file lock
	git      plumbing
	opts     store.Options
	sleep    func(context.Context, time.Duration) error
	lockPath string        // resolved on the first write
	lockWait time.Duration // how long a write waits for the lock
}

var _ store.Store = (*Store)(nil)

// Open returns a store over repo's refs/rig namespace, stamping events with
// opts' clock and actor. It runs no git command.
func Open(repo git.Repo, opts store.Options) (*Store, error) {
	return newStore(repo, opts), nil
}

func newStore(g plumbing, opts store.Options) *Store {
	return &Store{git: g, opts: opts.WithDefaults(), sleep: sleepCtx, lockWait: lockWait}
}

// sleepCtx waits for d or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// taskLog is one log as read: its ref, the commit it was read at ("" when the
// ref does not exist), the blob's bytes, the events they hold and how many
// lines were skipped.
type taskLog struct {
	ref     string
	tip     string
	raw     []byte
	events  []store.Event
	skipped int
}

// parseLog decodes log.jsonl. A line whose v this rig does not know, or that
// does not parse, is skipped and counted, never an error: one bad line must
// not hide every other task. Skipped lines stay in raw, so an append keeps
// them byte for byte.
func parseLog(raw []byte) (events []store.Event, skipped int) {
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var head struct {
			V int `json:"v"`
		}
		if err := json.Unmarshal(line, &head); err != nil || head.V != store.Version {
			skipped++
			continue
		}
		var e store.Event
		if err := json.Unmarshal(line, &e); err != nil {
			skipped++
			continue
		}
		events = append(events, e)
	}
	store.SortEvents(events)
	return events, skipped
}

// readAll reads every log under prefix whose ref keep accepts (nil keeps
// all): one for-each-ref and one cat-file.
func (s *Store) readAll(ctx context.Context, prefix string, keep func(string) bool) ([]taskLog, error) {
	all, err := s.git.ReadRefs(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("read store: %w", err)
	}
	refs := all
	if keep != nil {
		refs = nil
		for _, r := range all {
			if keep(r.Name) {
				refs = append(refs, r)
			}
		}
	}
	if len(refs) == 0 {
		return nil, nil
	}
	specs := make([]string, len(refs))
	for i, r := range refs {
		specs[i] = r.OID + ":" + LogFile
	}
	blobs, err := s.git.ReadBlobs(ctx, specs)
	if err != nil {
		return nil, fmt.Errorf("read store: %w", err)
	}
	logs := make([]taskLog, 0, len(refs))
	for i, r := range refs {
		if blobs[i].Missing {
			return nil, fmt.Errorf("read store: %s has no %s", r.Name, LogFile)
		}
		evs, skipped := parseLog(blobs[i].Data)
		logs = append(logs, taskLog{ref: r.Name, tip: r.OID, raw: blobs[i].Data, events: evs, skipped: skipped})
	}
	return logs, nil
}

// readOne reads one ref's log; a ref that does not exist is an empty log.
func (s *Store) readOne(ctx context.Context, ref string) (taskLog, error) {
	tip, ok, err := s.git.ResolveRef(ctx, ref)
	if err != nil {
		return taskLog{}, fmt.Errorf("read %s: %w", ref, err)
	}
	if !ok {
		return taskLog{ref: ref}, nil
	}
	blobs, err := s.git.ReadBlobs(ctx, []string{tip + ":" + LogFile})
	if err != nil {
		return taskLog{}, fmt.Errorf("read %s: %w", ref, err)
	}
	if blobs[0].Missing {
		return taskLog{}, fmt.Errorf("read %s: no %s", ref, LogFile)
	}
	evs, skipped := parseLog(blobs[0].Data)
	return taskLog{ref: ref, tip: tip, raw: blobs[0].Data, events: evs, skipped: skipped}, nil
}

// fold folds every task log, newest first, and maps task id to its log.
func fold(logs []taskLog) ([]store.Task, map[string]taskLog) {
	tasks := make([]store.Task, 0, len(logs))
	byID := make(map[string]taskLog, len(logs))
	for _, l := range logs {
		t, _, _, _ := store.Fold(l.events)
		if t.ID == "" {
			continue
		}
		t.Skipped += l.skipped
		tasks = append(tasks, t)
		byID[t.ID] = l
	}
	store.SortTasks(tasks)
	return tasks, byID
}

// resolve reads every task and returns the log of the one ref names.
func (s *Store) resolve(ctx context.Context, ref string) (taskLog, error) {
	logs, err := s.readAll(ctx, TasksPrefix, nil)
	if err != nil {
		return taskLog{}, err
	}
	tasks, byID := fold(logs)
	t, err := store.Resolve(ref, tasks)
	if err != nil {
		return taskLog{}, err
	}
	return byID[t.ID], nil
}

// encode appends events to raw as JSON lines. Existing lines are kept byte
// for byte, so events this rig does not understand survive an append.
func encode(raw []byte, events []store.Event) ([]byte, error) {
	out := slices.Clip(raw)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	for _, e := range events {
		line, err := json.Marshal(e)
		if err != nil {
			return nil, fmt.Errorf("encode %s event: %w", e.Kind, err)
		}
		out = append(append(out, line...), '\n')
	}
	return out, nil
}

// commit writes log plus events as a commit on top of log.tip and moves the
// ref there if it is still at log.tip. It returns git.ErrRefMoved (wrapped)
// when another writer moved it first.
func (s *Store) commit(ctx context.Context, log taskLog, events []store.Event, message string) error {
	data, err := encode(log.raw, events)
	if err != nil {
		return err
	}
	blob, err := s.git.WriteBlob(ctx, data)
	if err != nil {
		return fmt.Errorf("write %s: %w", log.ref, err)
	}
	tree, err := s.git.MakeTree(ctx, []git.TreeEntry{{Mode: "100644", Type: "blob", OID: blob, Name: LogFile}})
	if err != nil {
		return fmt.Errorf("write %s: %w", log.ref, err)
	}
	var parents []string
	if log.tip != "" {
		parents = []string{log.tip}
	}
	c, err := s.git.CommitTree(ctx, tree, parents, message+"\n")
	if err != nil {
		return fmt.Errorf("write %s: %w", log.ref, err)
	}
	if err := s.git.UpdateRef(ctx, log.ref, c, log.tip); err != nil {
		return fmt.Errorf("write %s: %w", log.ref, err)
	}
	return nil
}

// appendLoop is the compare-and-swap loop on one ref. build gets the log as
// it is now and returns the stamped events to append and the commit message;
// no events means nothing to write. build runs again on every retry, so a
// step another writer made illegal is refused then. It returns the log after
// the append.
func (s *Store) appendLoop(ctx context.Context, log taskLog, build func([]store.Event, int) ([]store.Event, string, error)) ([]store.Event, int, error) {
	for try := 1; ; try++ {
		events, message, err := build(log.events, log.skipped)
		if err != nil {
			return nil, 0, err
		}
		if len(events) == 0 {
			return log.events, log.skipped, nil
		}
		err = s.commit(ctx, log, events, message)
		if err == nil {
			out := append(slices.Clip(log.events), events...)
			store.SortEvents(out)
			return out, log.skipped, nil
		}
		if !errors.Is(err, git.ErrRefMoved) {
			return nil, 0, err
		}
		if try == maxTries {
			return nil, 0, fmt.Errorf("write %s: %w after %d tries", log.ref, ErrConflict, maxTries)
		}
		backoff := minBackoff + rand.N(maxBackoff-minBackoff+1)
		if err := s.sleep(ctx, backoff); err != nil {
			return nil, 0, err
		}
		if log, err = s.readOne(ctx, log.ref); err != nil {
			return nil, 0, err
		}
		if log.tip == "" {
			return nil, 0, fmt.Errorf("write %s: the ref was deleted: %w", log.ref, store.ErrNotFound)
		}
	}
}

// message is a commit message: the first event's kind and a short id.
func message(events []store.Event, short string) string {
	return events[0].Kind + " " + short
}

// CreateTask starts a new task log at refs/rig/tasks/<ULID>. Its short id
// avoids every short id already in the store. It refuses a second task for one
// external issue (ErrLinked) under the store lock, so that check is
// best-effort across a stale-lock break; see lock.
func (s *Store) CreateTask(ctx context.Context, n store.NewTask) (store.Task, error) {
	t, _, err := s.CreateTaskEvent(ctx, n)
	return t, err
}

// CreateTaskEvent is CreateTask that also returns the created event.
func (s *Store) CreateTaskEvent(ctx context.Context, n store.NewTask) (store.Task, store.Event, error) {
	if err := ctx.Err(); err != nil {
		return store.Task{}, store.Event{}, err
	}
	unlock, err := s.lock(ctx)
	if err != nil {
		return store.Task{}, store.Event{}, err
	}
	defer unlock()
	logs, err := s.readAll(ctx, TasksPrefix, nil)
	if err != nil {
		return store.Task{}, store.Event{}, err
	}
	if s.opts.Once != "" {
		for _, l := range logs {
			if len(l.events) > 0 && store.HasOnce(l.events[:1], s.opts.Once) {
				return store.Task{}, store.Event{}, store.ErrRepeat
			}
		}
	}
	tasks, _ := fold(logs)
	if existing, err := store.PrepareCreate(&n, tasks); err != nil {
		return existing, store.Event{}, err
	}
	shorts := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		shorts[t.Short] = true
	}
	e, err := s.opts.Created(n, func(sh string) bool { return shorts[sh] })
	if err != nil {
		return store.Task{}, store.Event{}, err
	}
	var d store.CreatedData
	if err := json.Unmarshal(e.Data, &d); err != nil {
		return store.Task{}, store.Event{}, fmt.Errorf("new task: %w", err)
	}
	log := taskLog{ref: TasksPrefix + e.ID}
	if err := s.commit(ctx, log, []store.Event{e}, message([]store.Event{e}, d.Short)); err != nil {
		if errors.Is(err, git.ErrRefMoved) {
			return store.Task{}, store.Event{}, fmt.Errorf("new task: %w", ErrConflict)
		}
		return store.Task{}, store.Event{}, err
	}
	t, _, _, _ := store.Fold([]store.Event{e})
	return t, e, nil
}

// Task resolves ref (full ULID, short id or slug) to a task.
func (s *Store) Task(ctx context.Context, ref string) (store.Task, error) {
	if err := ctx.Err(); err != nil {
		return store.Task{}, err
	}
	logs, err := s.readAll(ctx, TasksPrefix, nil)
	if err != nil {
		return store.Task{}, err
	}
	tasks, _ := fold(logs)
	return store.Resolve(ref, tasks)
}

// Tasks lists tasks in the filter's states, newest first. It starts two git
// processes however many tasks there are.
func (s *Store) Tasks(ctx context.Context, f store.TaskFilter) ([]store.Task, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	logs, err := s.readAll(ctx, TasksPrefix, nil)
	if err != nil {
		return nil, err
	}
	all, _ := fold(logs)
	var out []store.Task
	for _, t := range all {
		if len(f.States) == 0 || slices.Contains(f.States, t.State) {
			out = append(out, t)
		}
	}
	return out, nil
}

// Update appends the events next returns, given the task as it is now. If
// the ref moves before the append lands, it reads the task again and calls
// next again, up to 8 tries; then it returns a wrapped ErrConflict. Nothing
// is written if next, or stamping its events, fails. An external event that
// would link an issue another task links is refused (ErrLinked) under the
// store lock, so that check is best-effort across a stale-lock break, as in
// CreateTask; see lock.
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
	unlock, err := s.lock(ctx)
	if err != nil {
		return store.Task{}, nil, err
	}
	defer unlock()
	log, err := s.resolve(ctx, ref)
	if err != nil {
		return store.Task{}, nil, err
	}
	if next == nil {
		return store.Task{}, nil, errors.New("update: no callback")
	}
	written := []store.Event{}
	out, skipped, err := s.appendLoop(ctx, log, func(cur []store.Event, parseSkipped int) ([]store.Event, string, error) {
		written = []store.Event{}
		if store.HasOnce(cur, s.opts.Once) {
			return nil, "", store.ErrRepeat
		}
		task, findings, _, _ := store.Fold(cur)
		if task.ID != "" {
			task.Skipped += parseSkipped
		}
		events, err := next(task, findings)
		if err != nil {
			return nil, "", err
		}
		stamped, err := s.opts.Stamp(cur, events)
		if err != nil {
			return nil, "", fmt.Errorf("update %s: %w", task.Short, err)
		}
		if slices.ContainsFunc(stamped, func(e store.Event) bool { return e.Kind == store.KindExternal || e.Kind == store.KindParent }) {
			logs, err := s.readAll(ctx, TasksPrefix, nil)
			if err != nil {
				return nil, "", err
			}
			tasks, _ := fold(logs)
			if err := store.CheckRelink(task.ID, stamped, tasks); err != nil {
				return nil, "", fmt.Errorf("update %s: %w", task.Short, err)
			}
			for _, e := range stamped {
				if e.Kind != store.KindParent {
					continue
				}
				var d store.ParentData
				if err := json.Unmarshal(e.Data, &d); err != nil {
					return nil, "", fmt.Errorf("update %s: %w", task.Short, err)
				}
				if err := store.CheckParent(task.ID, d.Task, tasks); err != nil {
					return nil, "", fmt.Errorf("update %s: %w", task.Short, err)
				}
			}
		}
		if len(stamped) == 0 {
			return nil, "", nil
		}
		written = stamped
		return stamped, message(stamped, task.Short), nil
	})
	if err != nil {
		if cerr := ctx.Err(); cerr != nil && !errors.Is(err, cerr) {
			err = errors.Join(cerr, err)
		}
		return store.Task{}, nil, err
	}
	task, _, _, _ := store.Fold(out)
	if task.ID != "" {
		task.Skipped += skipped
	}
	return task, written, nil
}

// Events returns a task's log in (seq, id) order.
func (s *Store) Events(ctx context.Context, ref string) ([]store.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	log, err := s.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	return log.events, nil
}

// Findings returns a task's findings in the order they were added.
func (s *Store) Findings(ctx context.Context, ref string) ([]store.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	log, err := s.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	_, findings, _, _ := store.Fold(log.events)
	return findings, nil
}

// AddNote appends a note to its task's log, or to refs/rig/notes when n.Task
// is empty.
func (s *Store) AddNote(ctx context.Context, n store.Note) (store.Note, error) {
	note, _, err := s.AddNoteEvent(ctx, n)
	return note, err
}

// AddNoteEvent is AddNote that also returns the note's event.
func (s *Store) AddNoteEvent(ctx context.Context, n store.Note) (store.Note, store.Event, error) {
	if err := ctx.Err(); err != nil {
		return store.Note{}, store.Event{}, err
	}
	unlock, err := s.lock(ctx)
	if err != nil {
		return store.Note{}, store.Event{}, err
	}
	defer unlock()
	e, err := store.NewEvent(store.KindNote, store.NoteData{Kind: n.Kind, Text: n.Text})
	if err != nil {
		return store.Note{}, store.Event{}, fmt.Errorf("add note: %w", err)
	}
	var log taskLog
	if n.Task != "" {
		log, err = s.resolve(ctx, n.Task)
	} else {
		log, err = s.readOne(ctx, NotesRef)
	}
	if err != nil {
		return store.Note{}, store.Event{}, err
	}
	var id string
	var written store.Event
	out, _, err := s.appendLoop(ctx, log, func(cur []store.Event, _ int) ([]store.Event, string, error) {
		if store.HasOnce(cur, s.opts.Once) {
			return nil, "", store.ErrRepeat
		}
		stamped, err := s.opts.Stamp(cur, []store.Event{e})
		if err != nil {
			return nil, "", fmt.Errorf("add note: %w", err)
		}
		var d store.NoteData
		if err := json.Unmarshal(stamped[0].Data, &d); err != nil {
			return nil, "", fmt.Errorf("add note: %w", err)
		}
		id = stamped[0].ID
		written = stamped[0]
		short := d.Short
		if t, _, _, _ := store.Fold(cur); t.Short != "" {
			short = t.Short
		}
		return stamped, message(stamped, short), nil
	})
	if err != nil {
		return store.Note{}, store.Event{}, err
	}
	_, _, notes, _ := store.Fold(out)
	for _, note := range notes {
		if note.ID == id {
			return note, written, nil
		}
	}
	return store.Note{}, store.Event{}, errors.New("add note: the note did not fold")
}

// Notes lists notes, newest first. With no task it reads every log under
// refs/rig/ in one for-each-ref and one cat-file.
func (s *Store) Notes(ctx context.Context, f store.NoteFilter) ([]store.Note, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var all []store.Note
	if f.Task != "" {
		log, err := s.resolve(ctx, f.Task)
		if err != nil {
			return nil, err
		}
		_, _, all, _ = store.Fold(log.events)
	} else {
		logs, err := s.readAll(ctx, "refs/rig/", func(name string) bool {
			return name == NotesRef || strings.HasPrefix(name, TasksPrefix)
		})
		if err != nil {
			return nil, err
		}
		for _, l := range logs {
			_, _, notes, _ := store.Fold(l.events)
			all = append(all, notes...)
		}
	}
	return store.FilterNotes(all, f.Kind, f.Limit), nil
}

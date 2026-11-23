package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Created returns the first event of a new task log, stamped with seq 1, a
// fresh ULID (which becomes the task's id) and a short id that taken does not
// report in use.
func (o Options) Created(n NewTask, taken func(string) bool) (Event, error) {
	o = o.WithDefaults()
	if n.Slug == "" {
		return Event{}, errors.New("new task: slug is empty")
	}
	if n.Initial == "" {
		return Event{}, errors.New("new task: initial state is empty")
	}
	now := o.Now().UTC().Round(0)
	id := NewULID(now)
	d := CreatedData{
		Short: ShortID(TaskPrefix, id, taken), Slug: n.Slug, Title: n.Title,
		Body: n.Body, Branch: n.Branch, Fields: n.Fields, Workflow: n.Workflow,
		State: n.Initial, Parent: n.Parent,
	}
	if x := n.External; x != nil {
		if x.Kind == "" || x.Key == "" {
			return Event{}, errors.New("new task: external link has no kind or key")
		}
		d.External = &ExternalData{
			Kind: x.Kind, Host: strings.ToLower(x.Host), Project: x.Project, Key: x.Key, URL: x.URL,
			Type: x.Type, Owned: x.Owned, ParentKey: x.ParentKey,
		}
		if n.Snapshot != nil {
			at := x.SyncedAt
			if at.IsZero() {
				at = now
			}
			d.Snapshot = &TrackerSyncData{At: at, Rev: x.Rev, Values: n.Snapshot}
		}
	}
	if n.BranchTemplate != "" {
		key := ""
		if n.External != nil {
			key = n.External.Key
		}
		d.Branch = ExpandBranch(n.BranchTemplate, d.Short, n.Slug, id, key)
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return Event{}, fmt.Errorf("new task: %w", err)
	}
	return Event{V: Version, ID: id, Seq: 1, Time: now, Actor: o.Actor, Kind: KindCreated, Data: raw}, nil
}

// Stamp prepares events an Update callback returned for appending to log.
// It fills V, ID, Seq, Time and Actor; gives a new finding or note its id
// (the event's id), a short id unique within the log and, for a finding, the
// default key when it has none; and writes a finding-status event's finding
// as the full id. It refuses an unknown kind, a created event, and a payload
// that is malformed or names a finding that does not exist.
func (o Options) Stamp(log []Event, events []Event) ([]Event, error) {
	o = o.WithDefaults()
	task, findings, notes, _ := Fold(log)
	linked := task.External != nil
	findingShort := map[string]bool{}
	for _, f := range findings {
		findingShort[f.Short] = true
	}
	noteShort := map[string]bool{}
	for _, n := range notes {
		noteShort[n.Short] = true
	}
	seq := 0
	for _, e := range log {
		seq = max(seq, e.Seq)
	}

	now := o.Now().UTC().Round(0)
	out := make([]Event, 0, len(events))
	for _, e := range events {
		seq++
		e.V, e.ID, e.Seq, e.Time, e.Actor = Version, NewULID(now), seq, now, o.Actor
		var err error
		switch e.Kind {
		case KindTransition:
			err = check(e, func(d *TransitionData) error {
				if d.To == "" {
					return errors.New("no target state")
				}
				return nil
			})
		case KindField:
			err = check(e, func(d *FieldData) error {
				if d.Name == "" {
					return errors.New("no field name")
				}
				if _, ok := d.Value.(string); !ok && (d.Name == "title" || d.Name == "body") {
					return fmt.Errorf("%s must be a string", d.Name)
				}
				return nil
			})
		case KindCount:
			err = check(e, func(d *CountData) error {
				if d.Counter == "" {
					return errors.New("no counter name")
				}
				return nil
			})
		case KindParent:
			err = check(e, func(*ParentData) error { return nil })
		case KindExternal:
			e.Data, err = rewrite(e, func(d *ExternalData) error {
				if d.Kind == "" && d.Key == "" {
					if !linked {
						return errors.New("clearing a link: task is not linked")
					}
					linked = false
					return nil
				}
				if d.Kind == "" || d.Key == "" {
					return errors.New("no kind or key")
				}
				linked = true
				d.Host = strings.ToLower(d.Host)
				return nil
			})
		case KindTrackerSync:
			e.Data, err = rewrite(e, func(d *TrackerSyncData) error {
				if !linked {
					return errors.New("task is not linked")
				}
				if d.At.IsZero() {
					d.At = now
				}
				return nil
			})
		case KindFinding:
			e.Data, err = rewrite(e, func(d *FindingData) error {
				if d.Summary == "" {
					return errors.New("no summary")
				}
				if d.ID == "" {
					d.ID = e.ID
				}
				if d.Short == "" {
					d.Short = ShortID(FindingPrefix, d.ID, func(s string) bool { return findingShort[s] })
				}
				if d.Key == "" {
					d.Key = DefaultKey(d.Severity, d.Path, d.Summary)
				}
				findingShort[d.Short] = true
				findings = append(findings, Finding{ID: d.ID, Short: d.Short})
				return nil
			})
		case KindFindingStatus:
			e.Data, err = rewrite(e, func(d *FindingStatusData) error {
				if !validStatus(d.Status) {
					return fmt.Errorf("unknown status %q", d.Status)
				}
				i := findFinding(findings, d.Finding)
				if i < 0 {
					return fmt.Errorf("finding %q: %w", d.Finding, ErrNotFound)
				}
				d.Finding = findings[i].ID
				return nil
			})
		case KindNote:
			e.Data, err = rewrite(e, func(d *NoteData) error {
				if d.Text == "" {
					return errors.New("no text")
				}
				if d.ID == "" {
					d.ID = e.ID
				}
				if d.Short == "" {
					d.Short = ShortID(NotePrefix, d.ID, func(s string) bool { return noteShort[s] })
				}
				noteShort[d.Short] = true
				return nil
			})
		default:
			err = errors.New("cannot be appended")
		}
		if err != nil {
			return nil, fmt.Errorf("%s event: %w", e.Kind, err)
		}
		out = append(out, e)
	}
	return out, nil
}

// check decodes e's payload into a T and runs valid on it.
func check[T any](e Event, valid func(*T) error) error {
	_, err := rewrite(e, valid)
	return err
}

// rewrite decodes e's payload into a T, lets edit change it, and returns it
// encoded again.
func rewrite[T any](e Event, edit func(*T) error) (json.RawMessage, error) {
	var d T
	if err := json.Unmarshal(e.Data, &d); err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}
	if err := edit(&d); err != nil {
		return nil, err
	}
	return json.Marshal(d)
}

// SortTasks orders tasks newest first: by Created, then by id, descending.
func SortTasks(tasks []Task) {
	sort.SliceStable(tasks, func(i, j int) bool {
		if !tasks[i].Created.Equal(tasks[j].Created) {
			return tasks[i].Created.After(tasks[j].Created)
		}
		return tasks[i].ID > tasks[j].ID
	})
}

// SortNotes orders notes newest first: by Created, then by id, descending.
func SortNotes(notes []Note) {
	sort.SliceStable(notes, func(i, j int) bool {
		if !notes[i].Created.Equal(notes[j].Created) {
			return notes[i].Created.After(notes[j].Created)
		}
		return notes[i].ID > notes[j].ID
	})
}

// FilterNotes keeps the notes of kind (any kind when empty), newest first,
// and at most limit of them (all when limit is 0).
func FilterNotes(notes []Note, kind string, limit int) []Note {
	var out []Note
	for _, n := range notes {
		if kind == "" || n.Kind == kind {
			out = append(out, n)
		}
	}
	SortNotes(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

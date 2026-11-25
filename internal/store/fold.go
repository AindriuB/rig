package store

import (
	"encoding/json"
	"maps"
	"slices"
	"sort"
	"strings"
)

// SortEvents orders events by (Seq, ID), in place.
func SortEvents(events []Event) {
	sort.SliceStable(events, func(i, j int) bool { return eventLess(events[i], events[j]) })
}

func eventLess(a, b Event) bool {
	if a.Seq != b.Seq {
		return a.Seq < b.Seq
	}
	return a.ID < b.ID
}

// Fold turns a log into the task, findings and notes it records. Events are
// applied in (Seq, ID) order whatever order they are given in. An event whose
// V or Kind this rig does not know, or whose payload does not parse, is
// skipped and counted. A log with no created event (the repository's notes
// log) folds to a zero Task.
func Fold(events []Event) (Task, []Finding, []Note, int) {
	sorted := append([]Event(nil), events...)
	SortEvents(sorted)

	var (
		task     Task
		findings []Finding
		notes    []Note
		skipped  int
	)
	for _, e := range sorted {
		if e.V != Version || !knownKind(e.Kind) || !apply(&task, &findings, &notes, e) {
			skipped++
			continue
		}
		if e.Time.After(task.Updated) {
			task.Updated = e.Time
		}
	}
	task.OpenFindings = 0
	for _, f := range findings {
		if f.Status == StatusOpen {
			task.OpenFindings++
		}
	}
	if task.ID == "" {
		// A notes-only log describes no task.
		task = Task{}
	} else {
		task.Skipped = skipped
	}
	return task, findings, notes, skipped
}

// apply folds one event into the task, findings and notes. It returns false
// when the payload does not parse or names something that does not exist.
func apply(task *Task, findings *[]Finding, notes *[]Note, e Event) bool {
	switch e.Kind {
	case KindCreated:
		var d CreatedData
		if !decode(e, &d) || task.ID != "" {
			return false
		}
		*task = Task{
			ID: e.ID, Short: d.Short, Slug: d.Slug, Title: d.Title, Body: d.Body,
			Fields: map[string]any{}, Workflow: d.Workflow, State: d.State,
			Shas: map[string]string{}, Branch: d.Branch, Counters: map[string]int{},
			Created: e.Time, Updated: e.Time,
		}
		maps.Copy(task.Fields, d.Fields)
		task.Parent = d.Parent
		if d.External != nil {
			applyExternal(task, *d.External)
			if d.Snapshot != nil {
				applySync(task, *d.Snapshot)
			}
		}
	case KindParent:
		var d ParentData
		if !decode(e, &d) || task.ID == "" {
			return false
		}
		task.Parent = d.Task
	case KindExternal:
		var d ExternalData
		if !decode(e, &d) || task.ID == "" || (d.Kind == "") != (d.Key == "") {
			return false
		}
		if d.Kind == "" {
			if task.External == nil {
				return false
			}
			task.External = nil
			return true
		}
		applyExternal(task, d)
	case KindTrackerSync:
		var d TrackerSyncData
		if !decode(e, &d) || task.ID == "" || task.External == nil {
			return false
		}
		applySync(task, d)
	case KindTrackerReport:
		// A report changes nothing a task shows; `rig tracker report` reads
		// the log for it. It is applied only to count it as understood.
		var d TrackerReportData
		if !decode(e, &d) || task.ID == "" || !validReport(d) {
			return false
		}
	case KindTrackerPush:
		// An intent changes nothing a task shows; `rig tracker push` reads
		// the log for it. It is applied only to count it as understood.
		var d TrackerPushData
		if !decode(e, &d) || task.ID == "" || !validPush(d) {
			return false
		}
	case KindTransition:
		var d TransitionData
		if !decode(e, &d) || task.ID == "" || d.To == "" {
			return false
		}
		task.Previous, task.State = d.From, d.To
		task.StateSha, task.Reason = d.Sha, d.Reason
		if d.Sha != "" {
			task.Shas[d.To] = d.Sha
		}
		if d.PR != 0 {
			task.PR = d.PR
		}
	case KindField:
		var d FieldData
		if !decode(e, &d) || task.ID == "" || d.Name == "" {
			return false
		}
		switch d.Name {
		case "title", "body":
			s, ok := d.Value.(string)
			if !ok {
				return false
			}
			if d.Name == "title" {
				task.Title = s
			} else {
				task.Body = s
			}
		default:
			task.Fields[d.Name] = d.Value
		}
	case KindCount:
		var d CountData
		if !decode(e, &d) || task.ID == "" || d.Counter == "" {
			return false
		}
		task.Counters[d.Counter] += d.By
	case KindFinding:
		var d FindingData
		if !decode(e, &d) || task.ID == "" || d.ID == "" {
			return false
		}
		for _, f := range *findings {
			if f.ID == d.ID {
				return false
			}
		}
		*findings = append(*findings, Finding{
			ID: d.ID, Short: d.Short, Task: task.ID, Severity: d.Severity,
			Status: StatusOpen, Key: d.Key, Summary: d.Summary, Path: d.Path,
			Fields: d.Fields, Counters: maps.Clone(task.Counters),
			Created: e.Time, Updated: e.Time,
		})
	case KindFindingStatus:
		var d FindingStatusData
		if !decode(e, &d) || !validStatus(d.Status) {
			return false
		}
		i := findFinding(*findings, d.Finding)
		if i < 0 {
			return false
		}
		(*findings)[i].Status = d.Status
		(*findings)[i].Updated = e.Time
	case KindNote:
		var d NoteData
		if !decode(e, &d) || d.ID == "" {
			return false
		}
		*notes = append(*notes, Note{
			ID: d.ID, Short: d.Short, Task: task.ID, Kind: d.Kind, Text: d.Text,
			Created: e.Time,
		})
	default:
		return false
	}
	return true
}

// validReport reports whether d names an issue and at least one thing sent.
func validReport(d TrackerReportData) bool {
	return d.Kind != "" && d.Key != "" && (d.Status != nil || d.Comment != nil)
}

// validPush reports whether d names a marker and a tracker kind.
func validPush(d TrackerPushData) bool {
	return d.Marker != "" && d.Kind != ""
}

// applyExternal links task to d. A link written again keeps the snapshot
// already folded only when it is the same issue: the same kind, and the same
// host and key in any case. Otherwise the snapshot starts empty.
func applyExternal(task *Task, d ExternalData) {
	ext := &External{
		Kind: d.Kind, Host: d.Host, Project: d.Project, Key: d.Key, URL: d.URL,
		Type: d.Type, Owned: slices.Clone(d.Owned), ParentKey: d.ParentKey,
	}
	if old := task.External; old != nil && old.Kind == d.Kind &&
		strings.EqualFold(old.Host, d.Host) && strings.EqualFold(old.Key, d.Key) {
		ext.SyncedAt, ext.Rev, ext.Snapshot = old.SyncedAt, old.Rev, old.Snapshot
	}
	task.External = ext
}

// applySync records a tracker snapshot on task's link.
func applySync(task *Task, d TrackerSyncData) {
	task.External.SyncedAt, task.External.Rev = d.At, d.Rev
	task.External.Snapshot = maps.Clone(d.Values)
}

func decode(e Event, v any) bool {
	return json.Unmarshal(e.Data, v) == nil
}

// findFinding returns the index of the finding whose id or short id is ref,
// or -1.
func findFinding(findings []Finding, ref string) int {
	for i, f := range findings {
		if f.ID == ref || f.Short == ref {
			return i
		}
	}
	return -1
}

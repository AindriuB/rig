package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Version is the event format this rig writes and reads. A reader skips an
// event whose V it does not know.
const Version = 1

// Event kinds. A reader skips a kind it does not know.
const (
	KindCreated       = "created"
	KindTransition    = "transition"
	KindField         = "field"
	KindCount         = "count"
	KindFinding       = "finding"
	KindFindingStatus = "finding-status"
	KindNote          = "note"
	KindParent        = "parent"
	KindExternal      = "external"
	KindTrackerSync   = "tracker-sync"
	KindTrackerReport = "tracker-report"
	KindTrackerPush   = "tracker-push"
)

// knownKind reports whether this rig understands kind.
func knownKind(kind string) bool {
	switch kind {
	case KindCreated, KindTransition, KindField, KindCount,
		KindFinding, KindFindingStatus, KindNote,
		KindParent, KindExternal, KindTrackerSync, KindTrackerReport, KindTrackerPush:
		return true
	}
	return false
}

// Event is one line of a task log. Events are ordered by (Seq, ID); Seq counts
// from 1 within a log and ID is a ULID, unique across writers.
type Event struct {
	V     int             `json:"v"`
	ID    string          `json:"id"`
	Seq   int             `json:"seq"`
	Time  time.Time       `json:"time"`
	Actor string          `json:"actor"`
	Kind  string          `json:"kind"`
	Data  json.RawMessage `json:"data"`
}

// NewEvent returns an event of kind with data marshalled as its payload, for
// an Update callback to return. The store fills V, ID, Seq, Time and Actor.
func NewEvent(kind string, data any) (Event, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Event{}, fmt.Errorf("new %s event: %w", kind, err)
	}
	return Event{Kind: kind, Data: raw}, nil
}

// CreatedData is the payload of the first event in a task log. The task's id
// is the event's id.
type CreatedData struct {
	Short    string         `json:"short"`
	Slug     string         `json:"slug"`
	Title    string         `json:"title,omitempty"`
	Body     string         `json:"body,omitempty"`
	Branch   string         `json:"branch,omitempty"`
	Fields   map[string]any `json:"fields,omitempty"`
	Workflow WorkflowRef    `json:"workflow"`
	State    string         `json:"state"`
	// Parent, External and Snapshot are folded as the parent, external and
	// tracker-sync events would be.
	Parent   string           `json:"parent,omitempty"`
	External *ExternalData    `json:"external,omitempty"`
	Snapshot *TrackerSyncData `json:"snapshot,omitempty"`
}

// ParentData sets a task's parent: a task id, or "" to clear it.
type ParentData struct {
	Task string `json:"task"`
}

// ExternalData links a task to an issue in a tracker. Owned lists the rig
// fields the tracker owns; Type is the tracker's own level label; ParentKey is
// the issue's parent when that parent has no task yet. An external event whose
// Kind and Key are both empty clears the task's link and drops its snapshot;
// Stamp refuses it on a task that is not linked, and refuses an event with only
// one of Kind and Key.
type ExternalData struct {
	Kind      string   `json:"kind"`
	Host      string   `json:"host,omitempty"`
	Project   string   `json:"project,omitempty"`
	Key       string   `json:"key"`
	URL       string   `json:"url,omitempty"`
	Type      string   `json:"type,omitempty"`
	Owned     []string `json:"owned,omitempty"`
	ParentKey string   `json:"parentKey,omitempty"`
}

// TrackerSyncData is a snapshot of the owned fields as the tracker last gave
// them, and the tracker's revision.
type TrackerSyncData struct {
	At     time.Time      `json:"at"`
	Rev    string         `json:"rev,omitempty"`
	Values map[string]any `json:"values,omitempty"`
}

// TrackerReportData records what `rig tracker report` sent to the issue Key of
// a tracker of Kind. Status and Comment are set only for a part that was sent;
// the comment is recorded by a digest of its body, never the body itself.
type TrackerReportData struct {
	Kind    string         `json:"kind"`
	Key     string         `json:"key"`
	Status  *ReportStatus  `json:"status,omitempty"`
	Comment *ReportComment `json:"comment,omitempty"`
	At      time.Time      `json:"at"`
}

// ReportStatus is a status change a report sent.
type ReportStatus struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Asked is the status name the report requested; To is the one the
	// tracker reached, which may be spelled differently (GitLab "open" ->
	// "opened").
	Asked string `json:"asked,omitempty"`
}

// ReportComment is a comment a report sent: its template kind (pr, state or
// summary), the id the tracker gave it, and the hex SHA-256 of its body.
type ReportComment struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

// TrackerPushData is the intent `rig tracker push` writes before it creates an
// issue: the marker label the issue will carry, and the tracker it goes to. A
// task with an intent and no link may already have an issue on the tracker; a
// retry finds it by the marker.
type TrackerPushData struct {
	Marker  string `json:"marker"`
	Kind    string `json:"kind"`
	Host    string `json:"host,omitempty"`
	Project string `json:"project,omitempty"`
}

// ErrPushIntent is the error Options.Stamp returns for a tracker-push intent
// when the log already holds one for the same tracker.
var ErrPushIntent = errors.New("a tracker-push intent for this tracker is already recorded")

// PushIntentFor reports whether log holds a tracker-push intent for the same
// tracker as d: the same kind, and the same host and project in any case.
func PushIntentFor(log []Event, d TrackerPushData) bool {
	for _, e := range log {
		if e.Kind != KindTrackerPush || e.V != Version {
			continue
		}
		var x TrackerPushData
		if json.Unmarshal(e.Data, &x) != nil || !validPush(x) {
			continue
		}
		if x.Kind == d.Kind && strings.EqualFold(x.Host, d.Host) && strings.EqualFold(x.Project, d.Project) {
			return true
		}
	}
	return false
}

// TransitionData moves a task from one state to another. Sha, PR and Reason
// are recorded when the move carries them.
type TransitionData struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Sha    string `json:"sha,omitempty"`
	PR     int    `json:"pr,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// FieldData sets one field. The names "title" and "body" set the task's
// Title and Body; every other name goes into Fields.
type FieldData struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
}

// CountData adds By to a counter.
type CountData struct {
	Counter string `json:"counter"`
	By      int    `json:"by"`
}

// FindingData records a new finding. The store fills ID, Short and Key when
// they are empty.
type FindingData struct {
	ID       string         `json:"id"`
	Short    string         `json:"short"`
	Severity string         `json:"severity"`
	Key      string         `json:"key"`
	Summary  string         `json:"summary"`
	Path     string         `json:"path,omitempty"`
	Fields   map[string]any `json:"fields,omitempty"`
}

// FindingStatusData sets a finding's status. Finding is the finding's id; the
// store accepts its short id and writes the full one.
type FindingStatusData struct {
	Finding string `json:"finding"`
	Status  string `json:"status"`
}

// NoteData records a note. The store fills ID and Short when they are empty.
type NoteData struct {
	ID    string `json:"id"`
	Short string `json:"short"`
	Kind  string `json:"kind"`
	Text  string `json:"text"`
}

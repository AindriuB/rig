package workflow

import (
	"bytes"
	"encoding/json"
	"slices"
	"sort"
	"time"

	"github.com/AindriuB/rig/internal/store"
)

// TaskView is a task as rig shows it: to command gates and hooks on stdin,
// and in the commands' --json output. Its fields follow the data model's
// Task: State is the effective state and RecordedState the one the log alone
// gives.
type TaskView struct {
	ID            string            `json:"id"`
	Short         string            `json:"short"`
	Slug          string            `json:"slug"`
	Title         string            `json:"title"`
	Body          string            `json:"body"`
	Fields        map[string]any    `json:"fields"`
	Workflow      store.WorkflowRef `json:"workflow"`
	State         string            `json:"state"`
	RecordedState string            `json:"recordedState"`
	StateSha      string            `json:"stateSha"`
	Shas          map[string]string `json:"shas"`
	Branch        string            `json:"branch"`
	Parent        string            `json:"parent,omitempty"`
	External      *ExternalView     `json:"external,omitempty"`
	PR            int               `json:"pr,omitempty"`
	HeadSha       string            `json:"headSha,omitempty"`
	Reason        string            `json:"reason,omitempty"`
	Counters      map[string]int    `json:"counters"`
	OpenFindings  int               `json:"openFindings"`
	Created       time.Time         `json:"created"`
	Updated       time.Time         `json:"updated"`
}

// ExternalView is a task's link to a tracker issue as rig shows it. Drift
// names the owned fields whose local value differs from the last snapshot;
// the snapshot itself is not shown.
type ExternalView struct {
	Kind     string    `json:"kind"`
	Host     string    `json:"host,omitempty"`
	Project  string    `json:"project,omitempty"`
	Key      string    `json:"key"`
	URL      string    `json:"url,omitempty"`
	Type     string    `json:"type,omitempty"`
	Owned    []string  `json:"owned,omitempty"`
	SyncedAt time.Time `json:"syncedAt,omitzero"`
	Rev      string    `json:"rev,omitempty"`
	Drift    []string  `json:"drift"`
}

// drift returns the sorted names of the owned fields whose current value in t
// differs from the snapshot. A field present on one side only counts.
func drift(t store.Task) []string {
	out := []string{}
	ext := t.External
	for _, name := range ext.Owned {
		var cur any
		var have bool
		switch name {
		case "title":
			cur, have = t.Title, true
		case "body":
			cur, have = t.Body, true
		default:
			cur, have = t.Fields[name]
		}
		snap, sok := ext.Snapshot[name]
		if have != sok || (have && !sameValue(cur, snap)) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// sameValue compares two field values by their JSON form, so a []string and
// the []any a snapshot read back from the log holds are equal.
func sameValue(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

// View returns the task as rig shows it, given its branch head now: headSha
// "" means the branch does not exist. State is Effective(t, headSha); the
// branch is the task's, or else the workflow's template applied to it. Nil
// maps become empty ones, so they read as {} rather than null.
func (w *Workflow) View(t store.Task, headSha string) TaskView {
	branch := t.Branch
	if branch == "" {
		branch = w.BranchFor(t)
	}
	v := TaskView{
		ID:            t.ID,
		Short:         t.Short,
		Slug:          t.Slug,
		Title:         t.Title,
		Body:          t.Body,
		Fields:        t.Fields,
		Workflow:      t.Workflow,
		State:         w.Effective(t, headSha),
		RecordedState: t.State,
		StateSha:      t.StateSha,
		Shas:          t.Shas,
		Branch:        branch,
		PR:            t.PR,
		HeadSha:       headSha,
		Reason:        t.Reason,
		Counters:      t.Counters,
		OpenFindings:  t.OpenFindings,
		Created:       t.Created,
		Updated:       t.Updated,
	}
	v.Parent = t.Parent
	if e := t.External; e != nil {
		v.External = &ExternalView{
			Kind: e.Kind, Host: e.Host, Project: e.Project, Key: e.Key, URL: e.URL,
			Type: e.Type, Owned: e.Owned, SyncedAt: e.SyncedAt, Rev: e.Rev,
			Drift: drift(t),
		}
	}
	if v.Fields == nil {
		v.Fields = map[string]any{}
	}
	if v.Shas == nil {
		v.Shas = map[string]string{}
	}
	if v.Counters == nil {
		v.Counters = map[string]int{}
	}
	return v
}

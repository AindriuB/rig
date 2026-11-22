package workflow

import (
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
	PR            int               `json:"pr,omitempty"`
	HeadSha       string            `json:"headSha,omitempty"`
	Reason        string            `json:"reason,omitempty"`
	Counters      map[string]int    `json:"counters"`
	OpenFindings  int               `json:"openFindings"`
	Created       time.Time         `json:"created"`
	Updated       time.Time         `json:"updated"`
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

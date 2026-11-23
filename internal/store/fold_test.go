package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)

// ev builds a version-1 event whose id sorts by seq.
func ev(t *testing.T, seq int, kind string, data any) Event {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return Event{
		V: 1, ID: fmt.Sprintf("01JD%022d", seq), Seq: seq,
		Time: t0.Add(time.Duration(seq) * time.Minute), Actor: "andrew",
		Kind: kind, Data: raw,
	}
}

func sampleLog(t *testing.T) []Event {
	return []Event{
		ev(t, 1, KindCreated, CreatedData{
			Short: "t-0001", Slug: "add-parser", Title: "Add parser", Body: "body",
			Branch: "task/t-0001-add-parser", Fields: map[string]any{"size": "s"},
			Workflow: WorkflowRef{Name: "basic", Version: 1}, State: "todo",
		}),
		ev(t, 2, KindTransition, TransitionData{From: "todo", To: "doing"}),
		ev(t, 3, KindField, FieldData{Name: "owner", Value: "andrew"}),
		ev(t, 4, KindField, FieldData{Name: "title", Value: "Add a parser"}),
		ev(t, 5, KindCount, CountData{Counter: "round", By: 1}),
		ev(t, 6, KindFinding, FindingData{
			ID: "F1", Short: "f-0001", Severity: "high", Key: "high|a.go|x",
			Summary: "x", Path: "a.go:3",
		}),
		ev(t, 7, KindFinding, FindingData{ID: "F2", Short: "f-0002", Severity: "low", Summary: "y"}),
		ev(t, 8, KindTransition, TransitionData{From: "doing", To: "review", Sha: "abc", PR: 12}),
		ev(t, 9, KindFindingStatus, FindingStatusData{Finding: "F2", Status: StatusFixed}),
		ev(t, 10, KindCount, CountData{Counter: "round", By: 1}),
		ev(t, 11, KindTransition, TransitionData{From: "review", To: "blocked", Reason: "waiting"}),
		ev(t, 12, KindNote, NoteData{ID: "N1", Short: "n-0001", Kind: "decision", Text: "keep it"}),
	}
}

func TestFoldBuildsTask(t *testing.T) {
	log := sampleLog(t)
	// Fold orders by (seq, id) whatever order the slice is in.
	log[2], log[9] = log[9], log[2]
	task, findings, notes, skipped := Fold(log)
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	want := Task{
		ID: "01JD0000000000000000000001", Short: "t-0001", Slug: "add-parser",
		Title: "Add a parser", Body: "body",
		Fields:   map[string]any{"size": "s", "owner": "andrew"},
		Workflow: WorkflowRef{Name: "basic", Version: 1},
		State:    "blocked", Previous: "review", StateSha: "",
		Shas:   map[string]string{"review": "abc"},
		Branch: "task/t-0001-add-parser", PR: 12, Reason: "waiting",
		Counters: map[string]int{"round": 2}, OpenFindings: 1,
		Created: t0.Add(time.Minute), Updated: t0.Add(12 * time.Minute),
	}
	if !reflect.DeepEqual(task, want) {
		t.Fatalf("task =\n%+v\nwant\n%+v", task, want)
	}
	if len(findings) != 2 {
		t.Fatalf("findings = %+v", findings)
	}
	f1, f2 := findings[0], findings[1]
	if f1.ID != "F1" || f1.Short != "f-0001" || f1.Task != task.ID || f1.Status != StatusOpen ||
		f1.Severity != "high" || f1.Key != "high|a.go|x" || f1.Path != "a.go:3" ||
		!reflect.DeepEqual(f1.Counters, map[string]int{"round": 1}) ||
		!f1.Created.Equal(t0.Add(6*time.Minute)) || !f1.Updated.Equal(f1.Created) {
		t.Fatalf("finding 1 = %+v", f1)
	}
	if f2.Status != StatusFixed || !f2.Updated.Equal(t0.Add(9*time.Minute)) {
		t.Fatalf("finding 2 = %+v", f2)
	}
	if len(notes) != 1 || notes[0].ID != "N1" || notes[0].Task != task.ID ||
		notes[0].Kind != "decision" || notes[0].Text != "keep it" ||
		!notes[0].Created.Equal(t0.Add(12*time.Minute)) {
		t.Fatalf("notes = %+v", notes)
	}
}

func TestFoldStateShaFollowsCurrentState(t *testing.T) {
	log := sampleLog(t)[:8] // ends with the transition into review at sha abc
	task, _, _, _ := Fold(log)
	if task.State != "review" || task.Previous != "doing" || task.StateSha != "abc" {
		t.Fatalf("state %q previous %q stateSha %q", task.State, task.Previous, task.StateSha)
	}
}

func TestFoldSkipsUnknownVersionAndKind(t *testing.T) {
	log := sampleLog(t)
	future := ev(t, 13, KindField, FieldData{Name: "title", Value: "from the future"})
	future.V = 2
	odd := ev(t, 14, "claim", map[string]string{"who": "x"})
	bad := ev(t, 15, KindCount, nil)
	bad.Data = []byte(`{"counter":`)
	ghost := ev(t, 16, KindFindingStatus, FindingStatusData{Finding: "nope", Status: StatusFixed})
	task, findings, _, skipped := Fold(append(log, future, odd, bad, ghost))
	if skipped != 4 {
		t.Fatalf("skipped = %d, want 4", skipped)
	}
	want, wantFindings, _, _ := Fold(log)
	if !reflect.DeepEqual(task, want) || !reflect.DeepEqual(findings, wantFindings) {
		t.Fatalf("skipped events changed the fold:\n%+v\nwant\n%+v", task, want)
	}
}

func TestFoldEmptyLog(t *testing.T) {
	task, findings, notes, skipped := Fold(nil)
	if task.ID != "" || findings != nil || notes != nil || skipped != 0 {
		t.Fatalf("Fold(nil) = %+v %v %v %d", task, findings, notes, skipped)
	}
}

// A link written again keeps its snapshot only when it is the same issue
// (kind exactly; host and key in any case); any other link starts empty.
func TestFoldRelinkSnapshot(t *testing.T) {
	base := ExternalData{Kind: "jira", Host: "j.example", Key: "AB-7"}
	cases := []struct {
		name string
		to   ExternalData
		keep bool
	}{
		{"same issue", base, true},
		{"same issue, other case", ExternalData{Kind: "jira", Host: "J.Example", Key: "ab-7"}, true},
		{"other key", ExternalData{Kind: "jira", Host: "j.example", Key: "AB-8"}, false},
		{"other host", ExternalData{Kind: "jira", Host: "k.example", Key: "AB-7"}, false},
		{"other kind", ExternalData{Kind: "github", Host: "j.example", Key: "AB-7"}, false},
		{"kind in other case", ExternalData{Kind: "Jira", Host: "j.example", Key: "AB-7"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := append(sampleLog(t),
				ev(t, 13, KindExternal, base),
				ev(t, 14, KindTrackerSync, TrackerSyncData{At: t0, Rev: "r1", Values: map[string]any{"title": "Old"}}),
				ev(t, 15, KindExternal, tc.to))
			task, _, _, skipped := Fold(log)
			if skipped != 0 || task.External == nil {
				t.Fatalf("skipped %d, external %+v", skipped, task.External)
			}
			x := task.External
			if tc.keep {
				if x.Rev != "r1" || x.Snapshot["title"] != "Old" || x.SyncedAt.IsZero() {
					t.Fatalf("snapshot lost: %+v", x)
				}
			} else if x.Rev != "" || x.Snapshot != nil || !x.SyncedAt.IsZero() {
				t.Fatalf("snapshot carried to another issue: %+v", x)
			}
		})
	}
}

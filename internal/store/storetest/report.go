package storetest

import (
	"context"
	"testing"

	"github.com/AindriuB/rig/internal/store"
)

// A tracker-report event is accepted on a linked task with a status or a
// comment, and changes nothing the task shows. It is refused on an unlinked
// task and when it carries nothing.
func testTrackerReport(t *testing.T, s store.Store) {
	ctx := context.Background()
	send := func(ref string, d store.TrackerReportData) error {
		_, err := s.Update(ctx, ref, func(store.Task, []store.Finding) ([]store.Event, error) {
			e, err := store.NewEvent(store.KindTrackerReport, d)
			return []store.Event{e}, err
		})
		return err
	}
	good := store.TrackerReportData{
		Kind: "jira", Key: "AB-7",
		Status:  &store.ReportStatus{From: "To Do", To: "In Review"},
		Comment: &store.ReportComment{Kind: "state", ID: "9", Digest: "abc"},
	}

	plain := create(t, s, "plain")
	if err := send(plain.ID, good); err == nil {
		t.Fatal("tracker-report on an unlinked task accepted")
	}

	linked, err := s.CreateTask(ctx, linkedTask("linked", "AB-7"))
	if err != nil {
		t.Fatal(err)
	}
	if err := send(linked.ID, store.TrackerReportData{Kind: "jira", Key: "AB-7"}); err == nil {
		t.Fatal("tracker-report that sent nothing accepted")
	}
	before := len(events(t, s, linked.ID))
	if err := send(linked.ID, good); err != nil {
		t.Fatalf("tracker-report refused: %v", err)
	}
	evs := events(t, s, linked.ID)
	if len(evs) != before+1 || evs[len(evs)-1].Kind != store.KindTrackerReport {
		t.Fatalf("events = %d, want %d ending in tracker-report", len(evs), before+1)
	}
	if _, _, _, skipped := store.Fold(evs); skipped != 0 {
		t.Fatalf("Fold skipped %d events", skipped)
	}
	after, err := s.Task(ctx, linked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.External == nil || after.State != linked.State {
		t.Fatalf("task changed: %+v", after)
	}
}

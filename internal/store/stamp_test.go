package store

import (
	"encoding/json"
	"testing"
	"time"
)

// AC12: a well-formed tracker-push event is accepted on a task that is not
// linked; an empty marker, or a payload that is not a TrackerPushData, is
// refused.
func TestStampTrackerPush(t *testing.T) {
	log := sampleLog(t)
	good := TrackerPushData{Marker: "rig-t-0001", Kind: "jira", Host: "j.example", Project: "AB"}
	cases := []struct {
		name string
		data json.RawMessage
		ok   bool
	}{
		{"well formed", mustJSON(t, good), true},
		{"no marker", mustJSON(t, TrackerPushData{Kind: "jira"}), false},
		{"no kind", mustJSON(t, TrackerPushData{Marker: "rig-t-0001"}), false},
		{"not a payload", json.RawMessage(`["rig-t-0001"]`), false},
		{"torn", json.RawMessage(`{"marker":`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Options{Now: func() time.Time { return t0 }}.Stamp(log, []Event{{Kind: KindTrackerPush, Data: tc.data}})
			if tc.ok != (err == nil) {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
			if tc.ok && (len(out) != 1 || out[0].Kind != KindTrackerPush || out[0].Seq != len(log)+1) {
				t.Fatalf("stamped %+v", out)
			}
		})
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Every Create attempt records its own intent, so a second intent for the
// same tracker is accepted and both are kept.
func TestStampTrackerPushRecordsSequentialIntents(t *testing.T) {
	o := Options{Now: func() time.Time { return t0 }}
	d := TrackerPushData{Marker: "rig-t-0001", Kind: "jira", Host: "j.example", Project: "AB"}
	log := sampleLog(t)
	for i := 0; i < 2; i++ {
		out, err := o.Stamp(log, []Event{{Kind: KindTrackerPush, Data: mustJSON(t, d)}})
		if err != nil {
			t.Fatalf("intent %d: %v", i+1, err)
		}
		log = append(log, out...)
	}
	n := 0
	for _, e := range log {
		if e.Kind == KindTrackerPush {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d intents recorded, want 2", n)
	}
}

package store

import (
	"encoding/json"
	"strings"
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

// AC2: every event a write with Once stamps carries it, and the field is absent
// from the JSON line when there is none.
func TestStampOnce(t *testing.T) {
	log := sampleLog(t)
	two := []Event{{Kind: KindCount, Data: mustJSON(t, CountData{Counter: "n", By: 1})}, {Kind: KindCount, Data: mustJSON(t, CountData{Counter: "n", By: 1})}}
	out, err := Options{Now: func() time.Time { return t0 }, Once: "k"}.Stamp(log, two)
	if err != nil || len(out) != 2 {
		t.Fatalf("stamp: %v", err)
	}
	for _, e := range out {
		if e.Once != "k" {
			t.Fatalf("event %d once = %q", e.Seq, e.Once)
		}
	}
	plain, err := Options{Now: func() time.Time { return t0 }}.Stamp(log, two[:1])
	if err != nil {
		t.Fatal(err)
	}
	line, _ := json.Marshal(plain[0])
	if strings.Contains(string(line), "once") {
		t.Fatalf("once on a line with none set: %s", line)
	}
}

package workflow

import (
	"encoding/json"
	"testing"

	"github.com/AindriuB/rig/internal/store"
)

func TestEffective(t *testing.T) {
	w := fixture(t)
	task := func(state string, shas map[string]string) store.Task {
		return store.Task{State: state, Shas: shas}
	}
	cases := []struct {
		name string
		t    store.Task
		head string
		want string
	}{
		{"bound, head unmoved", task("verified", map[string]string{"verified": "aaa"}), "aaa", "verified"},
		{"bound, head moved", task("verified", map[string]string{"verified": "aaa"}), "bbb", "implementing"},
		{"bound, head unknown", task("verified", map[string]string{"verified": "aaa"}), "", "verified"},
		{"bound, no sha recorded", task("ready", map[string]string{}), "bbb", "implementing"},
		{"bound, another state's sha", task("pr_open", map[string]string{"verified": "aaa"}), "aaa", "implementing"},
		{"not bound", task("reviewed", map[string]string{"reviewed": "aaa"}), "bbb", "reviewed"},
		{"final", task("merged", map[string]string{"merged": "aaa"}), "bbb", "merged"},
		{"unknown state", task("gone", nil), "bbb", "gone"},
	}
	for _, c := range cases {
		if got := w.Effective(c.t, c.head); got != c.want {
			t.Errorf("%s: Effective = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestBranchFor(t *testing.T) {
	w := fixture(t)
	task := store.Task{ID: "01ABC", Short: "t-1a", Slug: "fix-it"}
	if got := w.BranchFor(task); got != "task/t-1a-fix-it" {
		t.Fatalf("BranchFor = %q", got)
	}
	w.Branch = "{id}/{short}"
	if got := w.BranchFor(task); got != "01ABC/t-1a" {
		t.Fatalf("BranchFor = %q", got)
	}
	w.Branch = ""
	if got := w.BranchFor(task); got != "" {
		t.Fatalf("BranchFor with no template = %q", got)
	}
}

// only returns the one transition from → to, failing the test otherwise.
func only(t *testing.T, w *Workflow, from, to string) Transition {
	t.Helper()
	for _, tr := range w.Transitions(from) {
		if tr.To == to {
			return tr
		}
	}
	t.Fatalf("no transition %s → %s", from, to)
	return Transition{}
}

func transitionOf(t *testing.T, events []store.Event) store.TransitionData {
	t.Helper()
	if len(events) == 0 || events[0].Kind != store.KindTransition {
		t.Fatalf("events = %v", events)
	}
	var d store.TransitionData
	if err := json.Unmarshal(events[0].Data, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestEvents(t *testing.T) {
	w := fixture(t)
	task := store.Task{State: "verified", Shas: map[string]string{"verified": "s2", "pr_open": "s1", "ready": "s0"}}

	// Takes sha.
	ev, err := w.Events(task, only(t, w, "reviewed", "verified"), "verified", Args{Sha: "s3"})
	if err != nil || transitionOf(t, ev) != (store.TransitionData{From: "reviewed", To: "verified", Sha: "s3"}) {
		t.Fatalf("takes sha: %v %+v", err, ev)
	}
	if _, err := w.Events(task, only(t, w, "reviewed", "verified"), "verified", Args{}); err == nil {
		t.Fatal("takes sha without one: no error")
	}
	// Takes pr, and carries verified's sha into pr_open.
	ev, err = w.Events(task, only(t, w, "verified", "pr_open"), "pr_open", Args{PR: 7})
	if err != nil || transitionOf(t, ev) != (store.TransitionData{From: "verified", To: "pr_open", Sha: "s2", PR: 7}) {
		t.Fatalf("takes pr: %v %+v", err, transitionOf(t, ev))
	}
	if _, err := w.Events(task, only(t, w, "verified", "pr_open"), "pr_open", Args{}); err == nil {
		t.Fatal("takes pr without one: no error")
	}
	// Takes reason.
	if _, err := w.Events(task, only(t, w, "verified", "stopped"), "stopped", Args{Reason: " "}); err == nil {
		t.Fatal("takes reason without one: no error")
	}
	// Back from stopped to a sha-bound state: no sha, so ready keeps s0.
	ev, err = w.Events(task, only(t, w, "stopped", "$previous"), "ready", Args{})
	if err != nil || transitionOf(t, ev) != (store.TransitionData{From: "stopped", To: "ready"}) {
		t.Fatalf("$previous: %v %+v", err, transitionOf(t, ev))
	}
	if _, err := w.Events(task, only(t, w, "stopped", "$previous"), "$previous", Args{}); err == nil {
		t.Fatal("unresolved $previous: no error")
	}
	// Counts.
	ev, err = w.Events(task, w.Transitions("reviewed")[1], "implementing", Args{})
	if err != nil || len(ev) != 2 || ev[1].Kind != store.KindCount || string(ev[1].Data) != `{"counter":"rounds","by":1}` {
		t.Fatalf("count: %v %v", err, ev)
	}
}

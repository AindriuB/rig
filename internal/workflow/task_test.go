package workflow

import (
	"encoding/json"
	"strings"
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
	w.Branch = "rig/{key}-{slug}"
	if got := w.BranchFor(task); got != "rig/t-1a-fix-it" {
		t.Fatalf("BranchFor {key} unlinked = %q, want the short id", got)
	}
	// Linking a task later must not rename its derived branch: only a branch
	// stamped at intake carries the issue key.
	linked := task
	linked.External = &store.External{Kind: "jira", Key: "PROJ-12"}
	if got := w.BranchFor(linked); got != "rig/t-1a-fix-it" {
		t.Fatalf("BranchFor {key} after link = %q, want the unlinked name", got)
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
	shas := map[string]string{"verified": "s2", "pr_open": "s1", "ready": "s0"}
	in := func(state string) store.Task { return store.Task{State: state, Shas: shas} }

	// Takes sha.
	ev, err := w.Events(in("reviewed"), "", only(t, w, "reviewed", "verified"), "verified", Args{Sha: "s3"})
	if err != nil || transitionOf(t, ev) != (store.TransitionData{From: "reviewed", To: "verified", Sha: "s3"}) {
		t.Fatalf("takes sha: %v %+v", err, ev)
	}
	if _, err := w.Events(in("reviewed"), "", only(t, w, "reviewed", "verified"), "verified", Args{}); err == nil {
		t.Fatal("takes sha without one: no error")
	}
	// Takes pr, and carries verified's sha into pr_open.
	ev, err = w.Events(in("verified"), "s2", only(t, w, "verified", "pr_open"), "pr_open", Args{PR: 7})
	if err != nil || transitionOf(t, ev) != (store.TransitionData{From: "verified", To: "pr_open", Sha: "s2", PR: 7}) {
		t.Fatalf("takes pr: %v %+v", err, transitionOf(t, ev))
	}
	if _, err := w.Events(in("verified"), "s2", only(t, w, "verified", "pr_open"), "pr_open", Args{}); err == nil {
		t.Fatal("takes pr without one: no error")
	}
	// Takes reason.
	if _, err := w.Events(in("verified"), "", only(t, w, "verified", "stopped"), "stopped", Args{Reason: " "}); err == nil {
		t.Fatal("takes reason without one: no error")
	}
	// Back from stopped to a sha-bound state: no sha, so ready keeps s0.
	stopped := in("stopped")
	stopped.Previous = "ready"
	ev, err = w.Events(stopped, "", only(t, w, "stopped", "$previous"), "ready", Args{})
	if err != nil || transitionOf(t, ev) != (store.TransitionData{From: "stopped", To: "ready"}) {
		t.Fatalf("$previous: %v %+v", err, transitionOf(t, ev))
	}
	if _, err := w.Events(stopped, "", only(t, w, "stopped", "$previous"), "$previous", Args{}); err == nil {
		t.Fatal("unresolved $previous: no error")
	}
	// Counts.
	ev, err = w.Events(in("reviewed"), "", only(t, w, "reviewed", "implementing"), "implementing", Args{})
	if err != nil || len(ev) != 2 || ev[1].Kind != store.KindCount || string(ev[1].Data) != `{"counter":"rounds","by":1}` {
		t.Fatalf("count: %v %v", err, ev)
	}
	// The effective state is the current one: ready at s0 with the head at
	// s9 is implementing.
	ev, err = w.Events(in("ready"), "s9", only(t, w, "implementing", "reviewed"), "reviewed", Args{})
	if err != nil || transitionOf(t, ev) != (store.TransitionData{From: "implementing", To: "reviewed"}) {
		t.Fatalf("from the effective state: %v %+v", err, ev)
	}
	// A whole from-list transition, as the file has it, leaves its states.
	if _, err := w.Events(in("pr_open"), "s1", w.AllTransitions[8], "implementing", Args{}); err != nil {
		t.Fatalf("from-list transition: %v", err)
	}
}

func TestEventsFailsClosed(t *testing.T) {
	w := fixture(t)
	reviewed := store.Task{State: "reviewed"}
	stopped := store.Task{State: "stopped", Previous: "ready"}
	star := w.AllTransitions[9] // "*" → stopped
	cases := []struct {
		name string
		t    store.Task
		head string
		tr   Transition
		to   string
		args Args
		want string
	}{
		{"to is not tr.To", reviewed, "", only(t, w, "reviewed", "verified"), "merged", Args{Sha: "x"}, "not the transition's target"},
		{"to is not the previous state", stopped, "", only(t, w, "stopped", "$previous"), "verified", Args{}, `not the transition's target "ready"`},
		{"no previous state", store.Task{State: "stopped"}, "", only(t, w, "stopped", "$previous"), "ready", Args{}, "no previous state"},
		{"from is another state", reviewed, "", only(t, w, "ready", "merged"), "merged", Args{Sha: "x"}, `does not leave the task's state "reviewed"`},
		{"recorded, not effective", store.Task{State: "ready", Shas: map[string]string{"ready": "s0"}}, "s9", only(t, w, "ready", "merged"), "merged", Args{Sha: "s9"}, `"implementing"`},
		{"* from a final state", store.Task{State: "merged"}, "", star, "stopped", Args{Reason: "r"}, `"merged"`},
		{"* to itself", store.Task{State: "stopped"}, "", star, "stopped", Args{Reason: "r"}, `"stopped"`},
		{"from-list missing the state", reviewed, "", w.AllTransitions[8], "implementing", Args{}, `"reviewed"`},
		{"no from", reviewed, "", Transition{To: "verified"}, "verified", Args{}, "does not leave"},
	}
	for _, c := range cases {
		if _, err := w.Events(c.t, c.head, c.tr, c.to, c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	// "*" does leave a non-final state.
	if _, err := w.Events(reviewed, "", star, "stopped", Args{Reason: "r"}); err != nil {
		t.Fatalf("* from reviewed: %v", err)
	}
}

func TestView(t *testing.T) {
	w := fixture(t)
	task := store.Task{
		ID: "01X", Short: "t-1", Slug: "s", Title: "T", State: "ready", StateSha: "s1",
		Shas: map[string]string{"ready": "s1"}, PR: 4, Workflow: store.WorkflowRef{Name: "review-loop", Version: 1},
	}
	v := w.View(task, "s2")
	if v.State != "implementing" || v.RecordedState != "ready" || v.HeadSha != "s2" || v.StateSha != "s1" || v.Branch != "task/t-1-s" {
		t.Fatalf("view = %+v", v)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(b, &keys); err != nil {
		t.Fatal(err)
	}
	want := "body branch counters created fields headSha id openFindings pr recordedState shas short slug state stateSha title updated workflow"
	if got := strings.Join(sortedKeys(keys), " "); got != want {
		t.Fatalf("keys = %s\nwant   %s", got, want)
	}
	if !strings.Contains(string(b), `"fields":{}`) || !strings.Contains(string(b), `"counters":{}`) {
		t.Fatalf("nil maps: %s", b)
	}
	// pr, headSha and reason are left out when unset; reason shows when set.
	task.PR, task.Reason = 0, "why"
	b, _ = json.Marshal(w.View(task, ""))
	if strings.Contains(string(b), `"pr"`) || strings.Contains(string(b), `"headSha"`) || !strings.Contains(string(b), `"reason":"why"`) {
		t.Fatalf("omitempty: %s", b)
	}
}

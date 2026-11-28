package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AindriuB/rig/internal/hookstate"
)

// parkedEnv is a repository with two tasks, each with one parked event (its
// first note), and the second note of each held back behind it.
type parkedEnv struct {
	*deliverEnv
	a, b       string
	idsA, idsB []string
}

func newParkedEnv(t *testing.T) *parkedEnv {
	e := &parkedEnv{deliverEnv: newDeliverEnv(t, fromStart(queuedHook))}
	e.a, e.b = e.newTask("a"), e.newTask("b")
	e.note(e.a)
	e.note(e.a)
	e.note(e.b)
	e.note(e.b)
	e.idsA, e.idsB = e.ids(e.a), e.ids(e.b)
	bad := map[string]bool{e.idsA[1]: true, e.idsB[1]: true}
	e.fake.fn = func(c call) error {
		if bad[c.event] {
			return errors.New("boom")
		}
		return nil
	}
	e.pass()
	e.fake.fn = nil
	e.fake.reset()
	return e
}

// seq is the delivered events that belong to ids, in delivery order.
func (e *parkedEnv) seq(ids []string) []string {
	in := map[string]bool{}
	for _, id := range ids {
		in[id] = true
	}
	var out []string
	for _, id := range e.fake.events() {
		if in[id] {
			out = append(out, id)
		}
	}
	return out
}

// AC1
func TestHooksStatusListsParkedOnTwoTasks(t *testing.T) {
	e := newParkedEnv(t)
	r := e.mustRig("hooks", "status", "--json")
	var got struct {
		Hooks []struct {
			Name, Runner string
			Started      bool
			Parked       []struct {
				Task, Event, Reason string
				Attempts            int
			}
		}
	}
	if err := json.Unmarshal([]byte(r.out), &got); err != nil {
		t.Fatalf("%v: %s", err, r.out)
	}
	if len(got.Hooks) != 1 || got.Hooks[0].Name != "q" || got.Hooks[0].Runner != "none" || !got.Hooks[0].Started {
		t.Fatalf("status = %s", r.out)
	}
	seen := map[string]string{}
	for _, p := range got.Hooks[0].Parked {
		if p.Attempts != 5 || !strings.Contains(p.Reason, "boom") {
			t.Errorf("parked %+v", p)
		}
		seen[p.Task] = p.Event
	}
	if len(got.Hooks[0].Parked) != 2 || seen["a"] != e.idsA[1] || seen["b"] != e.idsB[1] {
		t.Fatalf("parked = %+v", got.Hooks[0].Parked)
	}
	if r := e.mustRig("hooks", "status"); !strings.Contains(r.out, "a  "+e.idsA[1]) || !strings.Contains(r.out, "runner none") {
		t.Fatalf("text status = %q", r.out)
	}
}

// AC2
func TestHooksRetryDeliversParkedThenLater(t *testing.T) {
	e := newParkedEnv(t)
	e.mustRig("hooks", "retry", "q")
	st, err := hookstate.Load(e.statePath())
	if err != nil || len(st.Parked) != 0 {
		t.Fatalf("still parked: %v %+v", err, st)
	}
	e.pass()
	if got := e.seq(e.idsA); !eq(got, e.idsA[1:]) {
		t.Fatalf("a delivered %v, want %v", got, e.idsA[1:])
	}
	if got := e.seq(e.idsB); !eq(got, e.idsB[1:]) {
		t.Fatalf("b delivered %v, want %v", got, e.idsB[1:])
	}
}

// AC2: attempts start over, so one failure does not park again at once.
func TestHooksRetryResetsAttempts(t *testing.T) {
	e := newParkedEnv(t)
	e.mustRig("hooks", "retry", "q")
	n := 0
	e.fake.fn = func(c call) error {
		if c.event == e.idsA[1] && n < 4 {
			n++
			return errors.New("again")
		}
		return nil
	}
	e.pass()
	if got := e.seq(e.idsA); len(got) != 6 || got[4] != e.idsA[1] || got[5] != e.idsA[2] {
		t.Fatalf("a delivered %v", got)
	}
}

// AC3
func TestHooksSkipAdvancesCursor(t *testing.T) {
	e := newParkedEnv(t)
	e.mustRig("hooks", "skip", "q", e.idsA[1])
	e.pass()
	got := e.fake.events()
	if !eq(got, e.idsA[2:]) {
		t.Fatalf("delivered %v, want %v", got, e.idsA[2:])
	}
	e.fake.reset()
	e.pass()
	if got := e.fake.events(); len(got) != 0 {
		t.Fatalf("second pass delivered %v", got)
	}
	st, _ := hookstate.Load(e.statePath())
	if len(st.Parked) != 1 {
		t.Fatalf("parked = %+v", st.Parked)
	}
}

// AC3: the first event parks before any cursor is saved; skip still works.
func TestHooksSkipFirstEventNoCursor(t *testing.T) {
	e := newDeliverEnv(t, fromStart(queuedHook))
	a := e.newTask("a")
	e.note(a)
	e.note(a)
	ids := e.ids(a)
	e.fake.fn = func(c call) error {
		if c.event == ids[0] {
			return errors.New("boom")
		}
		return nil
	}
	e.pass()
	e.fake.fn = nil
	e.fake.reset()
	e.mustRig("hooks", "skip", "q", ids[0])
	e.pass()
	if got := e.fake.events(); !eq(got, ids[1:]) {
		t.Fatalf("delivered %v, want %v", got, ids[1:])
	}
}

// AC3: skipping the last event of a log moves past it too.
func TestHooksSkipLastEventOfLog(t *testing.T) {
	e := newDeliverEnv(t, fromStart(queuedHook))
	a := e.newTask("a")
	e.note(a)
	ids := e.ids(a)
	e.fake.fn = func(c call) error {
		if c.event == ids[1] {
			return errors.New("boom")
		}
		return nil
	}
	e.pass()
	e.fake.fn = nil
	e.fake.reset()
	e.mustRig("hooks", "skip", "q", ids[1])
	e.pass()
	e.note(a)
	e.pass()
	if got := e.fake.events(); len(got) != 1 || got[0] != e.ids(a)[2] {
		t.Fatalf("delivered %v", got)
	}
}

// AC3 / the runner race: skip refuses while a live runner holds the lease.
func TestHooksSkipRefusesLiveRunner(t *testing.T) {
	e := newParkedEnv(t)
	l, err := hookstate.Acquire(filepath.Dir(e.statePath()), "q")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	before, _ := hookstate.Load(e.statePath())
	r := e.rig("hooks", "skip", "q", e.idsA[1])
	after, _ := hookstate.Load(e.statePath())
	if r.code != 1 || !strings.Contains(r.errOut, "runner") ||
		!strings.Contains(r.errOut, "pid "+strconv.Itoa(os.Getpid())) {
		t.Fatalf("skip = %+v", r)
	}
	b1, _ := json.Marshal(before)
	b2, _ := json.Marshal(after)
	if string(b1) != string(b2) {
		t.Fatalf("state changed:\n%s\n%s", b1, b2)
	}
}

// AC4
func TestHooksRetrySkipRefusals(t *testing.T) {
	e := newParkedEnv(t)
	before, _ := json.Marshal(mustLoad(t, e.statePath()))
	for _, args := range [][]string{
		{"hooks", "retry", "nope"},
		{"hooks", "skip", "nope", e.idsA[1]},
		{"hooks", "skip", "q", e.idsA[0]},
	} {
		r := e.rig(args...)
		if r.code != 1 || strings.Count(strings.TrimSpace(r.errOut), "\n") != 0 {
			t.Errorf("%v = %+v", args, r)
		}
	}
	for _, args := range [][]string{{"hooks", "retry"}, {"hooks", "skip", "q"}, {"hooks", "status", "x"}} {
		if r := e.rig(args...); r.code != 2 || !strings.Contains(r.errOut, "usage: rig hooks") {
			t.Errorf("%v = %+v", args, r)
		}
	}
	after, _ := json.Marshal(mustLoad(t, e.statePath()))
	if string(before) != string(after) {
		t.Fatalf("state changed:\n%s\n%s", before, after)
	}
}

func mustLoad(t *testing.T, path string) *hookstate.State {
	t.Helper()
	s, err := hookstate.Load(path)
	if err != nil || s == nil {
		t.Fatalf("load %s: %v", path, err)
	}
	return s
}

const noRunnerWarning = "queued hook q has undelivered events and no runner"

// AC5
func TestHooksWarnsOnceWithNoRunner(t *testing.T) {
	e := newDeliverEnv(t, fromStart(queuedHook))
	a := e.newTask("a")
	r := e.mustRig("task", "move", a, "stopped", "--reason", "r")
	if n := strings.Count(r.errOut, noRunnerWarning); n != 1 {
		t.Fatalf("%d warnings, want 1: %q", n, r.errOut)
	}
	if !strings.Contains(r.errOut, "rig task move: warning: ") || !strings.Contains(r.errOut, "rig hooks deliver --follow") {
		t.Fatalf("stderr = %q", r.errOut)
	}
	l, err := hookstate.Acquire(filepath.Dir(e.statePath()), "q")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if err := l.Touch(); err != nil {
		t.Fatal(err)
	}
	r = e.mustRig("task", "move", a, "open")
	if strings.Contains(r.errOut, "no runner") {
		t.Fatalf("warned with a live runner: %q", r.errOut)
	}
}

const notStartedWarning = "rig task move: warning: queued hook q has not started and no runner, so these events will not be delivered; a runner started now begins at the newest events"

// AC1
func TestHooksWarnsNotStartedForNeverRunHook(t *testing.T) {
	e := newDeliverEnv(t, queuedHook)
	a := e.newTask("a")
	r := e.mustRig("task", "move", a, "stopped", "--reason", "r")
	if n := strings.Count(r.errOut, notStartedWarning); n != 1 {
		t.Fatalf("%d not-started lines, want 1: %q", n, r.errOut)
	}
	if strings.Contains(r.errOut, "undelivered") {
		t.Fatalf("said undelivered: %q", r.errOut)
	}
}

// AC2
func TestHooksKeepsUndeliveredLineWhenEventsWillBeDelivered(t *testing.T) {
	for name, setup := range map[string]func(e *deliverEnv){
		"from start": func(e *deliverEnv) {},
		"saved cursor": func(e *deliverEnv) {
			e.pass()
		},
	} {
		t.Run(name, func(t *testing.T) {
			hook := queuedHook
			if name == "from start" {
				hook = fromStart(queuedHook)
			}
			e := newDeliverEnv(t, hook)
			a := e.newTask("a")
			setup(e)
			r := e.mustRig("task", "move", a, "stopped", "--reason", "r")
			if n := strings.Count(r.errOut, noRunnerWarning); n != 1 || strings.Contains(r.errOut, "has not started") {
				t.Fatalf("stderr = %q", r.errOut)
			}
		})
	}
}

// setLeaseBeat rewrites the lease's beat and leaves every other field exactly
// as written. It decodes into hookstate.Holder, not a map, so the 64-bit start
// stays an integer (a float64 would round a Windows FILETIME).
func setLeaseBeat(t *testing.T, path string, beat time.Time) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var h hookstate.Holder
	if err := json.Unmarshal(b, &h); err != nil {
		t.Fatal(err)
	}
	h.Beat = beat
	if b, err = json.Marshal(h); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHooksSetLeaseBeatKeepsStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.lease")
	const start = uint64(1<<60 + 1)
	b, err := json.Marshal(hookstate.Holder{PID: 7, Start: start, Token: "tok", Beat: time.Unix(1, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	beat := time.Unix(1000, 0).UTC()
	setLeaseBeat(t, path, beat)
	if b, err = os.ReadFile(path); err != nil {
		t.Fatal(err)
	}
	var got hookstate.Holder
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Start != start || !got.Beat.Equal(beat) || got.PID != 7 || got.Token != "tok" {
		t.Fatalf("lease = %+v", got)
	}
}

// AC3
func TestHooksWarnsOnStaleHeartbeat(t *testing.T) {
	for _, tc := range []struct {
		age  time.Duration
		warn bool
	}{{3 * time.Minute, true}, {10 * time.Second, false}} {
		e := newDeliverEnv(t, fromStart(queuedHook))
		a := e.newTask("a")
		dir := filepath.Dir(e.statePath())
		l, err := hookstate.Acquire(dir, "q")
		if err != nil {
			t.Fatal(err)
		}
		lease := filepath.Join(dir, "q.lease")
		setLeaseBeat(t, lease, time.Now().UTC().Add(-tc.age))
		r := e.mustRig("task", "move", a, "stopped", "--reason", "r")
		l.Release()
		if got := strings.Contains(r.errOut, "no runner"); got != tc.warn {
			t.Fatalf("beat %v old: warned = %v, stderr %q", tc.age, got, r.errOut)
		}
	}
}

// AC4, AC5
func TestHooksStatusStartedWithParkedAndNoCursor(t *testing.T) {
	e := newDeliverEnv(t, queuedHook)
	started := func() (bool, string) {
		var got struct{ Hooks []struct{ Started bool } }
		r := e.mustRig("hooks", "status", "--json")
		if err := json.Unmarshal([]byte(r.out), &got); err != nil || len(got.Hooks) != 1 {
			t.Fatalf("%v: %s", err, r.out)
		}
		return got.Hooks[0].Started, e.mustRig("hooks", "status").out
	}
	if s, _ := started(); s {
		t.Fatal("hook with no state file reports started")
	}
	err := hookstate.Update(e.statePath(), func(s *hookstate.State) error {
		s.Parked = map[string]map[string]hookstate.Parked{"": {"ev1": {Attempts: 5, Reason: "boom"}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s, text := started()
	if !s || !strings.Contains(text, ", started,") {
		t.Fatalf("started = %v, text %q", s, text)
	}
}

// AC6
func TestHooksNoWarningWhenNothingMatches(t *testing.T) {
	e := newDeliverEnv(t, `{"name":"q","mode":"queued","on":"kind:count","run":["hook-prog"]}`)
	a := e.newTask("a")
	r := e.mustRig("note", "add", a, "--kind", "decision", "--text", "t")
	if strings.Contains(r.errOut, "no runner") {
		t.Fatalf("warned: %q", r.errOut)
	}
}

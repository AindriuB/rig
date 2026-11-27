package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/AindriuB/rig/internal/git"
	"github.com/AindriuB/rig/internal/hookstate"
	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/store/gitref"
	"github.com/AindriuB/rig/internal/workflow"
)

// call is one run of a queued hook as the fake runner saw it.
type call struct {
	argv  []string
	env   map[string]string
	event string
	kind  string
	task  string // the payload's task id, "" for a repository note
	dir   string
}

// fakeRunner stands in for ProcRunner: fn decides each call's result.
type fakeRunner struct {
	mu    sync.Mutex
	dir   string
	calls []call
	fn    func(c call) error
}

func (f *fakeRunner) Run(context.Context, []string, []byte) error { panic("queued hooks use RunHook") }

func (f *fakeRunner) RunHook(_ context.Context, argv []string, stdin []byte, h workflow.HookRun) error {
	var p struct {
		Event store.Event
		Task  map[string]any
	}
	if err := json.Unmarshal(stdin, &p); err != nil {
		return err
	}
	c := call{argv: argv, env: map[string]string{}, event: p.Event.ID, kind: p.Event.Kind, dir: f.dir}
	for _, kv := range h.Env {
		k, v, _ := strings.Cut(kv, "=")
		c.env[k] = v
	}
	if p.Task != nil {
		c.task, _ = p.Task["id"].(string)
	}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	if f.fn != nil {
		return f.fn(c)
	}
	return nil
}

func (f *fakeRunner) events() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, c.event)
	}
	return out
}

func (f *fakeRunner) reset() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

type deliverEnv struct {
	*hookEnv
	fake   *fakeRunner
	sleeps []time.Duration
}

const queuedHook = `{"name":"q","mode":"queued","on":"kind:*","run":["hook-prog","{short}"]}`

// newDeliverEnv is a repository whose workflow has the given hooks, with
// the delivery runner and its sleep replaced by fakes.
func newDeliverEnv(t *testing.T, hooks string) *deliverEnv {
	t.Helper()
	e := &deliverEnv{hookEnv: newHookEnv(t, func(string, string) string { return hooks }), fake: &fakeRunner{}}
	oldRunner, oldSleep := deliverRunner, deliverSleep
	deliverRunner = func(dir string) workflow.Runner { e.fake.dir = dir; return e.fake }
	deliverSleep = func(ctx context.Context, d time.Duration) bool {
		e.sleeps = append(e.sleeps, d)
		return ctx.Err() == nil
	}
	t.Cleanup(func() { deliverRunner, deliverSleep = oldRunner, oldSleep })
	return e
}

func (e *deliverEnv) deliver(ctx context.Context, args ...string) result {
	e.t.Helper()
	var out, errOut bytes.Buffer
	a := &app{stdout: &out, stderr: &errOut, cwd: e.repo, now: tick(),
		getenv: func(k string) string { return e.env[k] }}
	a.store = a.openStore
	code := a.run(ctx, append(append([]string{"hooks", "deliver"}, args...), "--repo", e.repo))
	return result{code, out.String(), errOut.String()}
}

func (e *deliverEnv) pass() result {
	e.t.Helper()
	r := e.deliver(context.Background())
	if r.code != 0 {
		e.t.Fatalf("hooks deliver: %+v", r)
	}
	return r
}

func (e *deliverEnv) note(short string) {
	e.t.Helper()
	e.mustRig("note", "add", short, "--kind", "decision", "--text", "t")
}

func (e *deliverEnv) statePath() string {
	return hookstate.Path(filepath.Join(e.repo, ".git"), "hk", "q")
}

func (e *deliverEnv) ids(short string) []string {
	var ids []string
	for _, ev := range e.events(short) {
		ids = append(ids, ev.ID)
	}
	return ids
}

func eq(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

func fromStart(h string) string { return strings.Replace(h, `"mode"`, `"from":"start","mode"`, 1) }

// AC1
func TestDeliverOnePassOnce(t *testing.T) {
	e := newDeliverEnv(t, fromStart(queuedHook))
	a, b := e.newTask("a"), e.newTask("b")
	e.note(a)
	e.note(a)
	e.note(b)
	e.pass()
	got := e.fake.events()
	if len(got) != 5 {
		t.Fatalf("%d deliveries, want 5: %v", len(got), got)
	}
	var onA []string
	inA := map[string]bool{}
	for _, id := range e.ids(a) {
		inA[id] = true
	}
	for _, id := range got {
		if inA[id] {
			onA = append(onA, id)
		}
	}
	if !eq(onA, e.ids(a)) {
		t.Fatalf("task a delivered %v, log %v", onA, e.ids(a))
	}
	e.fake.reset()
	e.pass()
	if got := e.fake.events(); len(got) != 0 {
		t.Fatalf("second pass delivered %v", got)
	}
}

// AC2
func TestDeliverParksAfterFiveAttempts(t *testing.T) {
	e := newDeliverEnv(t, fromStart(queuedHook))
	a, b := e.newTask("a"), e.newTask("b")
	e.note(a)
	e.note(a)
	e.note(b)
	idsA := e.ids(a)
	e.fake.fn = func(c call) error {
		if c.event == idsA[1] {
			return errors.New("boom")
		}
		return nil
	}
	e.pass()
	n := 0
	for _, id := range e.fake.events() {
		if id == idsA[1] {
			n++
		}
		if id == idsA[2] {
			t.Fatal("a's third event was delivered past the parked one")
		}
	}
	if n != 5 {
		t.Fatalf("%d attempts, want 5", n)
	}
	if want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}; len(e.sleeps) != 4 ||
		e.sleeps[0] != want[0] || e.sleeps[1] != want[1] || e.sleeps[2] != want[2] || e.sleeps[3] != want[3] {
		t.Fatalf("waits %v, want %v", e.sleeps, want)
	}
	delivered := map[string]bool{}
	for _, c := range e.fake.calls {
		delivered[c.event] = true
	}
	for _, id := range e.ids(b) {
		if !delivered[id] {
			t.Fatalf("b's event %s was not delivered", id)
		}
	}
	var taskID string
	err := hookstate.Update(e.statePath(), func(s *hookstate.State) error {
		for k, m := range s.Parked {
			p, ok := m[idsA[1]]
			if !ok || p.Attempts != 5 || !strings.Contains(p.Reason, "boom") {
				t.Errorf("parked %v", s.Parked)
			}
			taskID = k
		}
		delete(s.Parked, taskID)
		return nil
	})
	if err != nil || taskID == "" {
		t.Fatalf("%v %q", err, taskID)
	}
	e.fake.fn = nil
	e.fake.reset()
	e.pass()
	if got := e.fake.events(); !eq(got, idsA[1:]) {
		t.Fatalf("after unparking got %v, want %v", got, idsA[1:])
	}
}

// AC3
func TestRetryWait(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	for i, w := range want {
		if got := retryWait(i + 1); got != w {
			t.Errorf("retryWait(%d) = %v, want %v", i+1, got, w)
		}
	}
	for _, n := range []int{10, 11, 100, 1 << 30} {
		if got := retryWait(n); got > 5*time.Minute || n >= 10 && got != 5*time.Minute {
			t.Errorf("retryWait(%d) = %v", n, got)
		}
	}
}

// AC4
func TestDeliverKilledMidPass(t *testing.T) {
	e := newDeliverEnv(t, fromStart(queuedHook))
	a := e.newTask("a")
	e.note(a)
	e.note(a)
	ids := e.ids(a)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.fake.fn = func(c call) error {
		if c.event == ids[2] {
			cancel()
			return ctx.Err()
		}
		return nil
	}
	if r := e.deliver(ctx); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if got := e.fake.events(); !eq(got, ids) {
		t.Fatalf("first run %v, want %v", got, ids)
	}
	e.fake.fn = nil
	e.fake.reset()
	e.pass()
	got := e.fake.events()
	if len(got) == 0 || got[len(got)-1] != ids[2] || len(got) > 2 || (len(got) == 2 && got[0] != ids[1]) {
		t.Fatalf("second run %v: want event 3, and event 2 at most once more", got)
	}
	if got[0] == ids[0] {
		t.Fatalf("event 1 repeated: %v", got)
	}
	if len(got) != 1 {
		t.Fatalf("event 2 was saved after delivery but was repeated: %v", got)
	}
}

// AC5
func TestDeliverNewHookStartsAtTips(t *testing.T) {
	e := newDeliverEnv(t, queuedHook)
	a := e.newTask("a")
	e.note(a)
	e.pass()
	if got := e.fake.events(); len(got) != 0 {
		t.Fatalf("a new hook delivered history: %v", got)
	}
	e.note(a)
	e.pass()
	if got := e.fake.events(); len(got) != 1 {
		t.Fatalf("got %v, want the one new event", got)
	}
	s := newDeliverEnv(t, fromStart(queuedHook))
	x := s.newTask("x")
	s.note(x)
	s.pass()
	if got := s.fake.events(); !eq(got, s.ids(x)) {
		t.Fatalf("from start got %v, want %v", got, s.ids(x))
	}
}

// AC6
func TestDeliverDepthAboveMaxDepthIsParked(t *testing.T) {
	e := newDeliverEnv(t, strings.Replace(fromStart(queuedHook), `"run"`, `"maxDepth":2,"run"`, 1))
	a := e.newTask("a")
	e.env["RIG_HOOK_EVENT"], e.env["RIG_HOOK_TASK"], e.env["RIG_HOOK_DEPTH"] = "01ELSEWHERE", a, "2"
	e.note(a)
	delete(e.env, "RIG_HOOK_EVENT")
	ids := e.ids(a)
	if c := e.events(a)[1].Cause; c == nil || c.Depth != 3 {
		t.Fatalf("setup: cause %+v", c)
	}
	e.pass()
	if got := e.fake.events(); !eq(got, ids[:1]) {
		t.Fatalf("delivered %v, want only %v", got, ids[:1])
	}
	st, err := hookstate.Load(e.statePath())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range st.Parked {
		if p, ok := m[ids[1]]; ok {
			found = true
			if p.Reason != "depth 3 exceeds maxDepth 2" {
				t.Errorf("reason %q", p.Reason)
			}
		}
	}
	if !found {
		t.Fatalf("not parked: %+v", st.Parked)
	}
}

// AC7
func TestDeliverFiltersByWorkflow(t *testing.T) {
	e := newDeliverEnv(t, fromStart(queuedHook))
	a := e.newTask("a")
	st, err := gitref.Open(git.Repo{Dir: e.repo}, store.Options{Actor: "tester", Now: tick()})
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateTask(context.Background(), store.NewTask{Slug: "o", Initial: "open", Workflow: store.WorkflowRef{Name: "other", Version: 1}})
	if err != nil {
		t.Fatal(err)
	}
	e.mustRig("note", "add", "--kind", "decision", "--text", "repo")
	e.pass()
	byTask := map[string]int{}
	for _, c := range e.fake.calls {
		byTask[c.task]++
	}
	if byTask[other.ID] != 0 {
		t.Fatalf("the other workflow's task was delivered: %v", byTask)
	}
	if byTask[""] != 1 || len(e.fake.calls) != 2 {
		t.Fatalf("want a's created event and the repository note, got %v (%s)", byTask, a)
	}
}

// AC8
func TestDeliverRunsInRepositoryRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	out := filepath.Join(t.TempDir(), "out")
	argv, _ := json.Marshal([]string{"sh", "-c", `pwd > "$1"; echo "depth=$RIG_HOOK_DEPTH" >> "$1"`, "sh", out})
	hook := `{"name":"q","mode":"queued","from":"start","on":"kind:created","run":` + string(argv) + `}`
	e := newHookEnv(t, func(string, string) string { return hook })
	e.newTask("a")
	wt := filepath.Join(filepath.Dir(e.repo), "wt")
	gitc(t, e.repo, "worktree", "add", "-q", "-b", "side", wt)
	var stdout, stderr bytes.Buffer
	a := &app{stdout: &stdout, stderr: &stderr, cwd: wt, now: tick(), getenv: func(k string) string { return e.env[k] }}
	a.store = a.openStore
	if code := a.run(context.Background(), []string{"hooks", "deliver", "--repo", wt}); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the hook did not run: %v (%s)", err, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	want, _ := filepath.EvalSymlinks(e.repo)
	got, _ := filepath.EvalSymlinks(lines[0])
	if got != want || lines[1] != "depth=0" {
		t.Fatalf("pwd %q depth %q, want %q depth=0", lines[0], lines[1], want)
	}
}

// AC9
func TestDeliverSkipsHookHeldByLiveRunner(t *testing.T) {
	e := newDeliverEnv(t, fromStart(queuedHook))
	e.newTask("a")
	dir := filepath.Dir(e.statePath())
	l, err := hookstate.Acquire(dir, "q")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	r := e.pass()
	if got := e.fake.events(); len(got) != 0 {
		t.Fatalf("delivered %v while the lease was held", got)
	}
	if strings.Count(r.errOut, "skipped") != 1 {
		t.Fatalf("stderr %q", r.errOut)
	}
	if st, _ := hookstate.Load(e.statePath()); st != nil {
		t.Fatalf("state written: %+v", st)
	}
}

// AC10
func TestDeliverRunsNoInlineHooksAndMoveNoQueuedHook(t *testing.T) {
	inline := `{"name":"i","on":"kind:*","run":[` + `"INLINE"` + `],"env":["RIG_CMD_HELPER","RIG_CMD_EXIT"]}`
	exe, _ := os.Executable()
	b, _ := json.Marshal(exe)
	e := newDeliverEnv(t, fromStart(queuedHook)+","+strings.Replace(inline, `"INLINE"`, string(b), 1))
	a := e.newTask("a")
	e.mustRig("task", "move", a, "stopped", "--reason", "r")
	if got := e.fake.events(); len(got) != 0 {
		t.Fatalf("a write ran the queued hook: %v", got)
	}
	before := len(e.delivered())
	if before == 0 {
		t.Fatal("setup: the inline hook did not run for the writes")
	}
	e.pass()
	if len(e.fake.events()) != len(e.ids(a)) {
		t.Fatalf("queued deliveries %v", e.fake.events())
	}
	if after := len(e.delivered()); after != before {
		t.Fatalf("deliver ran an inline hook: %d -> %d", before, after)
	}
	for _, c := range e.fake.calls {
		if c.env["RIG_HOOK_NAME"] != "q" {
			t.Fatalf("hook %q ran", c.env["RIG_HOOK_NAME"])
		}
	}
}

// The runner's environment and the lease heartbeat.
func TestDeliverEnvAndHeartbeat(t *testing.T) {
	e := newDeliverEnv(t, fromStart(queuedHook))
	a := e.newTask("a")
	old := leaseTouchEvery
	leaseTouchEvery = 10 * time.Millisecond
	defer func() { leaseTouchEvery = old }()
	dir := filepath.Dir(e.statePath())
	var first, last time.Time
	e.fake.fn = func(call) error {
		first, _, _ = hookstate.Beat(dir, "q")
		time.Sleep(200 * time.Millisecond)
		last, _, _ = hookstate.Beat(dir, "q")
		return nil
	}
	e.pass()
	if !last.After(first) {
		t.Fatalf("no heartbeat during the hook: %v then %v", first, last)
	}
	c := e.fake.calls[0]
	if c.env["RIG_HOOK_DEPTH"] != "0" || c.env["RIG_ACTOR"] != "hook:q" || c.env["RIG_HOOK_NAME"] != "q" || c.env["RIG_HOOK_TASK"] == "" || c.env["RIG_HOOK_EVENT"] != c.event {
		t.Fatalf("env %v", c.env)
	}
	if c.argv[1] != a {
		t.Fatalf("argv %v: {short} not substituted", c.argv)
	}
	if want, _ := filepath.EvalSymlinks(e.repo); c.dir != want && c.dir != e.repo {
		t.Fatalf("runner dir %q, want %q", c.dir, e.repo)
	}
	if _, err := os.Stat(filepath.Join(dir, "q.lease")); !os.IsNotExist(err) {
		t.Fatalf("lease not released: %v", err)
	}
}

// AC11
func TestDeliverFollowDeliversAndExitsOnSIGTERM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM")
	}
	out := filepath.Join(t.TempDir(), "out")
	argv, _ := json.Marshal([]string{"sh", "-c", `cat >> "$1"; echo >> "$1"`, "sh", out})
	hook := `{"name":"q","mode":"queued","on":"kind:*","run":` + string(argv) + `}`
	e := newHookEnv(t, func(string, string) string { return hook })
	a := e.newTask("a")
	statePath := hookstate.Path(filepath.Join(e.repo, ".git"), "hk", "q")
	bin := filepath.Join(t.TempDir(), "rig")
	if b, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, b)
	}
	cmd := exec.Command(bin, "hooks", "deliver", "--follow", "--repo", e.repo)
	cmd.Env = append(os.Environ(), "RIG_WORKFLOW="+e.env["RIG_WORKFLOW"], "RIG_ACTOR=tester", "HOME="+t.TempDir())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() { cmd.Process.Kill() }()
	lease := filepath.Join(filepath.Dir(statePath), "q.lease")
	waitFor(t, 10*time.Second, func() bool { _, err := os.Stat(lease); return err == nil }, "lease")
	waitFor(t, 5*time.Second, func() bool { st, _ := hookstate.Load(statePath); return st != nil && st.Cursor != nil }, "baseline")
	start := time.Now()
	e.mustRig("note", "add", a, "--kind", "decision", "--text", "t")
	waitFor(t, 3*time.Second, func() bool { b, _ := os.ReadFile(out); return len(b) > 0 }, "delivery")
	t.Logf("delivered after %v", time.Since(start))
	cmd.Process.Signal(syscall.SIGTERM)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("exit: %v\n%s", err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("did not exit on SIGTERM")
	}
	if _, err := os.Stat(lease); !os.IsNotExist(err) {
		t.Fatalf("lease file remains: %v", err)
	}
}

func waitFor(t *testing.T, d time.Duration, ok func() bool, what string) {
	t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

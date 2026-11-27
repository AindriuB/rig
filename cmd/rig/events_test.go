package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/AindriuB/rig/internal/git"
	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/store/gitref"
)

type evEnv struct {
	t    *testing.T
	repo string
	st   store.Store
}

func newEventsEnv(t *testing.T) *evEnv {
	t.Helper()
	repo, _ := newRepo(t)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	st, err := gitref.Open(git.Repo{Dir: repo}, store.Options{Actor: "tester", Now: tick()})
	if err != nil {
		t.Fatal(err)
	}
	return &evEnv{t: t, repo: repo, st: st}
}

func (e *evEnv) app(out, errOut *bytes.Buffer) *app {
	a := &app{stdout: out, stderr: errOut, cwd: e.repo, now: tick(),
		getenv: func(k string) string {
			if k == "RIG_WORKFLOW" {
				return "basic"
			}
			return ""
		}}
	a.store = a.openStore
	return a
}

func (e *evEnv) events(args ...string) result {
	e.t.Helper()
	var out, errOut bytes.Buffer
	code := e.app(&out, &errOut).run(ctx, append([]string{"events"}, args...))
	return result{code, out.String(), errOut.String()}
}

func (e *evEnv) newTask(slug string) store.Task {
	e.t.Helper()
	tk, err := e.st.CreateTask(ctx, store.NewTask{Slug: slug, Initial: "open"})
	if err != nil {
		e.t.Fatal(err)
	}
	return tk
}

func (e *evEnv) transition(ref, from, to string) {
	e.t.Helper()
	_, err := e.st.Update(ctx, ref, func(store.Task, []store.Finding) ([]store.Event, error) {
		ev, err := store.NewEvent(store.KindTransition, store.TransitionData{From: from, To: to})
		return []store.Event{ev}, err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

type evLine struct {
	Format int            `json:"format"`
	Event  store.Event    `json:"event"`
	Task   map[string]any `json:"task"`
}

func evLines(t *testing.T, out string) []evLine {
	t.Helper()
	var ls []evLine
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l == "" {
			continue
		}
		var x evLine
		if err := json.Unmarshal([]byte(l), &x); err != nil {
			t.Fatalf("bad line %q: %v", l, err)
		}
		ls = append(ls, x)
	}
	return ls
}

func TestEventsFromStart(t *testing.T) {
	e := newEventsEnv(t)
	for _, s := range []string{"a", "b", "c"} {
		e.newTask(s)
	}
	e.transition("a", "open", "active")
	if _, err := e.st.AddNote(ctx, store.Note{Kind: "n", Text: "repo note"}); err != nil {
		t.Fatal(err)
	}
	r := e.events("--from-start", "--json")
	if r.code != 0 || r.errOut != "" {
		t.Fatalf("%+v", r)
	}
	ls := evLines(t, r.out)
	if len(ls) != 5 {
		t.Fatalf("got %d lines: %s", len(ls), r.out)
	}
	last := map[string]int{}
	nulls := 0
	for _, l := range ls {
		if l.Format != 1 {
			t.Errorf("format %d", l.Format)
		}
		if l.Task == nil {
			nulls++
			continue
		}
		id := l.Task["id"].(string)
		if l.Event.Seq <= last[id] {
			t.Errorf("task %s out of order at seq %d", id, l.Event.Seq)
		}
		last[id] = l.Event.Seq
	}
	if nulls != 1 {
		t.Errorf("%d null tasks, want 1", nulls)
	}
}

func TestEventsNoCursorPrintsNothing(t *testing.T) {
	e := newEventsEnv(t)
	e.newTask("a")
	if r := e.events("--json"); r.code != 0 || r.out != "" || r.errOut != "" {
		t.Fatalf("%+v", r)
	}
}

func TestEventsCursorFileResumes(t *testing.T) {
	e := newEventsEnv(t)
	e.newTask("a")
	c := filepath.Join(t.TempDir(), "c")
	if r := e.events("--cursor-file", c, "--from-start"); r.code != 0 || strings.Count(r.out, "\n") != 1 {
		t.Fatalf("%+v", r)
	}
	e.transition("a", "open", "active")
	e.transition("a", "active", "done")
	e.newTask("b")
	before, _ := os.ReadFile(c)

	r := e.events("--since", c, "--json")
	if r.code != 0 || len(evLines(t, r.out)) != 3 {
		t.Fatalf("--since: %+v", r)
	}
	if after, _ := os.ReadFile(c); !bytes.Equal(before, after) {
		t.Fatalf("--since changed the cursor file")
	}
	r2 := e.events("--cursor-file", c, "--json")
	if r2.code != 0 || r2.out != r.out {
		t.Fatalf("--cursor-file differs from --since:\n%s\n%s", r2.out, r.out)
	}
	if r := e.events("--cursor-file", c, "--json"); r.code != 0 || r.out != "" {
		t.Fatalf("third run: %+v", r)
	}
}

func TestEventsDelayedTransitionState(t *testing.T) {
	e := newEventsEnv(t)
	e.newTask("a")
	e.transition("a", "open", "pr_open")
	e.transition("a", "pr_open", "merged")
	r := e.events("--from-start", "--json")
	found := false
	for _, l := range evLines(t, r.out) {
		if l.Event.Kind != store.KindTransition {
			continue
		}
		var d store.TransitionData
		json.Unmarshal(l.Event.Data, &d)
		if d.To != "pr_open" {
			continue
		}
		found = true
		if l.Task["state"] != "pr_open" || l.Task["recordedState"] != "pr_open" {
			t.Errorf("task = %v", l.Task)
		}
		if _, has := l.Task["headSha"]; has {
			t.Errorf("headSha present")
		}
	}
	if !found {
		t.Fatalf("no transition into pr_open: %s", r.out)
	}
}

func TestEventsHeldLog(t *testing.T) {
	e := newEventsEnv(t)
	bad := e.newTask("bad")
	e.newTask("good")
	c := filepath.Join(t.TempDir(), "c")
	if r := e.events("--cursor-file", c, "--from-start"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	before, _ := os.ReadFile(c)
	e.transition("good", "open", "active")
	// Append an unreadable line to bad's log, with an event after it.
	g := git.Repo{Dir: e.repo}
	ref := gitref.TasksPrefix + bad.ID
	tip, _, _ := g.ResolveRef(ctx, ref)
	blobs, _ := g.ReadBlobs(ctx, []string{tip + ":" + gitref.LogFile})
	data := append(append([]byte{}, blobs[0].Data...), "not json\n"...)
	data = append(data, blobs[0].Data...)
	blob, _ := g.WriteBlob(ctx, data)
	tree, _ := g.MakeTree(ctx, []git.TreeEntry{{Mode: "100644", Type: "blob", OID: blob, Name: gitref.LogFile}})
	commit, err := g.CommitTree(ctx, tree, []string{tip}, "garbage")
	if err != nil {
		t.Fatal(err)
	}
	if err := g.UpdateRef(ctx, ref, commit, tip); err != nil {
		t.Fatal(err)
	}
	r := e.events("--cursor-file", c, "--json")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	ls := evLines(t, r.out)
	if len(ls) != 1 || ls[0].Task["slug"] != "good" {
		t.Fatalf("lines: %s", r.out)
	}
	if !strings.Contains(r.errOut, "bad") || !strings.Contains(r.errOut, "1") {
		t.Errorf("stderr %q does not name the task and count", r.errOut)
	}
	var cur struct {
		Logs map[string]struct{ Tip string }
	}
	raw, _ := os.ReadFile(c)
	json.Unmarshal(raw, &cur)
	var old struct {
		Logs map[string]struct{ Tip string }
	}
	json.Unmarshal(before, &old)
	if cur.Logs[ref].Tip != old.Logs[ref].Tip {
		t.Errorf("held task's cursor entry moved")
	}
}

func TestEventsFollow(t *testing.T) {
	e := newEventsEnv(t)
	e.newTask("a")
	c := filepath.Join(t.TempDir(), "c")
	pr, pw := newPipe()
	var errOut bytes.Buffer
	a := e.app(nil, &errOut)
	a.stdout = pw
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan int, 1)
	go func() {
		done <- a.run(cctx, []string{"events", "--follow", "--cursor-file", c, "--json"})
		pw.Close()
	}()
	// Let the first scan record the baseline, then write.
	time.Sleep(300 * time.Millisecond)
	e.transition("a", "open", "active")
	lines := make(chan string, 1)
	go func() {
		s := bufio.NewScanner(pr)
		if s.Scan() {
			lines <- s.Text()
		}
	}()
	select {
	case l := <-lines:
		if !strings.Contains(l, `"kind":"transition"`) {
			t.Fatalf("line %s", l)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no event within 3s")
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d, stderr %q", code, errOut.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not stop")
	}
	if r := e.events("--since", c, "--json"); r.code != 0 || r.out != "" {
		t.Fatalf("cursor does not cover the event: %+v", r)
	}
}

// TestEventsFollowHelper is not a test: with RIG_EVENTS_HELPER set it runs
// `rig events --follow` through the real main, which exits with its code.
func TestEventsFollowHelper(t *testing.T) {
	repo := os.Getenv("RIG_EVENTS_HELPER")
	if repo == "" {
		t.Skip("helper for TestEventsFollowSIGTERM")
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	os.Setenv("RIG_WORKFLOW", "basic")
	os.Args = []string{"rig", "events", "--follow", "--cursor-file", os.Getenv("RIG_EVENTS_CURSOR"), "--json"}
	main()
}

// AC8: a real SIGTERM ends --follow with exit 0 and the cursor covers what
// was printed.
func TestEventsFollowSIGTERM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signals")
	}
	e := newEventsEnv(t)
	e.newTask("a")
	c := filepath.Join(t.TempDir(), "c")
	cmd := exec.Command(os.Args[0], "-test.run=^TestEventsFollowHelper$")
	cmd.Env = append(os.Environ(), "RIG_EVENTS_HELPER="+e.repo, "RIG_EVENTS_CURSOR="+c)
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	lines := make(chan string, 16)
	go func() {
		s := bufio.NewScanner(out)
		for s.Scan() {
			lines <- s.Text()
		}
		close(lines)
	}()
	// Child start-up time varies, so keep adding tasks until one shows up
	// after the child's baseline scan.
	var first string
	deadline := time.After(20 * time.Second)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for n := 0; first == ""; {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("child ended early: %q", errOut.String())
			}
			first = l
		case <-tick.C:
			n++
			e.newTask("t" + strconv.Itoa(n))
		case <-deadline:
			t.Fatalf("no event from child; stderr %q", errOut.String())
		}
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waited = true
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child did not exit 0: %v; stderr %q", err, errOut.String())
	}
	r := e.events("--since", c, "--json")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(c); err != nil {
		t.Fatalf("cursor file not written: %v", err)
	}
	for _, l := range strings.Split(r.out, "\n") {
		if l == first {
			t.Fatalf("cursor does not cover printed event %s", first)
		}
	}
}

func TestEventsUsageErrors(t *testing.T) {
	e := newEventsEnv(t)
	if r := e.events("--since", "a", "--cursor-file", "b"); r.code != 2 {
		t.Errorf("both: %+v", r)
	}
	if r := e.events("extra"); r.code != 2 {
		t.Errorf("positional: %+v", r)
	}
}

func newPipe() (*os.File, *os.File) {
	r, w, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	return r, w
}

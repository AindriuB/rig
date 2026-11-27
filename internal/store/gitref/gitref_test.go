package gitref

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AindriuB/rig/internal/git"
	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/store/storetest"
)

var ctx = context.Background()

// gitc runs git in dir with a fixed identity and no user config.
func gitc(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRepo makes an empty repository with one commit on main.
func newRepo(t *testing.T) git.Repo {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	dir := t.TempDir()
	gitc(t, dir, "init", "-q", "-b", "main")
	gitc(t, dir, "commit", "-q", "--allow-empty", "-m", "first")
	return git.Repo{Dir: dir}
}

func open(t *testing.T, repo git.Repo) *Store {
	t.Helper()
	s, err := Open(repo, store.Options{Actor: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newTask(t *testing.T, s store.Store, slug string) store.Task {
	t.Helper()
	task, err := s.CreateTask(ctx, store.NewTask{
		Slug: slug, Title: "T " + slug, Fields: map[string]any{"size": "s", "list": []any{"a"}},
		Workflow: store.WorkflowRef{Name: "basic", Version: 1}, Initial: "todo",
	})
	if err != nil {
		t.Fatalf("CreateTask(%s): %v", slug, err)
	}
	return task
}

func countEvent(t *testing.T) store.Event {
	t.Helper()
	e, err := store.NewEvent(store.KindCount, store.CountData{Counter: "n", By: 1})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// AC1: the backend passes the store contract.
func TestContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store { return open(t, newRepo(t)) })
}

// AC2: refs/rig/tasks/<ULID> and refs/rig/notes point at commits whose tree
// holds log.jsonl, readable with plain git.
func TestLayout(t *testing.T) {
	repo := newRepo(t)
	s := open(t, repo)
	task := newTask(t, s, "layout")
	if _, err := s.Update(ctx, task.ID, func(store.Task, []store.Finding) ([]store.Event, error) {
		return []store.Event{countEvent(t)}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddNote(ctx, store.Note{Kind: "history", Text: "landed"}); err != nil {
		t.Fatal(err)
	}

	ref := "refs/rig/tasks/" + task.ID
	if got := gitc(t, repo.Dir, "ls-tree", "--name-only", ref); got != "log.jsonl" {
		t.Fatalf("tree of %s = %q", ref, got)
	}
	lines := strings.Split(gitc(t, repo.Dir, "cat-file", "blob", ref+":log.jsonl"), "\n")
	if len(lines) != 2 {
		t.Fatalf("log lines = %q", lines)
	}
	var first, second store.Event
	if json.Unmarshal([]byte(lines[0]), &first) != nil || json.Unmarshal([]byte(lines[1]), &second) != nil ||
		first.Kind != store.KindCreated || first.ID != task.ID || first.Seq != 1 ||
		second.Kind != store.KindCount || second.Seq != 2 || second.Actor != "tester" {
		t.Fatalf("log = %q", lines)
	}
	if got := gitc(t, repo.Dir, "log", "--format=%s|%an <%ae>", ref); got !=
		"count "+task.Short+"|rig <rig@localhost>\ncreated "+task.Short+"|rig <rig@localhost>" {
		t.Fatalf("history = %q", got)
	}
	note := gitc(t, repo.Dir, "cat-file", "blob", "refs/rig/notes:log.jsonl")
	if !strings.Contains(note, `"kind":"note"`) || strings.Count(note, "\n") != 0 {
		t.Fatalf("notes log = %q", note)
	}
	if got := gitc(t, repo.Dir, "status", "--porcelain"); got != "" {
		t.Fatalf("working tree touched: %q", got)
	}
	if got := gitc(t, repo.Dir, "rev-list", "--count", "HEAD"); got != "1" {
		t.Fatalf("HEAD moved: %s commits", got)
	}
}

// An event line this rig does not understand survives an append.
func TestUnknownLinesSurviveAppend(t *testing.T) {
	repo := newRepo(t)
	s := open(t, repo)
	task := newTask(t, s, "future")
	ref := "refs/rig/tasks/" + task.ID
	future := `{"v":9,"id":"01ZZZZZZZZZZZZZZZZZZZZZZZZ","seq":2,"kind":"teleport","data":{"x":1}}`
	old := gitc(t, repo.Dir, "cat-file", "blob", ref+":log.jsonl")
	blob, _ := repo.WriteBlob(ctx, []byte(old+"\n"+future+"\n"))
	tree, _ := repo.MakeTree(ctx, []git.TreeEntry{{Mode: "100644", Type: "blob", OID: blob, Name: LogFile}})
	tip := gitc(t, repo.Dir, "rev-parse", ref)
	c, _ := repo.CommitTree(ctx, tree, []string{tip}, "future\n")
	if err := repo.UpdateRef(ctx, ref, c, tip); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, task.ID, func(store.Task, []store.Finding) ([]store.Event, error) {
		return []store.Event{countEvent(t)}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := gitc(t, repo.Dir, "cat-file", "blob", ref+":log.jsonl"); !strings.Contains(got, future) {
		t.Fatalf("unknown line dropped: %q", got)
	}
}

// unlocked opens a store whose writes skip the shared store lock, as a
// writer racing from outside it would. Tests use it to lose the
// compare-and-swap on purpose.
func unlocked(t *testing.T, repo git.Repo) *Store {
	t.Helper()
	s := open(t, repo)
	s.lockPath = filepath.Join(t.TempDir(), "own.lock")
	return s
}

// AC3: 8 writers, each with its own Store as separate processes would have,
// append 25 events each to one task; every append lands with a contiguous
// seq. (Racing past the lock is the two tests below.)
func TestConcurrentAppends(t *testing.T) {
	repo := newRepo(t)
	task := newTask(t, open(t, repo), "busy")
	const writers, each = 8, 25
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		s := open(t, repo)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if _, err := s.Update(ctx, task.ID, func(store.Task, []store.Finding) ([]store.Event, error) {
					e, err := store.NewEvent(store.KindCount, store.CountData{Counter: "n", By: 1})
					return []store.Event{e}, err
				}); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	evs, err := open(t, repo).Events(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != writers*each+1 {
		t.Fatalf("events = %d, want %d", len(evs), writers*each+1)
	}
	for i, e := range evs {
		if e.Seq != i+1 {
			t.Fatalf("event %d has seq %d", i, e.Seq)
		}
	}
	got, _ := open(t, repo).Task(ctx, task.ID)
	if got.Counters["n"] != writers*each {
		t.Fatalf("counter = %d", got.Counters["n"])
	}
}

// AC3: a step that a racing writer makes illegal is refused on the retry,
// and nothing of it is written.
func TestRacingWriterMakesStepIllegal(t *testing.T) {
	repo := newRepo(t)
	a, b := open(t, repo), unlocked(t, repo)
	task := newTask(t, a, "race")
	refused := errors.New("not in todo")
	calls := 0
	_, err := a.Update(ctx, task.ID, func(cur store.Task, _ []store.Finding) ([]store.Event, error) {
		calls++
		if calls == 1 {
			// Another writer moves the task between this read and the append.
			if _, err := b.Update(ctx, task.ID, func(store.Task, []store.Finding) ([]store.Event, error) {
				e, err := store.NewEvent(store.KindTransition, store.TransitionData{From: "todo", To: "doing"})
				return []store.Event{e}, err
			}); err != nil {
				t.Fatal(err)
			}
		}
		if cur.State != "todo" {
			return nil, refused
		}
		e, err := store.NewEvent(store.KindTransition, store.TransitionData{From: "todo", To: "dropped"})
		return []store.Event{e}, err
	})
	if !errors.Is(err, refused) || calls != 2 {
		t.Fatalf("Update err = %v after %d calls; want the refusal on the second", err, calls)
	}
	got, _ := a.Task(ctx, task.ID)
	evs, _ := a.Events(ctx, task.ID)
	if got.State != "doing" || len(evs) != 2 {
		t.Fatalf("state %s, %d events; the refused step was written", got.State, len(evs))
	}
}

// A retry whose callback returns nothing must not report the first attempt's events.
func TestUpdateEventsRetryReturnsOnlyWritten(t *testing.T) {
	repo := newRepo(t)
	a, b := open(t, repo), unlocked(t, repo)
	task := newTask(t, a, "phantom")
	calls := 0
	_, got, err := a.UpdateEvents(ctx, task.ID, func(cur store.Task, _ []store.Finding) ([]store.Event, error) {
		calls++
		if calls == 1 {
			if _, err := b.Update(ctx, task.ID, func(store.Task, []store.Finding) ([]store.Event, error) {
				e, err := store.NewEvent(store.KindTransition, store.TransitionData{From: "todo", To: "doing"})
				return []store.Event{e}, err
			}); err != nil {
				t.Fatal(err)
			}
		}
		if cur.State != "todo" {
			return nil, nil
		}
		e, err := store.NewEvent(store.KindTransition, store.TransitionData{From: "todo", To: "dropped"})
		return []store.Event{e}, err
	})
	if err != nil || calls != 2 || len(got) != 0 {
		t.Fatalf("err %v, %d calls, %d events returned; want nil, 2, 0", err, calls, len(got))
	}
}

// After 8 lost races the write gives up with ErrConflict and writes nothing.
func TestConflictAfterEightTries(t *testing.T) {
	repo := newRepo(t)
	s := open(t, repo)
	s.sleep = func(context.Context, time.Duration) error { return nil }
	other := unlocked(t, repo)
	task := newTask(t, s, "starved")
	calls := 0
	_, err := s.Update(ctx, task.ID, func(store.Task, []store.Finding) ([]store.Event, error) {
		calls++
		if _, err := other.Update(ctx, task.ID, func(store.Task, []store.Finding) ([]store.Event, error) {
			return []store.Event{countEvent(t)}, nil
		}); err != nil {
			t.Fatal(err)
		}
		e, err := store.NewEvent(store.KindField, store.FieldData{Name: "mine", Value: "x"})
		return []store.Event{e}, err
	})
	if !errors.Is(err, ErrConflict) || calls != 8 {
		t.Fatalf("err = %v after %d calls, want ErrConflict after 8", err, calls)
	}
	got, _ := s.Task(ctx, task.ID)
	if _, ok := got.Fields["mine"]; ok || got.Counters["n"] != 8 {
		t.Fatalf("task = %+v", got)
	}
}

// counting wraps the plumbing and counts git processes.
type counting struct {
	git.Repo
	n atomic.Int64
}

func (c *counting) WriteBlob(ctx context.Context, d []byte) (string, error) {
	c.n.Add(1)
	return c.Repo.WriteBlob(ctx, d)
}

func (c *counting) MakeTree(ctx context.Context, e []git.TreeEntry) (string, error) {
	c.n.Add(1)
	return c.Repo.MakeTree(ctx, e)
}

func (c *counting) CommitTree(ctx context.Context, tree string, p []string, m string) (string, error) {
	c.n.Add(1)
	return c.Repo.CommitTree(ctx, tree, p, m)
}

func (c *counting) UpdateRef(ctx context.Context, ref, n, o string) error {
	c.n.Add(1)
	return c.Repo.UpdateRef(ctx, ref, n, o)
}

func (c *counting) ResolveRef(ctx context.Context, ref string) (string, bool, error) {
	c.n.Add(1)
	return c.Repo.ResolveRef(ctx, ref)
}

func (c *counting) ReadRefs(ctx context.Context, prefix string) ([]git.Ref, error) {
	c.n.Add(1)
	return c.Repo.ReadRefs(ctx, prefix)
}

func (c *counting) ReadBlobs(ctx context.Context, specs []string) ([]git.Blob, error) {
	c.n.Add(1)
	return c.Repo.ReadBlobs(ctx, specs)
}

// AC4: reading 200 tasks starts at most three git processes.
func TestReadingManyTasksIsFewProcesses(t *testing.T) {
	repo := newRepo(t)
	// Write 200 task logs with one git fast-import, which is much faster than
	// 200 CreateTasks.
	opts := store.Options{Actor: "tester"}
	var stream bytes.Buffer
	for i := 0; i < 200; i++ {
		e, err := opts.Created(store.NewTask{Slug: "t", Initial: "todo"}, func(string) bool { return false })
		if err != nil {
			t.Fatal(err)
		}
		line, _ := json.Marshal(e)
		line = append(line, '\n')
		fmt.Fprintf(&stream, "blob\nmark :%d\ndata %d\n%s\n", i+1, len(line), line)
		fmt.Fprintf(&stream, "commit %s%s\ncommitter rig <rig@localhost> 0 +0000\ndata 8\ncreated\nM 100644 :%d %s\n\n",
			TasksPrefix, e.ID, i+1, LogFile)
	}
	cmd := exec.Command("git", "-C", repo.Dir, "fast-import", "--quiet")
	cmd.Stdin = &stream
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fast-import: %v\n%s", err, out)
	}
	cg := &counting{Repo: repo}
	s := newStore(cg, opts)
	tasks, err := s.Tasks(ctx, store.TaskFilter{})
	if err != nil || len(tasks) != 200 {
		t.Fatalf("Tasks = %d, %v", len(tasks), err)
	}
	if n := cg.n.Load(); n > 3 {
		t.Fatalf("reading 200 tasks started %d git processes", n)
	}
	cg.n.Store(0)
	if _, err := s.Notes(ctx, store.NoteFilter{}); err != nil {
		t.Fatal(err)
	}
	if n := cg.n.Load(); n > 3 {
		t.Fatalf("reading notes of 200 tasks started %d git processes", n)
	}
}

// Changing what the store returned does not change what it returns next.
func TestReturnedValuesAreCopies(t *testing.T) {
	s := open(t, newRepo(t))
	task := newTask(t, s, "copy")
	task.Fields["size"] = "xl"
	task.Fields["list"].([]any)[0] = "changed"
	evs, err := s.Events(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range evs[0].Data {
		evs[0].Data[i] = ' '
	}
	got, err := s.Task(ctx, task.ID)
	if err != nil || got.Fields["size"] != "s" || got.Fields["list"].([]any)[0] != "a" {
		t.Fatalf("Task after mutation = %+v, %v", got, err)
	}
	again, _ := s.Events(ctx, task.ID)
	if !json.Valid(again[0].Data) {
		t.Fatalf("event data changed: %q", again[0].Data)
	}
}

// lockFile is the store lock's path in repo.
func lockFile(t *testing.T, repo git.Repo) string {
	t.Helper()
	common, err := repo.CommonDir(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(common, "rig", "store.lock")
}

func holdLock(t *testing.T, repo git.Repo, age time.Duration) string {
	t.Helper()
	path := lockFile(t, repo)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("1 other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if age > 0 {
		old := time.Now().Add(-age)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func addCount(s *Store, ref string) error {
	_, err := s.Update(ctx, ref, func(store.Task, []store.Finding) ([]store.Event, error) {
		e, err := store.NewEvent(store.KindCount, store.CountData{Counter: "n", By: 1})
		return []store.Event{e}, err
	})
	return err
}

// A write takes the lock and leaves no lock file behind.
func TestLockReleased(t *testing.T) {
	repo := newRepo(t)
	s := open(t, repo)
	task := newTask(t, s, "tidy")
	if err := addCount(s, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockFile(t, repo)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock file left behind: %v", err)
	}
}

// A lock file older than a minute is broken and the write goes ahead.
func TestStaleLockIsBroken(t *testing.T) {
	repo := newRepo(t)
	s := open(t, repo)
	task := newTask(t, s, "stale")
	path := holdLock(t, repo, 2*lockStale)
	if err := addCount(s, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale lock still there: %v", err)
	}
	if got, _ := s.Task(ctx, task.ID); got.Counters["n"] != 1 {
		t.Fatalf("counter = %d", got.Counters["n"])
	}
}

// A writer waits on a fresh lock held by someone else, then succeeds once it
// is released; another that gives up first returns ErrConflict and writes
// nothing.
func TestHeldLockMakesWriterWait(t *testing.T) {
	repo := newRepo(t)
	s := open(t, repo)
	task := newTask(t, s, "held")
	path := holdLock(t, repo, 0)

	quick := open(t, repo)
	quick.lockWait = 50 * time.Millisecond
	if err := addCount(quick, task.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}

	done := make(chan error, 1)
	go func() { done <- addCount(s, task.ID) }()
	select {
	case err := <-done:
		t.Fatalf("write did not wait for the lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if got, _ := open(t, repo).Task(ctx, task.ID); got.Counters["n"] != 0 {
		t.Fatalf("written while locked: %d", got.Counters["n"])
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("write still waiting after the lock was released")
	}
	if got, _ := s.Task(ctx, task.ID); got.Counters["n"] != 1 {
		t.Fatalf("counter = %d, want 1", got.Counters["n"])
	}
}

// parseLog skips and counts a line of an unknown version and a line that is
// not JSON; it never fails.
func TestParseLogSkips(t *testing.T) {
	raw := []byte(`{"v":1,"id":"01A","seq":1,"kind":"count","data":{}}
{"v":9,"seq":"two","kind":"teleport"}
not json
{"v":1,"id":"01B","seq":"bad"}
{"v":1,"id":"01C","seq":2,"kind":"count","data":{}}
`)
	evs, skipped := parseLog(raw)
	if len(evs) != 2 || evs[0].ID != "01A" || evs[1].ID != "01C" || skipped != 3 {
		t.Fatalf("events %+v, skipped %d", evs, skipped)
	}
}

// writeLog points ref at a commit whose log.jsonl is data.
func writeLog(t *testing.T, repo git.Repo, ref, data string) {
	t.Helper()
	blob, err := repo.WriteBlob(ctx, []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := repo.MakeTree(ctx, []git.TreeEntry{{Mode: "100644", Type: "blob", OID: blob, Name: LogFile}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := repo.CommitTree(ctx, tree, nil, "test\n")
	if err != nil {
		t.Fatal(err)
	}
	gitc(t, repo.Dir, "update-ref", ref, c)
}

// A malformed line in one task's log does not fail reads of the others, and
// survives an append to its own task.
func TestMalformedLineDoesNotFailOtherTasks(t *testing.T) {
	repo := newRepo(t)
	s := open(t, repo)
	good := newTask(t, s, "good")
	bad := newTask(t, s, "bad")
	ref := TasksPrefix + bad.ID
	old := gitc(t, repo.Dir, "cat-file", "blob", ref+":"+LogFile)
	writeLog(t, repo, ref, old+"\n{broken\n")
	tasks, err := s.Tasks(ctx, store.TaskFilter{})
	if err != nil || len(tasks) != 2 {
		t.Fatalf("Tasks = %d, %v", len(tasks), err)
	}
	if _, err := s.Task(ctx, good.ID); err != nil {
		t.Fatal(err)
	}
	if err := addCount(s, bad.ID); err != nil {
		t.Fatal(err)
	}
	if got := gitc(t, repo.Dir, "cat-file", "blob", ref+":"+LogFile); !strings.Contains(got, "{broken") {
		t.Fatalf("malformed line dropped: %q", got)
	}
}

// Notes reads only the notes ref and task refs: an unrelated ref under
// refs/rig/ with no log.jsonl is neither read nor an error.
func TestNotesIgnoresUnrelatedRefs(t *testing.T) {
	repo := newRepo(t)
	s := open(t, repo)
	if _, err := s.AddNote(ctx, store.Note{Kind: "history", Text: "kept"}); err != nil {
		t.Fatal(err)
	}
	gitc(t, repo.Dir, "update-ref", "refs/rig/other", gitc(t, repo.Dir, "rev-parse", "HEAD"))
	notes, err := s.Notes(ctx, store.NoteFilter{})
	if err != nil || len(notes) != 1 || notes[0].Text != "kept" {
		t.Fatalf("Notes = %+v, %v", notes, err)
	}
}

// A log with an unreadable line and a v2 line reports both as Skipped on
// every read path, and Update neither conflicts nor loses the count.
func TestSkippedCountsOnEveryPath(t *testing.T) {
	repo := newRepo(t)
	s := open(t, repo)
	task := newTask(t, s, "degraded")
	ref := TasksPrefix + task.ID
	old := gitc(t, repo.Dir, "cat-file", "blob", ref+":"+LogFile)
	future := `{"v":2,"id":"01ZZZZZZZZZZZZZZZZZZZZZZZZ","seq":50,"kind":"count","data":{}}`
	writeLog(t, repo, ref, old+"\n"+future+"\n{broken\n")

	got, err := s.Task(ctx, task.ID)
	if err != nil || got.Skipped != 2 {
		t.Fatalf("Task: %+v, %v", got, err)
	}
	all, err := s.Tasks(ctx, store.TaskFilter{})
	if err != nil || len(all) != 1 || all[0].Skipped != 2 {
		t.Fatalf("Tasks: %+v, %v", all, err)
	}
	var seen store.Task
	res, err := s.Update(ctx, task.ID, func(cur store.Task, _ []store.Finding) ([]store.Event, error) {
		seen = cur
		return []store.Event{countEvent(t)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seen, got) {
		t.Fatalf("callback task %+v != Task %+v", seen, got)
	}
	if res.Skipped != 2 {
		t.Fatalf("Update result Skipped = %d, want 2", res.Skipped)
	}
}

func TestCause(t *testing.T) {
	storetest.Cause(t, func(t *testing.T, o store.Options) store.Store {
		s, err := Open(newRepo(t), o)
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

// The log line carries the cause key only when Options.Cause is set.
func TestCauseInLogLine(t *testing.T) {
	for _, tc := range []struct {
		cause *store.Cause
		want  string
	}{
		{&store.Cause{Event: "E", Hook: "h", Depth: 2}, `"cause":{"event":"E","hook":"h","depth":2}`},
		{nil, ""},
	} {
		repo := newRepo(t)
		s, err := Open(repo, store.Options{Actor: "tester", Cause: tc.cause})
		if err != nil {
			t.Fatal(err)
		}
		task := newTask(t, s, "line")
		if err := addCount(s, task.ID); err != nil {
			t.Fatal(err)
		}
		raw := gitc(t, repo.Dir, "show", TasksPrefix+task.ID+":"+LogFile)
		for _, line := range strings.Split(raw, "\n") {
			if tc.want == "" && strings.Contains(line, `"cause"`) {
				t.Fatalf("cause key with none set: %s", line)
			}
			if tc.want != "" && !strings.Contains(line, tc.want) {
				t.Fatalf("line lacks %s: %s", tc.want, line)
			}
		}
	}
}

// A line with a cause, or an unknown top-level key, folds the same as one
// without and is not skipped.
func TestCauseAndUnknownKeyNotSkipped(t *testing.T) {
	repo := newRepo(t)
	id := "01JD00000000000000000000A1"
	writeLog(t, repo, TasksPrefix+id, strings.Join([]string{
		`{"v":1,"id":"` + id + `","seq":1,"time":"2026-11-22T10:00:00Z","actor":"a","kind":"created","data":{"short":"t-a1","slug":"x","title":"X","workflow":{"name":"basic","version":1},"state":"todo"}}`,
		`{"v":1,"id":"01JD00000000000000000000B1","seq":2,"time":"2026-11-22T10:01:00Z","actor":"a","kind":"count","data":{"counter":"n","by":1},"cause":{"event":"E","hook":"h","depth":1}}`,
		`{"v":1,"id":"01JD00000000000000000000B2","seq":3,"time":"2026-11-22T10:02:00Z","actor":"a","kind":"count","data":{"counter":"n","by":1},"future":{"x":1}}`,
		`{"v":1,"id":"01JD00000000000000000000B3","seq":4,"time":"2026-11-22T10:03:00Z","actor":"a","kind":"count","data":{"counter":"n","by":1}}`,
	}, "\n")+"\n")
	task, err := open(t, repo).Task(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if task.Skipped != 0 || task.Counters["n"] != 3 {
		t.Fatalf("skipped %d counts %v", task.Skipped, task.Counters)
	}
}

// A context cancelled while Update holds the store lock returns the context's
// error and leaves no lock file.
func TestUpdateCancelledHoldingLock(t *testing.T) {
	repo := newRepo(t)
	s := open(t, repo)
	task := newTask(t, s, "cancel")
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	_, err := s.Update(cctx, task.ID, func(store.Task, []store.Finding) ([]store.Event, error) {
		cancel()
		return []store.Event{countEvent(t)}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(lockFile(t, repo)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock file left behind: %v", err)
	}
}

func TestOnce(t *testing.T) {
	storetest.Once(t, func(t *testing.T, o store.Options) (store.Store, func(store.Options) store.Store) {
		repo := newRepo(t)
		open := func(o store.Options) store.Store {
			s, err := Open(repo, o)
			if err != nil {
				t.Fatal(err)
			}
			return s
		}
		return open(o), open
	})
}

package gitref

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
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

// AC3: 8 goroutines append 25 events each to one task; every append lands
// with a contiguous seq. (Racing processes are the two tests below.)
func TestConcurrentAppends(t *testing.T) {
	repo := newRepo(t)
	s := open(t, repo)
	task := newTask(t, s, "busy")
	const writers, each = 8, 25
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for w := 0; w < writers; w++ {
		wg.Add(1)
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
	a, b := open(t, repo), open(t, repo)
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

// After 8 lost races the write gives up with ErrConflict and writes nothing.
func TestConflictAfterEightTries(t *testing.T) {
	repo := newRepo(t)
	s := open(t, repo)
	s.sleep = func(context.Context, time.Duration) error { return nil }
	other := open(t, repo)
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

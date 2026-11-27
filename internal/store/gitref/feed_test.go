package gitref

import (
	"context"
	"testing"

	"github.com/AindriuB/rig/internal/git"
	"github.com/AindriuB/rig/internal/store"
)

// specLog records the specs of each ReadBlobs call.
type specLog struct {
	git.Repo
	calls [][]string
}

func (c *specLog) ReadBlobs(ctx context.Context, specs []string) ([]git.Blob, error) {
	c.calls = append(c.calls, specs)
	return c.Repo.ReadBlobs(ctx, specs)
}

func TestFeedReadsOnlyChangedRefs(t *testing.T) {
	repo := newRepo(t)
	opts := store.Options{Actor: "tester"}
	w := newStore(repo, opts)
	a, err := w.CreateTask(ctx, store.NewTask{Slug: "a", Initial: "todo"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.CreateTask(ctx, store.NewTask{Slug: "b", Initial: "todo"})
	if err != nil {
		t.Fatal(err)
	}
	sl := &specLog{Repo: repo}
	s := newStore(sl, opts)

	res, err := s.Feed(ctx, nil)
	if err != nil || len(res.Items) != 0 || len(res.Next.Logs) != 2 {
		t.Fatalf("baseline: %+v, %v", res, err)
	}
	sl.calls = nil
	res, err = s.Feed(ctx, res.Next)
	if err != nil || len(res.Items) != 0 || len(sl.calls) != 0 {
		t.Fatalf("nothing changed: items %d, cat-file calls %d, %v", len(res.Items), len(sl.calls), err)
	}
	cur := res.Next
	if _, err := w.AddNote(ctx, store.Note{Task: a.ID, Kind: "n", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	res, err = s.Feed(ctx, cur)
	if err != nil || len(res.Items) != 1 || res.Items[0].Task == nil || res.Items[0].Task.ID != a.ID {
		t.Fatalf("one changed: %+v, %v", res, err)
	}
	if len(sl.calls) != 1 || len(sl.calls[0]) != 1 || sl.calls[0][0][:len(res.Next.Logs[TasksPrefix+a.ID].Tip)] != res.Next.Logs[TasksPrefix+a.ID].Tip {
		t.Fatalf("cat-file calls = %v", sl.calls)
	}
	_ = b
}

func TestFeedHoldsLogWithUnreadableLines(t *testing.T) {
	repo := newRepo(t)
	opts := store.Options{Actor: "tester"}
	s := newStore(repo, opts)
	bad, err := s.CreateTask(ctx, store.NewTask{Slug: "bad", Initial: "todo"})
	if err != nil {
		t.Fatal(err)
	}
	good, _ := s.CreateTask(ctx, store.NewTask{Slug: "good", Initial: "todo"})
	res, _ := s.Feed(ctx, store.NewCursor())
	cur := res.Next
	ref := TasksPrefix + bad.ID
	tip, _, _ := repo.ResolveRef(ctx, ref)
	blobs, _ := repo.ReadBlobs(ctx, []string{tip + ":" + LogFile})
	blob, _ := repo.WriteBlob(ctx, append(append(append([]byte{}, blobs[0].Data...), "junk\n"...), blobs[0].Data...))
	tree, _ := repo.MakeTree(ctx, []git.TreeEntry{{Mode: "100644", Type: "blob", OID: blob, Name: LogFile}})
	commit, err := repo.CommitTree(ctx, tree, []string{tip}, "junk")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateRef(ctx, ref, commit, tip); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddNote(ctx, store.Note{Task: good.ID, Kind: "n", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	res, err = s.Feed(ctx, cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].Task.ID != good.ID {
		t.Fatalf("items = %+v", res.Items)
	}
	if len(res.Held) != 1 || res.Held[0].Name != "bad" || res.Held[0].Skipped != 1 {
		t.Fatalf("held = %+v", res.Held)
	}
	if res.Next.Logs[ref] != cur.Logs[ref] {
		t.Fatalf("held cursor entry moved: %+v -> %+v", cur.Logs[ref], res.Next.Logs[ref])
	}
}

// appendRaw commits log data for a task: its current log plus extra bytes.
func appendRaw(t *testing.T, repo git.Repo, ref string, build func(old []byte) []byte) {
	t.Helper()
	tip, _, _ := repo.ResolveRef(ctx, ref)
	blobs, _ := repo.ReadBlobs(ctx, []string{tip + ":" + LogFile})
	blob, _ := repo.WriteBlob(ctx, build(blobs[0].Data))
	tree, _ := repo.MakeTree(ctx, []git.TreeEntry{{Mode: "100644", Type: "blob", OID: blob, Name: LogFile}})
	commit, err := repo.CommitTree(ctx, tree, []string{tip}, "raw")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateRef(ctx, ref, commit, tip); err != nil {
		t.Fatal(err)
	}
}

func TestFeedBaselineHoldsUnreadableLog(t *testing.T) {
	repo := newRepo(t)
	s := newStore(repo, store.Options{Actor: "tester"})
	bad, _ := s.CreateTask(ctx, store.NewTask{Slug: "bad", Initial: "todo"})
	good, _ := s.CreateTask(ctx, store.NewTask{Slug: "good", Initial: "todo"})
	ref := TasksPrefix + bad.ID
	n := 0
	tip0, _, _ := repo.ResolveRef(ctx, ref)
	var e1 []byte
	appendRaw(t, repo, ref, func(old []byte) []byte {
		n = len(parseLogEvents(old))
		e1 = old
		return append(append([]byte{}, old...), "junk\n"...)
	})
	// e2 in its own commit, so the walk must pass an unreadable ancestor.
	appendRaw(t, repo, ref, func(old []byte) []byte {
		return append(append([]byte{}, old...), e1...)
	})
	// AC1
	res, err := s.Feed(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Next.Logs[ref].Count; got != n {
		t.Fatalf("baseline Count = %d, want %d", got, n)
	}
	if len(res.Held) != 1 || res.Held[0].Name != "bad" {
		t.Fatalf("held = %+v", res.Held)
	}
	// AC6: the position is the last readable commit, not the current tip.
	if got := res.Next.Logs[ref].Tip; got != tip0 {
		t.Fatalf("baseline Tip = %s, want the last readable commit %s", got, tip0)
	}
	// AC2
	if p := res.Next.Logs[TasksPrefix+good.ID]; p.Count != n || len(res.Items) != 0 {
		t.Fatalf("clean log pos %+v items %d", p, len(res.Items))
	}
	// AC3
	if _, err := s.AddNote(ctx, store.Note{Task: good.ID, Kind: "n", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	res2, err := s.Feed(ctx, res.Next)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Next.Logs[ref] != res.Next.Logs[ref] {
		t.Fatalf("held moved: %+v", res2.Next.Logs[ref])
	}
	if len(res2.Items) != 1 || res2.Items[0].Task.ID != good.ID {
		t.Fatalf("items = %+v", res2.Items)
	}
	if len(res2.Held) != 1 || res2.Held[0].Name != "bad" {
		t.Fatalf("second scan held = %+v", res2.Held)
	}
}

func parseLogEvents(raw []byte) []store.Event {
	e, _ := parseLog(raw)
	return e
}

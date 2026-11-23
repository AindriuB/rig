package gitref

import (
	"errors"
	"sync"
	"testing"

	"github.com/AindriuB/rig/internal/store"
)

// Writers racing to link one issue, each with a Store of its own as separate
// processes would have: exactly one task is created, and every other writer
// gets that task back with ErrLinked.
func TestRacingLinkedCreates(t *testing.T) {
	repo := newRepo(t)
	const writers = 6
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created int
		linked  int
		ids     = map[string]bool{}
		bad     []error
	)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		s := open(t, repo)
		go func() {
			defer wg.Done()
			task, err := s.CreateTask(ctx, store.NewTask{
				Slug: "dup", Workflow: store.WorkflowRef{Name: "basic", Version: 1}, Initial: "todo",
				External: &store.External{Kind: "github", Host: "github.com", Key: "o/r#1"},
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				created++
			case errors.Is(err, store.ErrLinked):
				linked++
			default:
				bad = append(bad, err)
			}
			ids[task.ID] = true
		}()
	}
	wg.Wait()
	if len(bad) != 0 {
		t.Fatal(bad)
	}
	if created != 1 || linked != writers-1 || len(ids) != 1 {
		t.Fatalf("created %d linked %d distinct ids %d", created, linked, len(ids))
	}
	all, err := open(t, repo).Tasks(ctx, store.TaskFilter{})
	if err != nil || len(all) != 1 {
		t.Fatalf("%d tasks, %v", len(all), err)
	}
}

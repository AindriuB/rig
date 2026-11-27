package workflow

import (
	"testing"

	"github.com/AindriuB/rig/internal/store"
)

func TestQueuedHooksSelectAndDepth(t *testing.T) {
	three := 3
	w := &Workflow{Hooks: []Hook{
		{Name: "inline", On: StateList{"kind:*"}, Run: []string{"x"}},
		{Name: "q", Mode: ModeQueued, On: StateList{"kind:note"}, Run: []string{"x"}, MaxDepth: &three},
		{Name: "q2", Mode: ModeQueued, On: StateList{"kind:note"}, Run: []string{"x"}},
	}}
	if got := w.QueuedHooks(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("QueuedHooks = %v", got)
	}
	if m, _ := w.QueuedMatches(1, store.Event{Kind: "note"}); !m {
		t.Error("kind:note did not match a note")
	}
	if m, _ := w.QueuedMatches(1, store.Event{Kind: "count"}); m {
		t.Error("kind:note matched a count")
	}
	if w.QueuedMaxDepth(1) != 3 || w.QueuedMaxDepth(2) != DefaultMaxDepth {
		t.Errorf("maxDepth %d, %d", w.QueuedMaxDepth(1), w.QueuedMaxDepth(2))
	}
}

package store

import (
	"reflect"
	"testing"
)

func TestMergeIsCommutativeUnion(t *testing.T) {
	log := sampleLog(t)
	common := log[:4]
	// Two clones append different events after the shared prefix; both use
	// seq 5 and 6, and the ids decide the order.
	a := append(append([]Event(nil), common...), log[4], log[5])
	b := append([]Event(nil), common...)
	for _, e := range []Event{log[6], log[7]} {
		e.Seq -= 2
		e.ID = "01JZ" + e.ID[4:] // sorts after a's ids at the same seq
		b = append(b, e)
	}
	// Out of order input must not matter.
	b[0], b[5] = b[5], b[0]

	ab, ba := Merge(a, b), Merge(b, a)
	if !reflect.DeepEqual(ab, ba) {
		t.Fatalf("Merge is not commutative:\n%v\n%v", ab, ba)
	}
	if len(ab) != 8 {
		t.Fatalf("len = %d, want 8 (4 shared, 2 + 2 new)", len(ab))
	}
	for i := 1; i < len(ab); i++ {
		if !eventLess(ab[i-1], ab[i]) {
			t.Fatalf("events %d and %d out of (seq, id) order", i-1, i)
		}
	}
	if got := Merge(a, a); !reflect.DeepEqual(got, Merge(a, nil)) || len(got) != len(a) {
		t.Fatalf("Merge(a, a) = %d events, want %d", len(got), len(a))
	}
	task, findings, _, skipped := Fold(ab)
	if skipped != 0 || len(findings) != 2 || task.State != "review" {
		t.Fatalf("merged log folds to state %q, %d findings, %d skipped", task.State, len(findings), skipped)
	}
}

func TestMergeSameIDDifferentContentIsCommutative(t *testing.T) {
	x := ev(t, 1, KindNote, NoteData{ID: "N", Text: "one"})
	y := ev(t, 1, KindNote, NoteData{ID: "N", Text: "two"})
	if !reflect.DeepEqual(Merge([]Event{x}, []Event{y}), Merge([]Event{y}, []Event{x})) {
		t.Fatal("a same-id conflict resolves differently by argument order")
	}
}

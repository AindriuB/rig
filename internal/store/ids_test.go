package store

import (
	"errors"
	"reflect"
	"testing"
)

func TestShortIDGrowsOnCollision(t *testing.T) {
	const id = "01JD000000000000000ABC3K9F"
	taken := map[string]bool{}
	isTaken := func(s string) bool { return taken[s] }
	for _, want := range []string{"t-3k9f", "t-c3k9f", "t-bc3k9f"} {
		got := ShortID(TaskPrefix, id, isTaken)
		if got != want {
			t.Fatalf("ShortID = %q, want %q", got, want)
		}
		taken[got] = true
	}
}

func TestShortIDAllTakenFallsBackToWholeID(t *testing.T) {
	got := ShortID(FindingPrefix, "ABCDE", func(string) bool { return true })
	if got != "f-abcde" {
		t.Fatalf("ShortID = %q, want f-abcde", got)
	}
}

func TestResolve(t *testing.T) {
	tasks := []Task{
		{ID: "01JDAAAAAAAAAAAAAAAAAA0001", Short: "t-0001", Slug: "parser"},
		{ID: "01JDAAAAAAAAAAAAAAAAAA0002", Short: "t-0002", Slug: "lexer"},
		{ID: "01JDAAAAAAAAAAAAAAAAAA0003", Short: "t-0003", Slug: "lexer"},
	}
	for _, ref := range []string{"01JDAAAAAAAAAAAAAAAAAA0001", "01jdaaaaaaaaaaaaaaaaaa0001", "t-0001", "parser"} {
		got, err := Resolve(ref, tasks)
		if err != nil || got.ID != tasks[0].ID {
			t.Fatalf("Resolve(%q) = %+v, %v", ref, got, err)
		}
	}

	_, err := Resolve("lexer", tasks)
	var amb *AmbiguousError
	if !errors.Is(err, ErrAmbiguous) || !errors.As(err, &amb) {
		t.Fatalf("Resolve(lexer) err = %v, want ErrAmbiguous", err)
	}
	if want := []string{tasks[1].ID, tasks[2].ID}; amb.Ref != "lexer" || !reflect.DeepEqual(amb.Matches, want) {
		t.Fatalf("AmbiguousError = %+v", amb)
	}

	for _, ref := range []string{"", "t-9999", "nothing"} {
		if _, err := Resolve(ref, tasks); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Resolve(%q) err = %v, want ErrNotFound", ref, err)
		}
	}
}

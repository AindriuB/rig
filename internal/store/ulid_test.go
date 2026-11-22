package store

import (
	"strings"
	"testing"
	"time"
)

func TestNewULIDMonotonicWithinOneMillisecond(t *testing.T) {
	now := time.Date(2026, 11, 22, 10, 4, 5, 0, time.UTC)
	prev := ""
	for i := 0; i < 10000; i++ {
		id := NewULID(now)
		if len(id) != 26 {
			t.Fatalf("len(%q) = %d, want 26", id, len(id))
		}
		if id <= prev {
			t.Fatalf("ULID %d %q does not sort after %q", i, id, prev)
		}
		prev = id
	}
}

func TestNewULIDEncodesTimeAndAlphabet(t *testing.T) {
	var g ulidSource
	base := time.UnixMilli(1000)
	a := g.next(base)
	b := g.next(base.Add(time.Second))
	if a[:10] >= b[:10] {
		t.Fatalf("time prefix %q not before %q", a[:10], b[:10])
	}
	if got := ulidTime(b); got != base.Add(time.Second).UnixMilli() {
		t.Fatalf("decoded time = %d, want %d", got, base.Add(time.Second).UnixMilli())
	}
	for _, c := range a + b {
		if !strings.ContainsRune(crockford, c) {
			t.Fatalf("character %q is not Crockford base32", c)
		}
	}
}

func TestNewULIDClockGoingBackStaysMonotonic(t *testing.T) {
	var g ulidSource
	a := g.next(time.UnixMilli(5000))
	b := g.next(time.UnixMilli(4000))
	if b <= a {
		t.Fatalf("%q after a backwards clock does not sort after %q", b, a)
	}
}

func ulidTime(id string) int64 {
	var ms int64
	for _, c := range id[:10] {
		ms = ms<<5 | int64(strings.IndexRune(crockford, c))
	}
	return ms
}

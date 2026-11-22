package store

import "testing"

func TestDefaultKey(t *testing.T) {
	cases := []struct{ sev, path, summary, want string }{
		{"high", "a/b.go:12", "  Nil   Deref\tin Parse ", "high|a/b.go|nil deref in parse"},
		{"high", "a/b.go:12:5", "x", "high|a/b.go|x"},
		{"low", "a/b.go", "X", "low|a/b.go|x"},
		{"low", "", "X", "low||x"},
		{"low", `C:\src\a.go:7`, "x", `low|C:\src\a.go|x`},
		{"low", "a/b.go:", "x", "low|a/b.go:|x"},
	}
	for _, c := range cases {
		if got := DefaultKey(c.sev, c.path, c.summary); got != c.want {
			t.Errorf("DefaultKey(%q, %q, %q) = %q, want %q", c.sev, c.path, c.summary, got, c.want)
		}
	}
	if DefaultKey("high", "a.go:1", "Same thing") != DefaultKey("high", "a.go:99", "same  thing") {
		t.Error("the same finding on another line gives a different key")
	}
}

func TestFindOpenByKey(t *testing.T) {
	key := DefaultKey("high", "a.go:3", "x")
	findings := []Finding{
		{ID: "F1", Key: key, Status: StatusFixed},
		{ID: "F2", Key: "other", Status: StatusOpen},
		{ID: "F3", Key: key, Status: StatusOpen},
	}
	got, ok := FindOpenByKey(findings, key)
	if !ok || got.ID != "F3" {
		t.Fatalf("FindOpenByKey = %+v, %v; want F3", got, ok)
	}
	if _, ok := FindOpenByKey(findings[:2], key); ok {
		t.Fatal("matched a finding that is not open")
	}
}

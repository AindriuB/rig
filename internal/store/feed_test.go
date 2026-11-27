package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCursorRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c")
	if c, err := LoadCursor(p); c != nil || err != nil {
		t.Fatalf("missing file: %v, %v", c, err)
	}
	c := NewCursor()
	c.Logs["refs/rig/notes"] = LogPos{Tip: "abc", Count: 2}
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := LoadCursor(p)
	if err != nil || got.Logs["refs/rig/notes"] != (LogPos{Tip: "abc", Count: 2}) {
		t.Fatalf("got %+v, %v", got, err)
	}
	if ents, _ := os.ReadDir(filepath.Dir(p)); len(ents) != 1 {
		t.Fatalf("temp file left behind: %v", ents)
	}
	os.WriteFile(p, []byte(`{"format":9,"logs":{}}`), 0o644)
	if _, err := LoadCursor(p); err == nil {
		t.Fatal("unknown format accepted")
	}
}

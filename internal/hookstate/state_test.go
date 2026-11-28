package hookstate

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/AindriuB/rig/internal/store"
)

func TestAC1RoundTrip(t *testing.T) {
	p := Path(t.TempDir(), "w", "chat")
	if s, err := Load(p); s != nil || err != nil {
		t.Fatalf("missing: %v %v", s, err)
	}
	cur := store.NewCursor()
	cur.Logs["refs/a"] = store.LogPos{Tip: "abc", Count: 3}
	at := time.Date(2026, 11, 27, 1, 2, 3, 0, time.UTC)
	err := Update(p, func(s *State) error {
		s.Cursor = cur
		s.Parked["t1"] = map[string]Parked{"e1": {Attempts: 3, Reason: "boom", At: at}}
		s.Parked[""] = map[string]Parked{"e1": {Attempts: 1, Reason: "x", At: at}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.Cursor, cur) {
		t.Fatalf("cursor %+v", s.Cursor)
	}
	if len(s.Parked) != 2 || s.Parked["t1"]["e1"].Reason != "boom" || s.Parked[""]["e1"].Reason != "x" {
		t.Fatalf("parked %+v", s.Parked)
	}
	ents, _ := os.ReadDir(filepath.Dir(p))
	for _, e := range ents {
		if filepath.Ext(e.Name()) != ".json" && e.Name() != "chat.json.lock" {
			t.Fatalf("leftover %s", e.Name())
		}
	}
}

func TestAC2ConcurrentUpdate(t *testing.T) {
	p := Path(t.TempDir(), "w", "chat")
	var wg sync.WaitGroup
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				id := fmt.Sprintf("g%d-%d", g, i)
				if err := Update(p, func(s *State) error {
					s.Parked["t"] = ensure(s.Parked["t"])
					s.Parked["t"][id] = Parked{}
					return nil
				}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	s, _ := Load(p)
	if n := len(s.Parked["t"]); n != 100 {
		t.Fatalf("entries %d", n)
	}
}

func ensure(m map[string]Parked) map[string]Parked {
	if m == nil {
		return map[string]Parked{}
	}
	return m
}

func TestAC8Path(t *testing.T) {
	c := t.TempDir()
	if got, want := Path(c, "w", "chat"), filepath.Join(c, "rig", "hooks", "w", "chat.json"); got != want {
		t.Fatalf("%s", got)
	}
	if Path(c, "w", "chat") == Path(c, "x", "chat") {
		t.Fatal("workflow ignored")
	}
	if err := Update(Path(c, "w", "chat"), func(s *State) error { s.Parked["t"] = map[string]Parked{"e": {}}; return nil }); err != nil {
		t.Fatal(err)
	}
	if s, err := Load(Path(c, "w", "chat2")); s != nil || err != nil {
		t.Fatalf("chat2 read chat: %v %v", s, err)
	}
}

func TestLoadRejectsUnknownFormat(t *testing.T) {
	p := Path(t.TempDir(), "w", "chat")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"format":999,"parked":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("unknown format accepted")
	}
}

func TestLoadLockedWaitsForUpdate(t *testing.T) {
	p := Path(t.TempDir(), "w", "chat")
	if s, err := LoadLocked(p); err != nil || s != nil {
		t.Fatalf("missing = %v, %v", s, err)
	}
	if err := Update(p, func(s *State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockFile(p + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if s, err := LoadLocked(p); err != nil || s == nil {
			t.Errorf("LoadLocked = %v, %v", s, err)
		}
	}()
	select {
	case <-done:
		t.Fatal("LoadLocked read while the lock was held")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	<-done
}

package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AindriuB/rig/internal/store"
)

// AC5: the six phase-2 commands dispatch to stubs that exit 2.
func TestPhase2StubsNotBuilt(t *testing.T) {
	for _, cmd := range []string{"task", "finding", "note", "workflow", "next", "context"} {
		r := rig(t, t.TempDir(), nil, nil, cmd, "anything")
		if r.code != 2 || r.out != "" || r.errOut != "rig "+cmd+": not built yet\n" {
			t.Fatalf("%s: %+v", cmd, r)
		}
		if h := rig(t, t.TempDir(), nil, nil, "help"); !strings.Contains(h.out, "rig "+cmd+" ") {
			t.Fatalf("usage does not list %s", cmd)
		}
	}
}

func TestOpenStore(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	repo, _ := newRepo(t)
	fixed := time.Date(2026, 11, 22, 10, 4, 5, 0, time.UTC)
	newTask := store.NewTask{Slug: "s", Initial: "todo"}

	for _, tc := range []struct {
		env       map[string]string
		userName  string
		wantActor string
	}{
		{map[string]string{"RIG_ACTOR": "agent-7"}, "Ada", "agent-7"},
		{nil, "Ada", "Ada"},
		{nil, "", "unknown"},
	} {
		if tc.userName != "" {
			gitc(t, repo, "config", "user.name", tc.userName)
		} else {
			gitc(t, repo, "config", "--unset", "user.name")
		}
		a := &app{getenv: func(k string) string { return tc.env[k] }, now: func() time.Time { return fixed }}
		s, err := a.openStore(ctx, repo)
		if err != nil {
			t.Fatal(err)
		}
		task, err := s.CreateTask(ctx, newTask)
		if err != nil {
			t.Fatal(err)
		}
		evs, err := s.Events(ctx, task.ID)
		if err != nil || evs[0].Actor != tc.wantActor || !evs[0].Time.Equal(fixed) {
			t.Fatalf("actor %q time %v, want %q; err %v", evs[0].Actor, evs[0].Time, tc.wantActor, err)
		}
	}

	a := &app{getenv: func(string) string { return "" }, now: time.Now}
	if _, err := a.openStore(ctx, t.TempDir()); err == nil {
		t.Fatal("openStore outside a repository succeeded")
	}
}

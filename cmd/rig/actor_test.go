package main

import (
	"os"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/tracker"
)

// eventsByTask returns every task's events, keyed by task ID.
func (e *trkEnv) eventsByTask() map[string][]store.Event {
	e.t.Helper()
	st := e.st()
	tasks, err := st.Tasks(ctx, store.TaskFilter{})
	if err != nil {
		e.t.Fatal(err)
	}
	m := map[string][]store.Event{}
	for _, tk := range tasks {
		evs, err := st.Events(ctx, tk.ID)
		if err != nil {
			e.t.Fatal(err)
		}
		m[tk.ID] = evs
	}
	return m
}

// newEvents returns the events that were not in before.
func (e *trkEnv) newEvents(before map[string][]store.Event) []store.Event {
	e.t.Helper()
	var out []store.Event
	for id, evs := range e.eventsByTask() {
		out = append(out, evs[len(before[id]):]...)
	}
	return out
}

func actorEnv(t *testing.T) (*trkEnv, *scripted) {
	t.Helper()
	e := newTrkEnv(t, ghCfg())
	e.envVars["RIG_WORKFLOW"] = taskWorkflow(t)
	e.envVars["RIG_ACTOR"] = "env-actor"
	s := &scripted{issues: map[string]tracker.Issue{
		"12": issue("12", "Twelve"), "20": issue("20", "Twenty"), "30": issue("30", "Thirty"),
	}}
	e.tr = s
	return e, s
}

// AC1: every store-writing command stamps --actor, and --role, over RIG_ACTOR.
func TestActorFlagStampsEveryWritingCommand(t *testing.T) {
	for _, flag := range []string{"--actor", "--role"} {
		e, s := actorEnv(t)
		x, y := e.mkTask("x"), e.mkTask("y")
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("RIG_TASK_EDIT_HELPER", "1")
		tmp := t.TempDir()
		t.Setenv("TMPDIR", tmp)
		t.Setenv("TEMP", tmp)
		t.Setenv("TMP", tmp)
		e.envVars["EDITOR"] = exe
		e.envVars["RIG_EDITOR_NO_TTY"] = "1"
		t.Setenv("RIG_EDIT_FROM", "## owner\n\n")
		t.Setenv("RIG_EDIT_TO", "## owner\n\nann")

		var f struct{ Finding struct{ ID string } }
		if code := e.json(&f, "finding", "add", x.Short, "--severity", "P1", "--summary", "s"); code != 0 {
			t.Fatalf("finding add setup: %d %s", code, e.err.String())
		}
		cases := []struct {
			name string
			args []string
			pre  func()
		}{
			{"task new", []string{"task", "new", "--slug", "n1", "--title", "N"}, nil},
			{"task new --from", []string{"task", "new", "--from", "20"}, nil},
			{"task edit", []string{"task", "edit", x.Short}, nil},
			{"task set", []string{"task", "set", x.Short, "--field", "owner=bob"}, nil},
			{"task count", []string{"task", "count", x.Short, "rounds"}, nil},
			{"task move", []string{"task", "move", x.Short, "stopped", "--reason", "r"}, nil},
			{"task parent", []string{"task", "parent", x.Short, y.Short}, nil},
			{"task link", []string{"task", "link", x.Short, "12"}, nil},
			{"tracker refresh", []string{"tracker", "refresh", x.Short}, func() {
				is := s.issues["12"]
				is.Title, is.Rev = "Twelve, renamed", "r2"
				s.issues["12"] = is
			}},
			{"task unlink", []string{"task", "unlink", x.Short}, nil},
			{"finding add", []string{"finding", "add", x.Short, "--severity", "P1", "--summary", "t"}, nil},
			{"finding set", []string{"finding", "set", x.Short, f.Finding.ID, "--status", "fixed"}, nil},
			{"note add", []string{"note", "add", x.Short, "--kind", "decision", "--text", "t"}, nil},
			{"tracker pull", []string{"tracker", "pull", "30"}, nil},
		}
		for _, tc := range cases {
			if tc.pre != nil {
				tc.pre()
			}
			before := e.eventsByTask()
			args := append(append([]string{}, tc.args...), flag, "flag-actor")
			if code := e.run(args...); code != 0 {
				t.Fatalf("%s %s: exit %d: %s", tc.name, flag, code, e.err.String())
			}
			evs := e.newEvents(before)
			if len(evs) == 0 {
				t.Fatalf("%s: wrote no event", tc.name)
			}
			for _, ev := range evs {
				if ev.Actor != "flag-actor" {
					t.Fatalf("%s %s: event %s has actor %q", tc.name, flag, ev.Kind, ev.Actor)
				}
			}
			// The =value form works too.
			if tc.name == "note add" {
				before = e.eventsByTask()
				if code := e.run(append(append([]string{}, tc.args...), flag+"=eq-actor")...); code != 0 {
					t.Fatalf("%s: exit %d", tc.name, code)
				}
				if evs := e.newEvents(before); len(evs) != 1 || evs[0].Actor != "eq-actor" {
					t.Fatalf("= form: %+v", evs)
				}
			}
		}
	}
}

// AC2 and AC3: the alias, a conflict, and bad values; a refusal writes nothing.
func TestActorFlagValues(t *testing.T) {
	e, _ := actorEnv(t)
	x := e.mkTask("x")
	note := []string{"note", "add", x.Short, "--kind", "decision", "--text", "t"}
	run := func(extra ...string) (int, []store.Event) {
		before := e.eventsByTask()
		code := e.run(append(append([]string{}, note...), extra...)...)
		return code, e.newEvents(before)
	}
	if code, evs := run("--actor", "A", "--role", "A"); code != 0 || len(evs) != 1 || evs[0].Actor != "A" {
		t.Fatalf("same value: exit %d %+v", code, evs)
	}
	if code, evs := run("--actor", " padded ", "--role=padded"); code != 0 || len(evs) != 1 || evs[0].Actor != "padded" {
		t.Fatalf("trimmed: exit %d %+v", code, evs)
	}
	for _, extra := range [][]string{
		{"--actor", "A", "--role", "B"}, {"--role", "B", "--actor", "A"},
		{"--actor", ""}, {"--role", ""}, {"--actor", "   "}, {"--actor=\t"},
		{"--actor", "a\x00b"}, {"--role", "a\nb"}, {"--actor", "a\x7fb"}, {"--actor", "ok", "--role", ""},
		{"--actor"},
	} {
		if code, evs := run(extra...); code != 2 || len(evs) != 0 {
			t.Fatalf("%q: exit %d, %d events, stderr %q", extra, code, len(evs), e.err.String())
		}
	}
}

// AC4: a read-only command rejects the flag, as it rejects any unknown option.
func TestActorFlagReadOnlyCommandsReject(t *testing.T) {
	e, _ := actorEnv(t)
	x := e.mkTask("x")
	for _, args := range [][]string{
		{"task", "show", x.Short}, {"task", "list"}, {"finding", "list", x.Short}, {"status"},
		{"note", "list", x.Short}, {"task", "log", x.Short}, {"task", "is", x.Short, "open"}, {"next"},
	} {
		for _, flag := range []string{"--actor", "--role"} {
			if code := e.run(append(append([]string{}, args...), flag, "A")...); code != 2 {
				t.Fatalf("%v %s: exit %d, want 2", args, flag, code)
			}
			if !strings.Contains(e.err.String(), "rig ") {
				t.Fatalf("%v: stderr %q", args, e.err.String())
			}
		}
	}
}

// AC5: with no flag, RIG_ACTOR still decides.
func TestActorWithoutFlagIsUnchanged(t *testing.T) {
	e, _ := actorEnv(t)
	x := e.mkTask("x")
	before := e.eventsByTask()
	if code := e.run("note", "add", x.Short, "--kind", "decision", "--text", "t"); code != 0 {
		t.Fatal(code)
	}
	if evs := e.newEvents(before); len(evs) != 1 || evs[0].Actor != "env-actor" {
		t.Fatalf("%+v", evs)
	}
}

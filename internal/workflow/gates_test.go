package workflow

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/store"
)

var ctx = context.Background()

// fakeRunner records what it ran and fails when fail is set.
type fakeRunner struct {
	argv  []string
	stdin string
	fail  error
}

func (f *fakeRunner) Run(_ context.Context, argv []string, stdin []byte) error {
	f.argv, f.stdin = argv, string(stdin)
	return f.fail
}

func TestGates(t *testing.T) {
	w := fixture(t)
	w.Fields["notes"] = Field{Type: TypeText}

	tasks := map[string]store.Task{
		"t-dep1": {ID: "1", Short: "t-dep1", State: "merged"},
		"t-dep2": {ID: "2", Short: "t-dep2", State: "ready", Shas: map[string]string{"ready": "r1"}},
	}
	heads := map[string]string{"t-main": "h1", "t-dep2": "r1"}
	env := func() Env {
		return Env{
			HeadSha: func(_ context.Context, t store.Task) (string, error) {
				if t.Short == "t-broken" {
					return "", errors.New("boom")
				}
				return heads[t.Short], nil
			},
			Task: func(_ context.Context, ref string) (store.Task, error) {
				if t, ok := tasks[ref]; ok {
					return t, nil
				}
				return store.Task{}, store.ErrNotFound
			},
		}
	}
	base := func() store.Task {
		return store.Task{
			ID: "0", Short: "t-main", Slug: "main", State: "reviewed", Title: "T",
			Fields:   map[string]any{"notes": "x", "depends": []any{"t-dep1"}},
			Shas:     map[string]string{"verified": "v1"},
			Counters: map[string]int{"rounds": 3},
		}
	}
	open := []store.Finding{
		{Short: "f-1", Severity: "P1", Status: store.StatusFixed},
		{Short: "f-2", Severity: "P3", Status: store.StatusOpen},
		{Short: "f-3", Severity: "NOTE", Status: store.StatusOpen},
	}
	blocking := append(open, store.Finding{Short: "f-4", Severity: "P2", Status: store.StatusOpen})

	cases := []struct {
		name     string
		gate     string
		edit     func(*store.Task, *Env, *Args)
		findings []store.Finding
		ok       bool
		msg      string // part of the message when !ok
	}{
		{"noOpenFindings pass", `{"noOpenFindings": {"severity": ["P1", "P2"]}}`, nil, open, true, ""},
		{"noOpenFindings fail", `{"noOpenFindings": {"severity": ["P1", "P2"]}}`, nil, blocking, false, "f-4 (P2)"},
		{"noOpenFindings one severity", `{"noOpenFindings": {"severity": "P3"}}`, nil, open, false, "f-2 (P3)"},

		{"shaIsHead pass", `{"shaIsHead": true}`, func(_ *store.Task, _ *Env, a *Args) { a.Sha = "h1" }, nil, true, ""},
		{"shaIsHead other sha", `{"shaIsHead": true}`, func(_ *store.Task, _ *Env, a *Args) { a.Sha = "h0" }, nil, false, "not the branch head"},
		{"shaIsHead no sha", `{"shaIsHead": true}`, nil, nil, false, "no sha"},
		{"shaIsHead no branch", `{"shaIsHead": true}`, func(t *store.Task, _ *Env, a *Args) { t.Short, a.Sha = "t-none", "h1" }, nil, false, "does not exist"},
		{"shaIsHead head error", `{"shaIsHead": true}`, func(t *store.Task, _ *Env, a *Args) { t.Short, a.Sha = "t-broken", "h1" }, nil, false, "boom"},
		{"shaIsHead no env", `{"shaIsHead": true}`, func(_ *store.Task, e *Env, a *Args) { e.HeadSha, a.Sha = nil, "h1" }, nil, false, "not available"},

		{"shaIs pass", `{"shaIs": "verified"}`, func(_ *store.Task, _ *Env, a *Args) { a.Sha = "v1" }, nil, true, ""},
		{"shaIs other sha", `{"shaIs": "verified"}`, func(_ *store.Task, _ *Env, a *Args) { a.Sha = "v2" }, nil, false, "not the sha recorded"},
		{"shaIs none recorded", `{"shaIs": "ready"}`, func(_ *store.Task, _ *Env, a *Args) { a.Sha = "v1" }, nil, false, "no sha recorded"},

		{"dependsIn pass", `{"dependsIn": ["merged"]}`, nil, nil, true, ""},
		{"dependsIn no depends", `{"dependsIn": ["merged"]}`, func(t *store.Task, _ *Env, _ *Args) { delete(t.Fields, "depends") }, nil, true, ""},
		{"dependsIn fail", `{"dependsIn": ["merged"]}`, func(t *store.Task, _ *Env, _ *Args) { t.Fields["depends"] = []any{"t-dep1", "t-dep2"} }, nil, false, "t-dep2 is ready"},
		{"dependsIn sha-bound, head unmoved", `{"dependsIn": ["ready"]}`, func(t *store.Task, _ *Env, _ *Args) { t.Fields["depends"] = []string{"t-dep2"} }, nil, true, ""},
		{"dependsIn sha-bound, head moved", `{"dependsIn": ["ready"]}`, func(t *store.Task, _ *Env, _ *Args) {
			t.Fields["depends"] = []string{"t-dep2"}
			heads["t-dep2"] = "r2"
		}, nil, false, "t-dep2 is implementing"},
		{"dependsIn missing task", `{"dependsIn": ["merged"]}`, func(t *store.Task, _ *Env, _ *Args) { t.Fields["depends"] = []any{"t-nope"} }, nil, false, "not found"},
		{"dependsIn bad field", `{"dependsIn": ["merged"]}`, func(t *store.Task, _ *Env, _ *Args) { t.Fields["depends"] = "t-dep1" }, nil, false, "want a list"},

		{"fieldSet pass", `{"fieldSet": "notes"}`, nil, nil, true, ""},
		{"fieldSet title", `{"fieldSet": "title"}`, nil, nil, true, ""},
		{"fieldSet blank", `{"fieldSet": "notes"}`, func(t *store.Task, _ *Env, _ *Args) { t.Fields["notes"] = " " }, nil, false, "not set"},
		{"fieldSet empty list", `{"fieldSet": "depends"}`, func(t *store.Task, _ *Env, _ *Args) { t.Fields["depends"] = []any{} }, nil, false, "not set"},
		{"fieldSet absent", `{"fieldSet": "owns"}`, nil, nil, false, "not set"},

		{"counterBelow pass", `{"counterBelow": {"counter": "rounds", "max": 4}}`, nil, nil, true, ""},
		{"counterBelow at max", `{"counterBelow": {"counter": "rounds", "max": 3}}`, nil, nil, false, "is 3, not below 3"},
		{"counterBelow unset", `{"counterBelow": {"counter": "other", "max": 1}}`, nil, nil, true, ""},

		{"command pass", `{"command": ["check", "{short}"]}`, func(_ *store.Task, e *Env, _ *Args) { e.Runner = &fakeRunner{} }, nil, true, ""},
		{"command fail", `{"command": ["check"]}`, func(_ *store.Task, e *Env, _ *Args) { e.Runner = &fakeRunner{fail: errors.New("exited 3")} }, nil, false, "exited 3"},
		{"command no runner", `{"command": ["check"]}`, nil, nil, false, "no runner"},

		{"malformed gate", `{"counterBelow": {"counter": "rounds"}}`, nil, nil, false, "invalid gate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer func() { heads["t-dep2"] = "r1" }()
			var g Gate
			if err := g.UnmarshalJSON([]byte(c.gate)); err != nil {
				t.Fatal(err)
			}
			task, e, args := base(), env(), Args{}
			if c.edit != nil {
				c.edit(&task, &e, &args)
			}
			tr := Transition{From: StateList{"reviewed"}, To: "verified", Gates: []Gate{g}}
			res := w.Check(ctx, e, task, c.findings, tr, args)
			if len(res) != 1 || res[0].Gate != g.Kind && g.problem == "" {
				t.Fatalf("results = %+v", res)
			}
			if res[0].OK != c.ok || !c.ok && !strings.Contains(res[0].Message, c.msg) {
				t.Fatalf("result = %+v, want ok %v with %q", res[0], c.ok, c.msg)
			}
		})
	}
}

func TestCheckEvaluatesEveryGate(t *testing.T) {
	w := fixture(t)
	var a, b Gate
	_ = a.UnmarshalJSON([]byte(`{"counterBelow": {"counter": "rounds", "max": 1}}`))
	_ = b.UnmarshalJSON([]byte(`{"fieldSet": "owns"}`))
	task := store.Task{Fields: map[string]any{"owns": []any{"a/"}}, Counters: map[string]int{"rounds": 1}}
	res := w.Check(ctx, Env{}, task, nil, Transition{From: StateList{"reviewed"}, Gates: []Gate{a, b}}, Args{})
	if len(res) != 2 || res[0].OK || !res[1].OK || res[0].Gate != GateCounterBelow || res[1].Gate != GateFieldSet {
		t.Fatalf("results = %+v", res)
	}
}

func TestCommandGateInput(t *testing.T) {
	w := fixture(t)
	var g Gate
	_ = g.UnmarshalJSON([]byte(`{"command": ["check", "{short}", "{state}", "{sha}", "{reason}", "--id={id}"]}`))
	r := &fakeRunner{}
	task := store.Task{ID: "01X", Short: "t-1", Slug: "s", State: "reviewed"}
	args := Args{Sha: "abc", Reason: "a b; rm -rf /"}
	res := w.Check(ctx, Env{Runner: r}, task, nil, Transition{From: StateList{"reviewed"}, Gates: []Gate{g}}, args)
	if !res[0].OK {
		t.Fatalf("result = %+v", res[0])
	}
	want := []string{"check", "t-1", "reviewed", "abc", "a b; rm -rf /", "--id={id}"}
	if strings.Join(r.argv, "|") != strings.Join(want, "|") {
		t.Fatalf("argv = %q, want %q", r.argv, want)
	}
	if !strings.Contains(r.stdin, `"short":"t-1"`) {
		t.Fatalf("stdin = %s", r.stdin)
	}
}

func TestCommandGateGetsTheView(t *testing.T) {
	w := fixture(t)
	var g Gate
	_ = g.UnmarshalJSON([]byte(`{"command": ["check"]}`))
	r := &fakeRunner{}
	head := func(context.Context, store.Task) (string, error) { return "s2", nil }
	task := store.Task{Short: "t-1", State: "ready", StateSha: "s1", Shas: map[string]string{"ready": "s1"}}
	res := w.Check(ctx, Env{Runner: r, HeadSha: head}, task, nil, Transition{From: StateList{"implementing"}, Gates: []Gate{g}}, Args{})
	if !res[0].OK {
		t.Fatalf("result = %+v", res[0])
	}
	if !strings.Contains(r.stdin, `"state":"implementing","recordedState":"ready"`) || !strings.Contains(r.stdin, `"headSha":"s2"`) {
		t.Fatalf("stdin = %s", r.stdin)
	}
	// A head that cannot be read fails the gate rather than guessing.
	broken := func(context.Context, store.Task) (string, error) { return "", errors.New("boom") }
	res = w.Check(ctx, Env{Runner: r, HeadSha: broken}, task, nil, Transition{From: StateList{"implementing"}, Gates: []Gate{g}}, Args{})
	if res[0].OK || !strings.Contains(res[0].Message, "boom") {
		t.Fatalf("broken head: %+v", res[0])
	}
}

func TestCheckRefusesInvalidWorkflow(t *testing.T) {
	w := fixture(t)
	w.Initial = "nowhere"
	var g Gate
	_ = g.UnmarshalJSON([]byte(`{"counterBelow": {"counter": "rounds", "max": 9}}`))
	res := w.Check(ctx, Env{}, store.Task{}, nil, Transition{From: StateList{"reviewed"}, Gates: []Gate{g, g}}, Args{})
	if len(res) != 2 || res[0].OK || res[1].OK || !strings.Contains(res[0].Message, "is invalid") || !strings.Contains(res[0].Message, "initial") {
		t.Fatalf("results = %+v", res)
	}
	// A transition with no gates still gets a failing result.
	res = w.Check(ctx, Env{}, store.Task{}, nil, Transition{From: StateList{"ci_green"}, To: "ready"}, Args{})
	if len(res) != 1 || res[0].OK || res[0].Gate != "workflow" {
		t.Fatalf("no gates: %+v", res)
	}
}

func TestCheckHistoryGate(t *testing.T) {
	w := fixture(t)
	tr := w.AllTransitions[1]
	clean := w.Check(ctx, Env{}, store.Task{State: tr.From[0]}, nil, tr, Args{})
	for _, r := range clean {
		if r.Gate == GateHistory {
			t.Fatalf("history gate on a clean task: %+v", clean)
		}
	}
	got := w.Check(ctx, Env{}, store.Task{State: tr.From[0], Skipped: 3}, nil, tr, Args{})
	if len(got) != len(clean)+1 || got[0].Gate != "history" || got[0].OK ||
		!strings.Contains(got[0].Message, "3") || !strings.Contains(got[0].Message, "upgrade rig") {
		t.Fatalf("got %+v", got)
	}
	if !reflect.DeepEqual(got[1:], clean) {
		t.Fatalf("own gates changed: %+v vs %+v", got[1:], clean)
	}
}

func TestDependsInDegradedDependency(t *testing.T) {
	w := fixture(t)
	var g Gate
	if err := g.UnmarshalJSON([]byte(`{"dependsIn": ["merged"]}`)); err != nil {
		t.Fatal(err)
	}
	env := Env{Task: func(context.Context, string) (store.Task, error) {
		return store.Task{State: "merged", Skipped: 1}, nil
	}}
	task := store.Task{Fields: map[string]any{"depends": []any{"t-dep1"}}}
	ok, msg := w.check(ctx, env, task, nil, Transition{}, Args{}, g)
	if ok || !strings.Contains(msg, "t-dep1") || !strings.Contains(msg, "upgrade rig") {
		t.Fatalf("ok=%v msg=%q", ok, msg)
	}
}

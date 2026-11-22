package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/store/memstore"
)

func TestLoad(t *testing.T) {
	w, src, err := Load("basic")
	if err != nil || src.Preset != "basic" || w.Name != "basic" || w.Initial != "open" {
		t.Fatalf("Load(basic) = %+v, %v, %v", w, src, err)
	}
	if fmt.Sprint(Presets()) != "[basic]" {
		t.Fatalf("Presets = %v", Presets())
	}
	if s := w.Findings.Severities; strings.Join(s, ",") != "blocking,info" {
		t.Fatalf("basic severities = %v", s)
	}

	abs, err := filepath.Abs(filepath.Join("testdata", "claude-kit-loop.json"))
	if err != nil {
		t.Fatal(err)
	}
	w, src, err = Load(abs)
	if err != nil || src.Path != abs || src.Preset != "" || w.Name != "claude-kit-loop" {
		t.Fatalf("Load(fixture) = %v, %v", src, err)
	}

	for _, bad := range []string{"", "nope", filepath.Join("testdata", "claude-kit-loop.json"), "./basic", filepath.Join(t.TempDir(), "missing.json")} {
		if _, _, err := Load(bad); err == nil {
			t.Errorf("Load(%q) succeeded", bad)
		}
	}

	// A file that parses but breaks rules comes back with its problems.
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"name": "x", "version": 1, "initial": "a", "states": {"a": {}, "b": {}}, "transitions": []}`), 0o600); err != nil {
		t.Fatal(err)
	}
	w, _, err = Load(bad)
	var inv *InvalidError
	if !errors.As(err, &inv) || w == nil || len(inv.Problems) != 1 || inv.Problems[0].Path != "states.b" {
		t.Fatalf("Load(invalid) = %v, %v", w, err)
	}
}

// harness drives tasks through a workflow on memstore the way a command
// would: gates are checked outside Update, because a gate may read other
// tasks and memstore's callback must not call the store, and Update then
// refuses if the task moved in between.
type harness struct {
	t     *testing.T
	w     *Workflow
	s     *memstore.Store
	heads map[string]string // short id -> branch head
	ran   [][]string        // every hook argv, in order
}

func newHarness(t *testing.T, w *Workflow) *harness {
	return &harness{t: t, w: w, s: memstore.New(store.Options{Actor: "test"}), heads: map[string]string{}}
}

func (h *harness) env() Env {
	return Env{
		HeadSha: func(_ context.Context, t store.Task) (string, error) { return h.heads[t.Short], nil },
		Task:    func(ctx context.Context, ref string) (store.Task, error) { return h.s.Task(ctx, ref) },
	}
}

func (h *harness) create(slug string, fields map[string]any) store.Task {
	h.t.Helper()
	task, err := h.s.CreateTask(ctx, store.NewTask{
		Slug: slug, Title: slug, Fields: fields,
		Workflow: store.WorkflowRef{Name: h.w.Name, Version: h.w.Version}, Initial: h.w.Initial,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return task
}

// state returns the task's effective state.
func (h *harness) state(ref string) string {
	h.t.Helper()
	task, err := h.s.Task(ctx, ref)
	if err != nil {
		h.t.Fatal(err)
	}
	return h.w.Effective(task, h.heads[task.Short])
}

// move takes the first transition from the task's effective state to the
// state to whose gates pass, and runs its hooks.
func (h *harness) move(ref, to string, args Args) error {
	h.t.Helper()
	task, err := h.s.Task(ctx, ref)
	if err != nil {
		return err
	}
	findings, err := h.s.Findings(ctx, ref)
	if err != nil {
		return err
	}
	from := h.w.Effective(task, h.heads[task.Short])
	var events []store.Event
	var refused []string
	for _, tr := range h.w.Transitions(from) {
		target := tr.To
		if target == Previous {
			target = task.Previous
		}
		if target != to {
			continue
		}
		var failed []string
		for _, r := range h.w.Check(ctx, h.env(), task, findings, tr, args) {
			if !r.OK {
				failed = append(failed, r.Gate+": "+r.Message)
			}
		}
		if len(failed) > 0 {
			refused = append(refused, failed...)
			continue
		}
		if events, err = h.w.Events(task, h.heads[task.Short], tr, target, args); err != nil {
			return err
		}
		break
	}
	if events == nil {
		if refused == nil {
			return fmt.Errorf("no transition %s → %s", from, to)
		}
		return fmt.Errorf("%s → %s refused: %s", from, to, strings.Join(refused, "; "))
	}
	after, err := h.s.Update(ctx, ref, func(now store.Task, _ []store.Finding) ([]store.Event, error) {
		if now.State != task.State || now.Updated != task.Updated {
			return nil, errors.New("task moved meanwhile")
		}
		return events, nil
	})
	if err != nil {
		return err
	}
	log, err := h.s.Events(ctx, ref)
	if err != nil {
		return err
	}
	for i := len(log) - 1; i >= 0; i-- {
		if log[i].Kind == store.KindTransition {
			r := &fakeRunner{}
			if errs := h.w.RunHooks(ctx, recorder{h, r}, log[i], after, h.heads[after.Short]); errs != nil {
				return errors.Join(errs...)
			}
			break
		}
	}
	return nil
}

// recorder keeps every argv a hook ran.
type recorder struct {
	h *harness
	r *fakeRunner
}

func (rc recorder) Run(ctx context.Context, argv []string, stdin []byte) error {
	rc.h.ran = append(rc.h.ran, argv)
	return rc.r.Run(ctx, argv, stdin)
}

func (h *harness) must(ref, to string, args Args) {
	h.t.Helper()
	if err := h.move(ref, to, args); err != nil {
		h.t.Fatal(err)
	}
	if got := h.state(ref); got != to {
		h.t.Fatalf("after moving to %s the state is %s", to, got)
	}
}

func (h *harness) refused(ref, to string, args Args, why string) {
	h.t.Helper()
	err := h.move(ref, to, args)
	if err == nil || !strings.Contains(err.Error(), why) {
		h.t.Fatalf("move to %s: err = %v, want %q", to, err, why)
	}
}

func (h *harness) update(ref string, kind string, data any) {
	h.t.Helper()
	if _, err := h.s.Update(ctx, ref, func(store.Task, []store.Finding) ([]store.Event, error) {
		ev, err := store.NewEvent(kind, data)
		return []store.Event{ev}, err
	}); err != nil {
		h.t.Fatal(err)
	}
}

func TestWalkClaudeKitLoop(t *testing.T) {
	abs, _ := filepath.Abs(filepath.Join("testdata", "claude-kit-loop.json"))
	w, _, err := Load(abs)
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, w)
	dep := h.create("dep", nil)
	main := h.create("main", map[string]any{"depends": []string{dep.Short}, "owns": []string{"internal/x/**"}})
	ref := main.Short
	if got := w.BranchFor(main); got != "task/"+main.Short+"-main" {
		t.Fatalf("branch = %q", got)
	}

	// planned → implementing waits on the dependency.
	h.refused(ref, "implementing", Args{}, "is planned")
	h.update(dep.Short, store.KindTransition, store.TransitionData{From: "planned", To: "merged"})
	h.must(ref, "implementing", Args{})

	// implementing → reviewed waits on blocking findings; a P3 does not block.
	h.update(ref, store.KindFinding, store.FindingData{Severity: "P1", Summary: "bug"})
	h.update(ref, store.KindFinding, store.FindingData{Severity: "P3", Summary: "nit"})
	h.refused(ref, "reviewed", Args{}, "(P1)")
	findings, _ := h.s.Findings(ctx, ref)
	h.update(ref, store.KindFindingStatus, store.FindingStatusData{Finding: findings[0].ID, Status: store.StatusFixed})
	h.must(ref, "reviewed", Args{})

	// A review round back to implementing counts.
	h.must(ref, "implementing", Args{})
	h.must(ref, "reviewed", Args{})
	if task, _ := h.s.Task(ctx, ref); task.Counters["rounds"] != 1 {
		t.Fatalf("rounds = %d", task.Counters["rounds"])
	}

	// reviewed → verified takes the head sha.
	h.heads[ref] = "s1"
	h.refused(ref, "verified", Args{Sha: "s0"}, "not the branch head")
	h.must(ref, "verified", Args{Sha: "s1"})
	h.must(ref, "pr_open", Args{PR: 12})
	h.refused(ref, "ci_green", Args{Sha: "s0"}, "not the sha recorded entering verified")
	h.must(ref, "ci_green", Args{Sha: "s1"})
	h.must(ref, "ready", Args{})
	if task, _ := h.s.Task(ctx, ref); task.PR != 12 || task.Shas["ready"] != "s1" {
		t.Fatalf("task = pr %d, shas %v", task.PR, task.Shas)
	}

	// Stop, with a hook, and come back to the state before.
	h.refused(ref, "stopped", Args{}, "takes a reason")
	h.must(ref, "stopped", Args{Reason: "wait; for 'ops'"})
	if len(h.ran) != 1 || strings.Join(h.ran[0], "|") != "notify-user|"+main.Short+"|wait; for 'ops'" {
		t.Fatalf("hooks ran %q", h.ran)
	}
	h.must(ref, "ready", Args{})

	// A new commit moves the task off ready: it is implementing again, and
	// cannot be merged until it goes round the loop at the new sha.
	h.heads[ref] = "s2"
	if got := h.state(ref); got != "implementing" {
		t.Fatalf("after a new commit the state is %s", got)
	}
	h.refused(ref, "merged", Args{Sha: "s2"}, "no transition implementing → merged")
	h.must(ref, "reviewed", Args{})
	h.must(ref, "verified", Args{Sha: "s2"})
	h.must(ref, "pr_open", Args{PR: 12})
	h.must(ref, "ci_green", Args{Sha: "s2"})
	h.must(ref, "ready", Args{})
	h.must(ref, "merged", Args{Sha: "s2"})
	if got := w.Transitions("merged"); len(got) != 0 {
		t.Fatalf("transitions out of merged: %v", got)
	}
	if len(h.ran) != 1 {
		t.Fatalf("hooks ran %q", h.ran)
	}
}

func TestWalkBasic(t *testing.T) {
	w, _, err := Load("basic")
	if err != nil {
		t.Fatal(err)
	}
	if ps := w.Validate(); len(ps) != 0 {
		t.Fatal(ps)
	}
	h := newHarness(t, w)
	ref := h.create("job", nil).Short
	h.refused(ref, "done", Args{}, "no transition open → done")
	h.must(ref, "active", Args{})
	h.must(ref, "stopped", Args{Reason: "blocked"})
	h.must(ref, "active", Args{})
	h.must(ref, "done", Args{})
	h.refused(ref, "stopped", Args{Reason: "late"}, "no transition done → stopped")
	if w.Blocking("blocking") || w.Blocking("info") {
		t.Fatal("basic has no gates, so no severity blocks")
	}
}

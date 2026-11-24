package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/forge"
	forgefake "github.com/AindriuB/rig/internal/forge/fake"
	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/tracker"
	"github.com/AindriuB/rig/internal/tracker/fake"
)

// reportWorkflow maps doing, review and done to tracker statuses; todo has no
// entry.
func reportWorkflow(t *testing.T) string {
	t.Helper()
	wf := `{
  "name": "rw", "version": 3, "initial": "todo",
  "states": {
    "todo":   { "action": "start" },
    "doing":  { "action": "review" },
    "review": { "action": "merge" },
    "done":   { "final": true }
  },
  "transitions": [
    { "from": "todo", "to": "doing" },
    { "from": "doing", "to": "review", "takes": "pr" },
    { "from": "review", "to": "done" }
  ],
  "findings": { "severities": ["P1"] },
  "counters": ["rounds"],
  "tracker": { "report": { "status": { "doing": "In Progress", "review": "In Review", "done": "Done" } } }
}`
	path := filepath.Join(t.TempDir(), "rw.json")
	if err := os.WriteFile(path, []byte(wf), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// reportRec records every write attempt that reaches the tracker, in order,
// whether or not the fake takes it.
type reportRec struct {
	tracker.Tracker
	log        []string
	commentErr error
	shoutTo    bool // report the reached status in upper case
}

func (r *reportRec) SetStatus(ctx context.Context, key, status string) (tracker.StatusResult, error) {
	r.log = append(r.log, "status "+status)
	res, err := r.Tracker.SetStatus(ctx, key, status)
	if r.shoutTo {
		res.To = strings.ToUpper(res.To)
	}
	return res, err
}

func (r *reportRec) Comment(ctx context.Context, key string, c tracker.Comment) (tracker.CommentResult, error) {
	r.log = append(r.log, "comment")
	if r.commentErr != nil {
		return tracker.CommentResult{}, r.commentErr
	}
	return r.Tracker.Comment(ctx, key, c)
}

type reportEnv struct {
	*trkEnv
	fk   *fake.Tracker
	rec  *reportRec
	task string // the linked task's short id
	prs  *forgefake.Forge
}

func newReportEnv(t *testing.T, caps tracker.Caps, statuses ...string) *reportEnv {
	t.Helper()
	e := &reportEnv{trkEnv: newTrkEnv(t, ghCfg())}
	e.envVars["RIG_WORKFLOW"] = reportWorkflow(t)
	e.fk = fake.New(caps, []tracker.Issue{issue("12", "Twelve")}, statuses)
	e.rec = &reportRec{Tracker: e.fk}
	e.tr = e.rec
	e.prs = &forgefake.Forge{PRs: []forge.PullRequest{{Number: 5, URL: "https://forge.example/acme/rig/pull/5"}}}
	e.a.forge = func(context.Context, string) (forge.Forge, error) { return e.prs, nil }
	var v struct{ Short string }
	if code := e.json(&v, "task", "new", "--from", "12"); code != 0 {
		t.Fatalf("task new --from: %d %s", code, e.err.String())
	}
	e.task = v.Short
	e.rec.log = nil
	return e
}

var reportCaps = tracker.Caps{Comment: true, SetStatus: true}

func (e *reportEnv) mustRun(args ...string) {
	e.t.Helper()
	if code := e.run(args...); code != 0 {
		e.t.Fatalf("%v: exit %d: %s", args, code, e.err.String())
	}
}

// reports returns the task's tracker-report events.
func (e *reportEnv) reports() []store.Event {
	e.t.Helper()
	var out []store.Event
	for _, evs := range e.eventsByTask() {
		for _, ev := range evs {
			if ev.Kind == store.KindTrackerReport {
				out = append(out, ev)
			}
		}
	}
	return out
}

type reportJSON struct {
	Task, Key string
	Status    *struct{ From, To string }
	Comment   *struct{ Kind, ID, Body string }
	Skipped   bool
	DryRun    bool
	Warnings  []string
}

var statuses3 = []string{"In Progress", "In Review", "Done"}

// AC1: a real run sends the status, then the comment, to the tracker, writes
// one tracker-report event, and prints the documented JSON.
func TestReportSendsStatusThenComment(t *testing.T) {
	e := newReportEnv(t, reportCaps, statuses3...)
	e.mustRun("task", "move", e.task, "doing")
	var out reportJSON
	if code := e.json(&out, "tracker", "report", e.task); code != 0 {
		t.Fatalf("exit %d: %s", code, e.err.String())
	}
	if want := []string{"status In Progress", "comment"}; !slices.Equal(e.rec.log, want) {
		t.Fatalf("calls %v, want %v", e.rec.log, want)
	}
	wantBody := "rig: task " + e.task + " is now in state doing."
	if len(e.fk.Comments) != 1 || e.fk.Comments[0].Key != "12" || e.fk.Comments[0].Comment.Body != wantBody {
		t.Fatalf("comments %+v", e.fk.Comments)
	}
	if out.Task != e.task || out.Key != "12" || out.Status == nil || out.Status.To != "In Progress" || out.Status.From != "Open" ||
		out.Comment == nil || out.Comment.Kind != "state" || out.Comment.ID != "1" || out.Skipped {
		t.Fatalf("json %+v", out)
	}
	evs := e.reports()
	if len(evs) != 1 {
		t.Fatalf("%d tracker-report events, want 1", len(evs))
	}
	var d store.TrackerReportData
	if err := json.Unmarshal(evs[0].Data, &d); err != nil {
		t.Fatal(err)
	}
	if d.Kind != "github" || d.Key != "12" || d.Status == nil || d.Status.To != "In Progress" ||
		d.Comment == nil || d.Comment.ID != "1" || d.Comment.Digest != digest(wantBody) || d.At.IsZero() {
		t.Fatalf("event %+v", d)
	}
	if strings.Contains(string(evs[0].Data), wantBody) {
		t.Fatal("the event stores the comment body")
	}
}

// AC2: a repeat is skipped with nothing sent and no event; a changed comment
// body sends only the comment; a changed state sends both.
func TestReportRepeatIsSkipped(t *testing.T) {
	e := newReportEnv(t, reportCaps, statuses3...)
	e.mustRun("task", "move", e.task, "doing")
	e.mustRun("tracker", "report", e.task, "--status", "--comment", "summary")
	if len(e.rec.log) != 2 || len(e.reports()) != 1 {
		t.Fatalf("setup: %v, %d events", e.rec.log, len(e.reports()))
	}

	var out reportJSON
	if code := e.json(&out, "tracker", "report", e.task, "--status", "--comment", "summary"); code != 0 {
		t.Fatalf("repeat: exit %d: %s", code, e.err.String())
	}
	if !out.Skipped || out.Status != nil || out.Comment != nil || len(e.rec.log) != 2 || len(e.reports()) != 1 {
		t.Fatalf("repeat not skipped: %+v calls %v events %d", out, e.rec.log, len(e.reports()))
	}
	if code := e.run("tracker", "report", e.task, "--status", "--comment", "summary"); code != 0 || !strings.Contains(e.out.String(), "skipped") {
		t.Fatalf("text: exit %d %q", code, e.out.String())
	}

	// The summary now differs (a counter); the status is unchanged.
	e.mustRun("task", "count", e.task, "rounds")
	e.rec.log = nil
	out = reportJSON{}
	if code := e.json(&out, "tracker", "report", e.task, "--status", "--comment", "summary"); code != 0 {
		t.Fatalf("exit %d: %s", code, e.err.String())
	}
	if !slices.Equal(e.rec.log, []string{"comment"}) || out.Status != nil || out.Comment == nil || len(e.reports()) != 2 {
		t.Fatalf("changed body: %+v calls %v events %d", out, e.rec.log, len(e.reports()))
	}
	if b := e.fk.Comments[len(e.fk.Comments)-1].Comment.Body; !strings.Contains(b, "Counters: rounds=1") {
		t.Fatalf("summary body %q", b)
	}

	// A new state changes both parts.
	e.mustRun("task", "move", e.task, "review", "--pr", "5")
	e.rec.log = nil
	e.mustRun("tracker", "report", e.task)
	if !slices.Equal(e.rec.log, []string{"status In Review", "comment"}) {
		t.Fatalf("changed state: %v", e.rec.log)
	}
}

// AC3: a state with no report.status entry, a tracker with no such transition
// and --comment pr with no pull request are exit 1; nothing is sent and no
// event is written.
func TestReportRefusals(t *testing.T) {
	t.Run("no mapping", func(t *testing.T) {
		e := newReportEnv(t, reportCaps, statuses3...)
		if code := e.run("tracker", "report", e.task); code != 1 || !strings.Contains(e.err.String(), "report.status") {
			t.Fatalf("exit %d: %s", code, e.err.String())
		}
		if len(e.rec.log) != 0 || len(e.reports()) != 0 {
			t.Fatalf("calls %v, events %d", e.rec.log, len(e.reports()))
		}
	})
	t.Run("no transition", func(t *testing.T) {
		e := newReportEnv(t, reportCaps, "Done") // no "In Progress"
		e.mustRun("task", "move", e.task, "doing")
		if code := e.run("tracker", "report", e.task); code != 1 || !strings.Contains(e.err.String(), "no transition") {
			t.Fatalf("exit %d: %s", code, e.err.String())
		}
		if len(e.fk.Comments) != 0 || len(e.fk.StatusCalls) != 0 || slices.Contains(e.rec.log, "comment") || len(e.reports()) != 0 {
			t.Fatalf("calls %v, events %d", e.rec.log, len(e.reports()))
		}
	})
	t.Run("no pull request", func(t *testing.T) {
		e := newReportEnv(t, reportCaps, statuses3...)
		e.mustRun("task", "move", e.task, "doing")
		if code := e.run("tracker", "report", e.task, "--status", "--comment", "pr"); code != 1 || !strings.Contains(e.err.String(), "no pull request") {
			t.Fatalf("exit %d: %s", code, e.err.String())
		}
		if len(e.rec.log) != 0 || len(e.reports()) != 0 {
			t.Fatalf("calls %v, events %d", e.rec.log, len(e.reports()))
		}
	})
}

// --comment pr sends the pull request's URL.
func TestReportCommentPR(t *testing.T) {
	e := newReportEnv(t, reportCaps, statuses3...)
	e.mustRun("task", "move", e.task, "doing")
	e.mustRun("task", "move", e.task, "review", "--pr", "5")
	e.mustRun("tracker", "report", e.task, "--comment", "pr")
	if len(e.fk.Comments) != 1 || !strings.Contains(e.fk.Comments[0].Comment.Body, "https://forge.example/acme/rig/pull/5") || len(e.fk.StatusCalls) != 0 {
		t.Fatalf("comments %+v status %+v", e.fk.Comments, e.fk.StatusCalls)
	}
}

// AC4: --dry-run prints what the next real run sends, byte for byte, and
// makes no tracker call and writes no event.
func TestReportDryRunMatchesRealRun(t *testing.T) {
	e := newReportEnv(t, reportCaps, statuses3...)
	e.mustRun("task", "move", e.task, "doing")
	e.mustRun("finding", "add", e.task, "--severity", "P1", "--summary", "null deref")
	e.mustRun("note", "add", e.task, "--kind", "decision", "--text", "use a map")

	e.mustRun("tracker", "report", e.task, "--comment", "summary", "--status", "--include", "findings", "--include", "notes", "--dry-run")
	dry := e.out.String()
	if len(e.rec.log) != 0 || len(e.reports()) != 0 {
		t.Fatalf("dry run: calls %v, events %d", e.rec.log, len(e.reports()))
	}

	e.mustRun("tracker", "report", e.task, "--comment", "summary", "--status", "--include", "findings", "--include", "notes")
	if len(e.fk.Comments) != 1 || len(e.fk.StatusCalls) != 1 {
		t.Fatalf("real run: comments %d status %d", len(e.fk.Comments), len(e.fk.StatusCalls))
	}
	want := e.task + "  12  dry run, nothing sent\nstatus: " + e.fk.StatusCalls[0].Status +
		"\ncomment (summary):\n" + e.fk.Comments[0].Comment.Body + "\n"
	if dry != want {
		t.Fatalf("dry run output\n%q\nwant\n%q", dry, want)
	}

	// A dry run after the real one applies the skip rules.
	if e.mustRun("tracker", "report", e.task, "--comment", "summary", "--status", "--include", "findings", "--include", "notes", "--dry-run"); !strings.Contains(e.out.String(), "skipped") {
		t.Fatalf("dry run after real: %q", e.out.String())
	}
}

// AC5: findings and notes are in the comment only with --include, and the
// task's title, body and fields never are.
func TestReportIncludeAndNothingElse(t *testing.T) {
	e := newReportEnv(t, reportCaps, statuses3...)
	e.mustRun("task", "move", e.task, "doing")
	e.mustRun("finding", "add", e.task, "--severity", "P1", "--summary", "null deref")
	e.mustRun("finding", "add", e.task, "--severity", "P1", "--summary", "fixed already")
	var fs []struct{ Short, Summary string }
	if code := e.json(&fs, "finding", "list", e.task); code != 0 {
		t.Fatalf("finding list: %d %s", code, e.err.String())
	}
	for _, f := range fs {
		if f.Summary == "fixed already" {
			e.mustRun("finding", "set", e.task, f.Short, "--status", "fixed")
		}
	}
	e.mustRun("note", "add", e.task, "--kind", "decision", "--text", "use a map")

	last := func() string { return e.fk.Comments[len(e.fk.Comments)-1].Comment.Body }
	e.mustRun("tracker", "report", e.task, "--comment", "summary")
	plain := last()
	for _, s := range []string{"null deref", "use a map", "Twelve", "about Twelve"} {
		if strings.Contains(plain, s) {
			t.Fatalf("body has %q without --include or at all: %q", s, plain)
		}
	}
	e.mustRun("tracker", "report", e.task, "--comment", "summary", "--include", "findings")
	if b := last(); !strings.Contains(b, "null deref") || strings.Contains(b, "fixed already") || strings.Contains(b, "use a map") ||
		!strings.Contains(b, "P1") {
		t.Fatalf("--include findings: %q", b)
	}
	e.mustRun("tracker", "report", e.task, "--comment", "summary", "--include", "notes")
	if b := last(); !strings.Contains(b, "decision: use a map") || strings.Contains(b, "null deref") {
		t.Fatalf("--include notes: %q", b)
	}
	e.mustRun("tracker", "report", e.task, "--comment", "state", "--include", "findings", "--include", "notes")
	for _, c := range e.fk.Comments {
		for _, s := range []string{"Twelve", "about Twelve", "fixed already"} {
			if strings.Contains(c.Comment.Body, s) {
				t.Fatalf("body has %q: %q", s, c.Comment.Body)
			}
		}
	}
}

// AC6: --actor and --role stamp the event; a tracker that cannot set a
// status gives a warning and the comment is still sent.
func TestReportActorAndUnsupportedStatus(t *testing.T) {
	for _, flag := range []string{"--actor", "--role"} {
		e := newReportEnv(t, reportCaps, statuses3...)
		e.mustRun("task", "move", e.task, "doing")
		e.mustRun("tracker", "report", e.task, flag, "flag-actor")
		evs := e.reports()
		if len(evs) != 1 || evs[0].Actor != "flag-actor" {
			t.Fatalf("%s: %+v", flag, evs)
		}
	}

	e := newReportEnv(t, tracker.Caps{Comment: true}, statuses3...)
	e.mustRun("task", "move", e.task, "doing")
	var out reportJSON
	if code := e.json(&out, "tracker", "report", e.task); code != 0 {
		t.Fatalf("exit %d: %s", code, e.err.String())
	}
	if len(out.Warnings) != 1 || out.Status != nil || out.Comment == nil || len(e.fk.Comments) != 1 {
		t.Fatalf("%+v comments %d", out, len(e.fk.Comments))
	}
	evs := e.reports()
	if len(evs) != 1 || strings.Contains(string(evs[0].Data), `"status"`) {
		t.Fatalf("event %+v", evs)
	}
}

// A comment that fails after the status went through records the status and
// exits 2; the next run sends only the comment.
func TestReportCommentFailureRecordsStatus(t *testing.T) {
	e := newReportEnv(t, reportCaps, statuses3...)
	e.mustRun("task", "move", e.task, "doing")
	e.rec.commentErr = errors.New("boom")
	if code := e.run("tracker", "report", e.task); code != 2 || !strings.Contains(e.err.String(), "boom") {
		t.Fatalf("exit %d: %s", code, e.err.String())
	}
	evs := e.reports()
	if len(evs) != 1 || !strings.Contains(string(evs[0].Data), `"status"`) || strings.Contains(string(evs[0].Data), `"comment"`) {
		t.Fatalf("events %+v", evs)
	}
	e.rec.commentErr = nil
	e.rec.log = nil
	e.mustRun("tracker", "report", e.task)
	if !slices.Equal(e.rec.log, []string{"comment"}) {
		t.Fatalf("retry sent %v", e.rec.log)
	}
}

// A local tracker is exit 2, as for the other tracker commands; so is a task
// that is not linked, and a bad flag.
func TestReportUsageErrors(t *testing.T) {
	e := newTrkEnv(t, nil)
	e.envVars["RIG_WORKFLOW"] = reportWorkflow(t)
	if code := e.run("tracker", "report", "x"); code != 2 || !strings.Contains(e.err.String(), "no tracker") {
		t.Fatalf("local: exit %d: %s", code, e.err.String())
	}

	r := newReportEnv(t, reportCaps, statuses3...)
	r.mustRun("task", "new", "--slug", "plain", "--title", "Plain")
	for _, args := range [][]string{
		{"tracker", "report", "plain"},
		{"tracker", "report"},
		{"tracker", "report", r.task, "--comment", "bogus"},
		{"tracker", "report", r.task, "--include", "bogus"},
		{"tracker", "report", r.task, "--status", "--include", "notes"},
	} {
		if code := r.run(args...); code != 2 {
			t.Fatalf("%v: exit %d: %s", args, code, r.err.String())
		}
	}
	if len(r.rec.log) != 0 {
		t.Fatalf("calls %v", r.rec.log)
	}
}

// F1: a relink starts the skip history fresh, so the same report is sent again.
func TestReportRelinkResetsSkipHistory(t *testing.T) {
	e := newReportEnv(t, reportCaps, statuses3...)
	e.mustRun("task", "move", e.task, "doing")
	e.mustRun("tracker", "report", e.task)
	e.mustRun("task", "unlink", e.task)
	e.mustRun("task", "link", e.task, "12")
	e.rec.log = nil
	e.mustRun("tracker", "report", e.task)
	if !slices.Equal(e.rec.log, []string{"status In Progress", "comment"}) {
		t.Fatalf("after relink sent %v", e.rec.log)
	}
}

// F2: the status skip ignores case, against the recorded status and the
// requested one.
func TestReportStatusSkipIgnoresCase(t *testing.T) {
	e := newReportEnv(t, reportCaps, statuses3...)
	e.rec.shoutTo = true
	e.mustRun("task", "move", e.task, "doing")
	e.mustRun("tracker", "report", e.task, "--status")
	e.rec.log = nil
	e.mustRun("tracker", "report", e.task, "--status")
	if len(e.rec.log) != 0 || len(e.reports()) != 1 {
		t.Fatalf("repeat sent %v, events %d", e.rec.log, len(e.reports()))
	}
}

// F3: the comment skip is per comment kind, so alternating kinds still skip a
// repeat of each.
func TestReportCommentSkipPerKind(t *testing.T) {
	e := newReportEnv(t, reportCaps, statuses3...)
	e.mustRun("task", "move", e.task, "doing")
	e.mustRun("tracker", "report", e.task, "--comment", "state")
	e.mustRun("tracker", "report", e.task, "--comment", "summary")
	e.rec.log = nil
	e.mustRun("tracker", "report", e.task, "--comment", "state")
	if len(e.rec.log) != 0 || len(e.reports()) != 2 {
		t.Fatalf("state repeat sent %v, events %d", e.rec.log, len(e.reports()))
	}
}

// F4: a dry run on a tracker that cannot set a status warns and prints no
// status line, as a real run does.
func TestReportDryRunUnsupportedStatus(t *testing.T) {
	e := newReportEnv(t, tracker.Caps{Comment: true}, statuses3...)
	e.mustRun("task", "move", e.task, "doing")
	var out reportJSON
	if code := e.json(&out, "tracker", "report", e.task, "--dry-run"); code != 0 {
		t.Fatalf("exit %d: %s", code, e.err.String())
	}
	if out.Status != nil || out.Comment == nil || len(out.Warnings) != 1 || len(e.rec.log) != 0 {
		t.Fatalf("%+v calls %v", out, e.rec.log)
	}
}

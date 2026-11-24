package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/tracker"
)

const reportUsage = "usage: rig tracker report <task> [--status] [--comment pr|state|summary] [--include findings|notes]... [--dry-run] [--actor <name>] [--json] [--repo <dir>]"

// reportCommentOut is the comment part of `rig tracker report --json`. Body
// is set only by --dry-run.
type reportCommentOut struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Body string `json:"body,omitempty"`
}

// reportOut is `rig tracker report --json`. With --dry-run, DryRun is set,
// the status has no From and the comment has no id but its Body.
type reportOut struct {
	Task     string              `json:"task"`
	Key      string              `json:"key"`
	Status   *store.ReportStatus `json:"status"`
	Comment  *reportCommentOut   `json:"comment"`
	Skipped  bool                `json:"skipped,omitempty"`
	DryRun   bool                `json:"dryRun,omitempty"`
	Warnings []string            `json:"warnings,omitempty"`
}

// reportHistory is what the latest reports for an issue sent: the last status
// target and the last comment, each from the newest event that carried one.
type reportHistory struct {
	status  *store.ReportStatus
	comment *store.ReportComment
}

// lastReports reads what earlier reports sent to the issue (kind, key). Only
// reports written after the task's latest link event count: a relink starts
// the history fresh. The comment is the latest one of commentKind.
func lastReports(log []store.Event, kind, key, commentKind string) reportHistory {
	var h reportHistory
	for i := len(log) - 1; i >= 0 && (h.status == nil || h.comment == nil); i-- {
		if log[i].Kind == store.KindExternal {
			break
		}
		if log[i].Kind != store.KindTrackerReport || log[i].V != store.Version {
			continue
		}
		var d store.TrackerReportData
		if json.Unmarshal(log[i].Data, &d) != nil || d.Kind != kind || !strings.EqualFold(d.Key, key) {
			continue
		}
		if h.status == nil {
			h.status = d.Status
		}
		if h.comment == nil && d.Comment != nil && d.Comment.Kind == commentKind {
			h.comment = d.Comment
		}
	}
	return h
}

// sameStatus reports whether the requested status name matches an earlier
// report's asked or reached one, ignoring case and surrounding spaces.
func sameStatus(want string, earlier ...string) bool {
	want = strings.TrimSpace(want)
	for _, e := range earlier {
		if e = strings.TrimSpace(e); e != "" && strings.EqualFold(want, e) {
			return true
		}
	}
	return false
}

// digest is the hex SHA-256 of a comment body.
func digest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// reportBody builds the comment a report sends. It is a fixed template, and
// the only task data in it is the short id, the state, the counters, the pull
// request URL and, with include, the open findings and the notes. Counters are
// listed by name; a finding is its short id, severity and summary; a note is
// its kind and text.
func reportBody(kind, short, state, prURL string, counters map[string]int, findings []store.Finding, notes []store.Note, include []string) string {
	var b strings.Builder
	switch kind {
	case "pr":
		fmt.Fprintf(&b, "rig: pull request for task %s: %s", short, prURL)
	case "summary":
		fmt.Fprintf(&b, "rig: task %s is now in state %s.", short, state)
		if len(counters) > 0 {
			var parts []string
			for _, n := range sortedNames(counters) {
				parts = append(parts, fmt.Sprintf("%s=%d", n, counters[n]))
			}
			fmt.Fprintf(&b, "\nCounters: %s", strings.Join(parts, ", "))
		}
		if prURL != "" {
			fmt.Fprintf(&b, "\nPull request: %s", prURL)
		}
	default:
		fmt.Fprintf(&b, "rig: task %s is now in state %s.", short, state)
	}
	if slices.Contains(include, "findings") {
		var open []store.Finding
		for _, f := range findings {
			if f.Status == store.StatusOpen {
				open = append(open, f)
			}
		}
		if len(open) > 0 {
			b.WriteString("\n\nOpen findings:")
			for _, f := range open {
				fmt.Fprintf(&b, "\n- %s %s %s", f.Short, f.Severity, mdLine(f.Summary))
			}
		}
	}
	if slices.Contains(include, "notes") && len(notes) > 0 {
		b.WriteString("\n\nNotes:")
		for i := len(notes) - 1; i >= 0; i-- { // oldest first
			fmt.Fprintf(&b, "\n- %s: %s", notes[i].Kind, mdLine(notes[i].Text))
		}
	}
	return b.String()
}

// trackerReport implements `rig tracker report`. Exit codes: 0 sent, or
// skipped because an earlier report already sent the same; 1 refused (the
// state has no report.status entry, the tracker has no such transition, or
// --comment pr with no pull request), with nothing sent; 2 on any other
// error, including no tracker.
func (a *app) trackerReport(ctx context.Context, args []string) int {
	const cmd = "tracker report"
	args, rep, err := splitRepeated(args, []string{"--include"}, []string{"--comment", "--actor", "--role", "--repo"})
	if err != nil {
		return a.fail(cmd, "%v\n%s", err, reportUsage)
	}
	include := rep["--include"]
	fs := newFlags()
	actorOK := a.actorFlags(fs)
	repo, commentFlag := fs.str("--repo"), fs.str("--comment")
	statusFlag, dry, asJSON := fs.boolean("--status"), fs.boolean("--dry-run"), fs.boolean("--json")
	pos, err := fs.parse(args)
	if err != nil || len(pos) != 1 {
		return a.fail(cmd, "%s", reportUsage)
	}
	if err := actorOK(); err != nil {
		return a.fail(cmd, "%v", err)
	}
	commentGiven := slices.Contains(fs.given, "--comment")
	if commentGiven && !slices.Contains([]string{"pr", "state", "summary"}, *commentFlag) {
		return a.fail(cmd, "--comment wants pr, state or summary, not %q", *commentFlag)
	}
	for _, in := range include {
		if in != "findings" && in != "notes" {
			return a.fail(cmd, "--include wants findings or notes, not %q", in)
		}
	}
	// With neither --status nor --comment, a report is --status --comment state.
	wantStatus, wantComment, kind := *statusFlag, commentGiven, *commentFlag
	if !wantStatus && !wantComment {
		wantStatus, wantComment, kind = true, true, "state"
	}
	if len(include) > 0 && !wantComment {
		return a.fail(cmd, "--include needs a comment: it adds to the comment, which --status alone does not send")
	}

	w, err := a.openWork(ctx, *repo, true)
	if err != nil {
		return a.storeFail(cmd, err)
	}
	tr, spec, err := a.selectTracker(ctx, w.dir)
	if err != nil {
		return a.fail(cmd, "%v", err)
	}
	t, err := w.st.Task(ctx, pos[0])
	if err != nil {
		return a.storeFail(cmd, err)
	}
	ext := t.External
	if ext == nil {
		return a.fail(cmd, "task %s is not linked to an issue", t.Short)
	}
	if msg := linkMismatch(ext, spec); msg != "" {
		return a.fail(cmd, "%s: %s", ext.Key, msg)
	}
	v, err := w.view(ctx, t)
	if err != nil {
		return a.storeFail(cmd, err)
	}
	refuse := func(format string, args ...any) int {
		fmt.Fprintf(a.stderr, "rig %s: %s; nothing sent\n", cmd, fmt.Sprintf(format, args...))
		return 1
	}

	// Everything that can refuse is settled before anything is sent.
	out := reportOut{Task: t.Short, Key: ext.Key, DryRun: *dry}
	var warnings []string
	status := ""
	if wantStatus {
		var ok bool
		if status, ok = w.wf.ReportStatus(v.State); !ok {
			return refuse("task %s is in state %s, which has no tracker.report.status entry", t.Short, v.State)
		}
	}
	body := ""
	if wantComment {
		prURL := ""
		if t.PR > 0 && (kind == "pr" || kind == "summary") {
			u, err := a.pullRequestURL(ctx, w.dir, t.PR)
			switch {
			case err == nil:
				prURL = u
			case kind == "pr":
				return a.fail(cmd, "pull request %d: %v", t.PR, err)
			default:
				warnings = append(warnings, fmt.Sprintf("pull request %d: %v", t.PR, err))
			}
		}
		if kind == "pr" && prURL == "" {
			return refuse("task %s has no pull request", t.Short)
		}
		var findings []store.Finding
		var notes []store.Note
		if slices.Contains(include, "findings") {
			if findings, err = w.st.Findings(ctx, t.ID); err != nil {
				return a.storeFail(cmd, err)
			}
		}
		if slices.Contains(include, "notes") {
			if notes, err = w.st.Notes(ctx, store.NoteFilter{Task: t.ID}); err != nil {
				return a.storeFail(cmd, err)
			}
		}
		body = reportBody(kind, t.Short, v.State, prURL, t.Counters, findings, notes, include)
	}

	log, err := w.st.Events(ctx, t.ID)
	if err != nil {
		return a.storeFail(cmd, err)
	}
	// Capabilities is a read. A status the tracker cannot set is dropped before
	// the skip check, so a repeat whose other part is skipped reports skipped.
	var caps tracker.Caps
	haveCaps := false
	if wantStatus || *dry {
		c, err := tr.Capabilities(ctx)
		switch {
		case err == nil:
			caps, haveCaps = c, true
		case *dry:
			warnings = append(warnings, fmt.Sprintf("could not read the tracker's capabilities: %v", err))
		}
	}
	if wantStatus && haveCaps && !caps.SetStatus {
		warnings = append(warnings, "this tracker cannot set a status")
		wantStatus = false
	}
	last := lastReports(log, ext.Kind, ext.Key, kind)
	if wantStatus && last.status != nil && sameStatus(status, last.status.To, last.status.Asked) {
		wantStatus = false
	}
	if wantComment && last.comment != nil && last.comment.Digest == digest(body) {
		wantComment = false
	}
	if !wantStatus && !wantComment {
		out.Skipped = true
		return a.reportDone(out, warnings, *asJSON, "already reported; nothing sent", 0)
	}

	if *dry {
		if wantComment && haveCaps && !caps.Comment {
			warnings = append(warnings, "this tracker cannot comment")
			wantComment = false
		}
		if wantStatus {
			out.Status = &store.ReportStatus{To: status}
		}
		if wantComment {
			out.Comment = &reportCommentOut{Kind: kind, Body: body}
		}
		return a.reportDone(out, warnings, *asJSON, "", 0)
	}

	// Status goes first: a refusal then means nothing has left the machine.
	var sent store.TrackerReportData
	sent.Kind, sent.Key = ext.Kind, ext.Key
	fail := 0
	if wantStatus {
		res, err := tr.SetStatus(ctx, ext.Key, status)
		switch {
		case errors.Is(err, tracker.ErrNoTransition):
			return refuse("%s: the tracker has no transition to %q", ext.Key, status)
		case errors.Is(err, tracker.ErrUnsupported):
			warnings = append(warnings, "this tracker cannot set a status")
		case err != nil:
			return a.fail(cmd, "%s: set status: %v", ext.Key, err)
		default:
			sent.Status = &store.ReportStatus{From: res.From, To: res.To, Asked: status}
			out.Status = &store.ReportStatus{From: res.From, To: res.To}
		}
	}
	if wantComment {
		res, err := tr.Comment(ctx, ext.Key, tracker.Comment{Body: body})
		switch {
		case errors.Is(err, tracker.ErrUnsupported):
			warnings = append(warnings, "this tracker cannot comment")
		case err != nil:
			fmt.Fprintf(a.stderr, "rig %s: %s: comment: %v\n", cmd, ext.Key, err)
			fail = 2
		default:
			sent.Comment = &store.ReportComment{Kind: kind, ID: res.ID, Digest: digest(body)}
			out.Comment = &reportCommentOut{Kind: kind, ID: res.ID}
		}
	}
	if sent.Status != nil || sent.Comment != nil {
		sent.At = a.now().UTC()
		_, err := w.st.Update(ctx, t.ID, func(cur store.Task, _ []store.Finding) ([]store.Event, error) {
			if cur.External == nil || !strings.EqualFold(cur.External.Key, ext.Key) || cur.External.Kind != ext.Kind {
				return nil, errors.New("the task's link changed during the report")
			}
			ev, err := store.NewEvent(store.KindTrackerReport, sent)
			return []store.Event{ev}, err
		})
		if err != nil {
			fmt.Fprintf(a.stderr, "rig %s: sent, but could not record it: %v\n", cmd, err)
			fail = 2
		}
	}
	if fail != 0 {
		return fail
	}
	return a.reportDone(out, warnings, *asJSON, "", 0)
}

// reportDone prints a report's result and returns code.
func (a *app) reportDone(out reportOut, warnings []string, asJSON bool, msg string, code int) int {
	out.Warnings = warnings
	if asJSON {
		if c := a.printJSON(out); c != 0 {
			return c
		}
		return code
	}
	switch {
	case out.Skipped:
		fmt.Fprintf(a.stdout, "%s  %s  skipped: %s\n", out.Task, out.Key, msg)
	case out.DryRun:
		fmt.Fprintf(a.stdout, "%s  %s  dry run, nothing sent\n", out.Task, out.Key)
		if out.Status != nil {
			fmt.Fprintf(a.stdout, "status: %s\n", out.Status.To)
		}
		if out.Comment != nil {
			fmt.Fprintf(a.stdout, "comment (%s):\n%s\n", out.Comment.Kind, out.Comment.Body)
		}
	default:
		line := fmt.Sprintf("%s  %s", out.Task, out.Key)
		if s := out.Status; s != nil {
			line += fmt.Sprintf("  status %s -> %s", s.From, s.To)
		}
		if c := out.Comment; c != nil {
			line += fmt.Sprintf("  comment %s #%s", c.Kind, c.ID)
		}
		fmt.Fprintln(a.stdout, line)
	}
	for _, w := range out.Warnings {
		fmt.Fprintf(a.stdout, "  warning: %s\n", w)
	}
	return code
}

// pullRequestURL asks the forge for the URL of pull request number n.
func (a *app) pullRequestURL(ctx context.Context, dir string, n int) (string, error) {
	if a.forge == nil {
		return "", errors.New("no forge")
	}
	f, err := a.forge(ctx, dir)
	if err != nil {
		return "", err
	}
	pr, err := f.ViewPR(ctx, strconv.Itoa(n))
	if err != nil {
		return "", err
	}
	if pr.URL == "" {
		return "", errors.New("the forge gave no URL")
	}
	return pr.URL, nil
}

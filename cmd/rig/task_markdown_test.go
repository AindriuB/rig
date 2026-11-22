package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/workflow"
)

func TestRenderTaskMarkdownStableAndOptionalSections(t *testing.T) {
	at := time.Date(2026, 11, 22, 12, 34, 0, 0, time.UTC)
	transition, err := json.Marshal(store.TransitionData{From: "open", To: "done", Sha: "1234567890abcdef", PR: 12, Reason: "verified"})
	if err != nil {
		t.Fatal(err)
	}
	v := workflow.TaskView{
		Title: "A task", Short: "t-abc", Slug: "a-task", Body: "\nDetails.\n", State: "done", RecordedState: "open",
		Branch: "task/a-task", PR: 12, HeadSha: "abcdef1234567890", Reason: "verified",
		Fields: map[string]any{"zeta": "last", "alpha": "first"}, Counters: map[string]int{"rounds": 2},
	}
	got := renderTaskMarkdown(v, workflow.State{Action: "ship", Who: "human"}, taskMarkdownOptions{
		showFindings: true, showNotes: true, showHistory: true,
		findings: []store.Finding{{Short: "f-abc", Severity: "high", Status: "open", Summary: "Review it", Path: "a.go:4"}},
		notes:    []store.Note{{Created: at, Kind: "decision", Text: "Keep it"}},
		events:   []store.Event{{Kind: store.KindTransition, Time: at, Actor: "ann", Data: transition}},
	})
	want := "# A task\n\n" +
		"t-abc (a-task)  state: done  action: ship  who: human\n" +
		"recorded state: open\n" +
		"branch: task/a-task  pr: #12  head: abcdef123456  reason: verified\n" +
		"\n## Body\n\nDetails.\n" +
		"\n## Fields\n\n- alpha: first\n- zeta: last\n" +
		"\n## Counters\n\n- rounds: 2\n" +
		"\n## Findings\n\n- f-abc [high] open: Review it (a.go:4)\n" +
		"\n## Notes\n\n- 2026-11-22 12:34 [decision] Keep it\n" +
		"\n## History\n\n- 2026-11-22 12:34  open -> done  sha 1234567890ab  pr #12  reason: verified  (ann)\n"
	if got != want {
		t.Fatalf("renderTaskMarkdown() mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	sparse := renderTaskMarkdown(workflow.TaskView{Title: "Empty", Short: "t-empty", Slug: "empty", State: "open", RecordedState: "open", Branch: "task/empty"}, workflow.State{}, taskMarkdownOptions{
		showFindings: true, showNotes: true, showHistory: true,
	})
	for _, section := range []string{"## Findings\n\nnone", "## Notes\n\nnone", "## History\n\nnone"} {
		if !strings.Contains(sparse, section) {
			t.Errorf("empty optional section missing %q:\n%s", section, sparse)
		}
	}
}

func TestRenderTaskMarkdownEscapesTerminalControls(t *testing.T) {
	got := renderTaskMarkdown(workflow.TaskView{
		Title: "title\x1b]52;c;clipboard\x07", Short: "t-safe", Slug: "safe", State: "open", RecordedState: "open", Branch: "task/safe",
		Body: "text\x1b[2J",
	}, workflow.State{}, taskMarkdownOptions{})
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Fatalf("Markdown contains terminal control characters: %q", got)
	}
	for _, want := range []string{`title\u001b]52;c;clipboard\u0007`, `text\u001b[2J`} {
		if !strings.Contains(got, want) {
			t.Errorf("escaped Markdown missing %q: %q", want, got)
		}
	}
}

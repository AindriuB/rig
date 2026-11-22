package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/workflow"
)

type taskMarkdownOptions struct {
	showFindings bool
	showNotes    bool
	showHistory  bool
	findings     []store.Finding
	notes        []store.Note
	events       []store.Event
}

func renderTaskMarkdown(v workflow.TaskView, state workflow.State, opts taskMarkdownOptions) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# %s\n\n", v.Title)
	line := fmt.Sprintf("%s (%s)  state: %s", v.Short, v.Slug, v.State)
	if state.Action != "" {
		line += "  action: " + state.Action
	}
	if state.Who != "" {
		line += "  who: " + state.Who
	}
	fmt.Fprintln(&out, line)
	if v.State != v.RecordedState {
		fmt.Fprintf(&out, "recorded state: %s\n", v.RecordedState)
	}
	meta := "branch: " + v.Branch
	if v.PR > 0 {
		meta += "  pr: #" + strconv.Itoa(v.PR)
	}
	if v.HeadSha != "" {
		meta += "  head: " + short(v.HeadSha)
	}
	if v.Reason != "" {
		meta += "  reason: " + v.Reason
	}
	fmt.Fprintln(&out, meta)
	if v.Body != "" {
		fmt.Fprintf(&out, "\n## Body\n\n%s\n", strings.Trim(v.Body, "\n"))
	}
	if len(v.Fields) > 0 {
		fmt.Fprint(&out, "\n## Fields\n\n")
		for _, n := range sortedNames(v.Fields) {
			fmt.Fprintf(&out, "- %s: %s\n", n, renderValue(v.Fields[n]))
		}
	}
	if len(v.Counters) > 0 {
		fmt.Fprint(&out, "\n## Counters\n\n")
		for _, n := range sortedNames(v.Counters) {
			fmt.Fprintf(&out, "- %s: %d\n", n, v.Counters[n])
		}
	}
	if opts.showFindings {
		fmt.Fprint(&out, "\n## Findings\n\n")
		for _, f := range opts.findings {
			loc := ""
			if f.Path != "" {
				loc = " (" + f.Path + ")"
			}
			fmt.Fprintf(&out, "- %s [%s] %s: %s%s\n", f.Short, f.Severity, f.Status, f.Summary, loc)
		}
		if len(opts.findings) == 0 {
			fmt.Fprintln(&out, "none")
		}
	}
	if opts.showNotes {
		fmt.Fprint(&out, "\n## Notes\n\n")
		for _, n := range opts.notes {
			fmt.Fprintf(&out, "- %s [%s] %s\n", n.Created.Format("2006-01-02 15:04"), n.Kind, n.Text)
		}
		if len(opts.notes) == 0 {
			fmt.Fprintln(&out, "none")
		}
	}
	if opts.showHistory {
		fmt.Fprint(&out, "\n## History\n\n")
		n := 0
		for _, e := range opts.events {
			if e.Kind != store.KindTransition {
				continue
			}
			var d store.TransitionData
			if json.Unmarshal(e.Data, &d) != nil {
				continue
			}
			n++
			extra := ""
			if d.Sha != "" {
				extra += "  sha " + short(d.Sha)
			}
			if d.PR > 0 {
				extra += "  pr #" + strconv.Itoa(d.PR)
			}
			if d.Reason != "" {
				extra += "  reason: " + d.Reason
			}
			fmt.Fprintf(&out, "- %s  %s -> %s%s  (%s)\n", e.Time.Format("2006-01-02 15:04"), d.From, d.To, extra, e.Actor)
		}
		if n == 0 {
			fmt.Fprintln(&out, "none")
		}
	}
	return out.String()
}

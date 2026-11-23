package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/workflow"
)

// The Markdown renderings of `rig task show`, `rig task list` and `rig
// context`. They are stable like JSON: sections and fields are only added.
// Every one is UTF-8 with LF line endings, no ANSI codes, no wrapping, times
// in RFC 3339 UTC and one trailing newline.

// mdHistory is one transition in the History section of task show.
type mdHistory struct {
	Time     time.Time
	From, To string
	Sha      string
	PR       int
	Reason   string
	Actor    string
}

// mdShow is what renderShowMD prints. A section flag asks for a section that
// prints `none` when its list is empty.
type mdShow struct {
	View        workflow.TaskView
	ParentShort string // the parent's short id; empty falls back to View.Parent
	ShowFind    bool
	Findings    []store.Finding
	ShowNotes   bool
	Notes       []store.Note
	ShowHistory bool
	History     []mdHistory
}

// mdTime formats t as RFC 3339 UTC.
func mdTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// mdLine collapses every run of line breaks in s to one space, for a value
// that must stay on one line.
func mdLine(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	var b strings.Builder
	brk := false
	for _, r := range s {
		if r == '\r' || r == '\n' {
			if !brk {
				b.WriteByte(' ')
			}
			brk = true
			continue
		}
		brk = false
		b.WriteRune(r)
	}
	return b.String()
}

// externalSummary is a task's tracker link on one line: the kind and key, the
// URL, and the fields the tracker owns.
func externalSummary(e *workflow.ExternalView) string {
	s := e.Kind + " " + e.Key
	if e.URL != "" {
		s += " " + e.URL
	}
	if len(e.Owned) > 0 {
		s += " owned: " + strings.Join(e.Owned, ", ")
	}
	if len(e.Drift) > 0 {
		s += " drift: " + strings.Join(e.Drift, ", ")
	}
	return s
}

// renderShowMD renders `rig task show` as Markdown.
func renderShowMD(s mdShow) string {
	v := s.View
	var b strings.Builder
	b.WriteString("# " + mdLine(v.Title) + "\n\n")
	bullet := func(label, val string) {
		if val != "" {
			b.WriteString("- " + label + ": " + mdLine(val) + "\n")
		}
	}
	bullet("id", v.ID)
	bullet("slug", v.Slug)
	bullet("state", v.State)
	bullet("recorded state", v.RecordedState)
	bullet("branch", v.Branch)
	if v.PR > 0 {
		bullet("pr", "#"+strconv.Itoa(v.PR))
	}
	bullet("head", v.HeadSha)
	bullet("reason", v.Reason)
	if v.Parent != "" {
		p := s.ParentShort
		if p == "" {
			p = v.Parent
		}
		bullet("parent", p)
	}
	if v.External != nil {
		bullet("external", externalSummary(v.External))
	}
	if !v.Created.IsZero() {
		bullet("created", mdTime(v.Created))
	}
	if !v.Updated.IsZero() {
		bullet("updated", mdTime(v.Updated))
	}
	section := func(name string) { b.WriteString("\n## " + name + "\n\n") }
	if body := strings.Trim(strings.ReplaceAll(strings.ReplaceAll(v.Body, "\r\n", "\n"), "\r", "\n"), "\n"); body != "" {
		section("Body")
		b.WriteString(quoteBody(body) + "\n")
	}
	if len(v.Fields) > 0 {
		section("Fields")
		for _, n := range sortedNames(v.Fields) {
			b.WriteString("- " + mdLine(n) + ": " + mdLine(renderValue(v.Fields[n])) + "\n")
		}
	}
	if len(v.Counters) > 0 {
		section("Counters")
		for _, n := range sortedNames(v.Counters) {
			b.WriteString("- " + mdLine(n) + ": " + strconv.Itoa(v.Counters[n]) + "\n")
		}
	}
	if s.ShowFind {
		section("Findings")
		for _, f := range s.Findings {
			loc := ""
			if f.Path != "" {
				loc = " (" + mdLine(f.Path) + ")"
			}
			fmt.Fprintf(&b, "- %s [%s] %s: %s%s\n", f.Short, mdLine(f.Severity), f.Status, mdLine(f.Summary), loc)
		}
		if len(s.Findings) == 0 {
			b.WriteString("none\n")
		}
	}
	if s.ShowNotes {
		section("Notes")
		for _, n := range s.Notes {
			fmt.Fprintf(&b, "- %s [%s] %s\n", mdTime(n.Created), mdLine(n.Kind), mdLine(n.Text))
		}
		if len(s.Notes) == 0 {
			b.WriteString("none\n")
		}
	}
	if s.ShowHistory {
		section("History")
		for _, h := range s.History {
			line := fmt.Sprintf("- %s %s -> %s", mdTime(h.Time), mdLine(h.From), mdLine(h.To))
			if h.Sha != "" {
				line += " sha " + short(h.Sha)
			}
			if h.PR > 0 {
				line += " pr #" + strconv.Itoa(h.PR)
			}
			if h.Reason != "" {
				line += " reason: " + mdLine(h.Reason)
			}
			b.WriteString(line + " (" + mdLine(h.Actor) + ")\n")
		}
		if len(s.History) == 0 {
			b.WriteString("none\n")
		}
	}
	return b.String()
}

// renderListMD renders `rig task list` as Markdown, in the order given. shorts
// maps a task id to its short id, for the parent of a task.
func renderListMD(views []workflow.TaskView, shorts map[string]string) string {
	var b strings.Builder
	b.WriteString("# Tasks\n\n")
	for _, v := range views {
		fmt.Fprintf(&b, "- %s [%s] %s (%s)", v.Short, mdLine(v.State), mdLine(v.Title), mdLine(v.Slug))
		if v.External != nil && v.External.Key != "" {
			b.WriteString(" — " + mdLine(v.External.Key))
		}
		if v.Parent != "" {
			p := shorts[v.Parent]
			if p == "" {
				p = v.Parent
			}
			b.WriteString(" ← " + p)
		}
		b.WriteString("\n")
	}
	if len(views) == 0 {
		b.WriteString("none\n")
	}
	return b.String()
}

// mdLayout is how fitBudget measures what a section prints: the text brief's
// own bytes, or the Markdown's.
type mdLayout struct {
	heading func(name string) string
	more    func(n int) string
	// tail is the bytes printed after each visible section.
	tail int
}

var textLayout = mdLayout{heading: headingLine, more: moreLine}

var markdownLayout = mdLayout{
	heading: func(name string) string { return "## " + name + "\n\n" },
	more:    func(n int) string { return "_… " + strconv.Itoa(n) + " more_\n" },
	tail:    1,
}

// markdownSections turns the gathered sections into Markdown items: Body
// as a blockquote, every other section as `- ` bullets.
func markdownSections(secs []ctxSection) []ctxSection {
	out := make([]ctxSection, len(secs))
	for i, s := range secs {
		items := make([]string, len(s.Items))
		if s.Name == "Body" {
			items = strings.Split(quoteBody(strings.Join(s.Items, "\n")), "\n")
			if len(s.Items) == 0 {
				items = []string{}
			}
		} else {
			for j, it := range s.Items {
				items[j] = "- " + mdLine(it)
			}
		}
		s.Items = items
		out[i] = s
	}
	return out
}

// renderContextMD prints the cut sections as Markdown. A hidden section is
// left out.
func renderContextMD(o ctxOut) string {
	var parts []string
	for _, s := range o.Sections {
		if s.hidden {
			continue
		}
		var b strings.Builder
		b.WriteString(markdownLayout.heading(s.Name))
		for _, it := range s.Items {
			b.WriteString(it + "\n")
		}
		if s.Omitted > 0 {
			b.WriteString(markdownLayout.more(s.Omitted))
		}
		parts = append(parts, b.String())
	}
	return strings.Join(parts, "\n")
}

// quoteBody returns body as a Markdown blockquote: line endings normalised to
// LF, every line prefixed "> " (an empty one ">"). Nothing in it can then
// open a section, a fence or an HTML block outside the quote.
func quoteBody(body string) string {
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		if l == "" {
			lines[i] = ">"
		} else {
			lines[i] = "> " + l
		}
	}
	return strings.Join(lines, "\n")
}

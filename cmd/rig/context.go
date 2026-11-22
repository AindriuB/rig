package main

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AindriuB/rig/internal/git"
	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/workflow"
)

// Budget limits, in tokens (bytes ÷ 4, rounded up).
const (
	defaultBudget = 1500
	minBudget     = 200
	maxBudget     = 20000
)

// Limits on what one section can gather, whatever the budget.
const (
	commitLimit  = 50
	blobChunk    = 50
	noteTextRune = 120
	overlapShown = 3
)

// ctxSection is one section of the brief: its name and its candidate lines,
// before the budget cuts them.
type ctxSection struct {
	Name    string   `json:"name"`
	Items   []string `json:"items"`
	Omitted int      `json:"omitted"`
	// hidden marks a section the text form does not print: one with no
	// candidates, or one whose heading and "… n more" line do not fit.
	hidden bool
}

// ctxOut is the --json shape of `rig context`.
type ctxOut struct {
	Task       string       `json:"task"`
	Budget     int          `json:"budget"`
	Used       int          `json:"used"`
	OverBudget bool         `json:"overBudget"`
	Sections   []ctxSection `json:"sections"`
}

// tokens estimates the tokens in n bytes.
func tokens(n int) int { return (n + 3) / 4 }

// context implements `rig context <task> [--budget n] [--json] [--repo dir]`.
func (a *app) context(ctx context.Context, args []string) int {
	fs := newFlags()
	budgetFlag, repoFlag := fs.str("--budget"), fs.str("--repo")
	asJSON := fs.boolean("--json")
	pos, err := fs.parse(args)
	if err != nil {
		return a.fail("context", "%v", err)
	}
	if len(pos) != 1 {
		return a.fail("context", "usage: rig context <task> [--budget <tokens>] [--json] [--repo <dir>]")
	}
	budget := defaultBudget
	if *budgetFlag != "" {
		n, err := strconv.Atoi(*budgetFlag)
		if err != nil || n < minBudget || n > maxBudget {
			return a.fail("context", "--budget must be a whole number from %d to %d", minBudget, maxBudget)
		}
		budget = n
	}
	w, err := a.openWork(ctx, *repoFlag, true)
	if err != nil {
		return a.storeFail("context", err)
	}
	t, err := w.st.Task(ctx, pos[0])
	if err != nil {
		return a.storeFail("context", err)
	}
	secs, err := w.gather(ctx, t)
	if err != nil {
		return a.storeFail("context", err)
	}
	out := fitBudget(t.Short, budget, secs)
	if *asJSON {
		return a.printJSON(out)
	}
	fmt.Fprint(a.stdout, renderContext(out))
	return 0
}

// gather builds every section's full candidate lines for t. A workflow with
// no owns field gets only the first three sections.
func (w *work) gather(ctx context.Context, t store.Task) ([]ctxSection, error) {
	view, err := w.view(ctx, t)
	if err != nil {
		return nil, err
	}
	findings, err := w.st.Findings(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	secs := []ctxSection{
		{Name: "Header", Items: w.headerLines(view)},
		{Name: "Body", Items: w.bodyLines(t)},
		{Name: "Findings", Items: w.findingLines(findings)},
	}
	ownsName := w.ownsField()
	if ownsName == "" {
		return secs, nil
	}
	files, err := w.ownedFiles(ctx, t, ownsName)
	if err != nil {
		return nil, err
	}
	paths, err := w.pathLines(ctx, files)
	if err != nil {
		return nil, err
	}
	overlap, history, err := w.otherTasks(ctx, t, ownsName, files)
	if err != nil {
		return nil, err
	}
	commits, err := w.commitLines(ctx, files)
	if err != nil {
		return nil, err
	}
	return append(secs,
		ctxSection{Name: "Paths", Items: paths},
		ctxSection{Name: "Overlap", Items: overlap},
		ctxSection{Name: "History", Items: history},
		ctxSection{Name: "Commits", Items: commits},
	), nil
}

// fitBudget cuts the sections to the budget. Sections 1 and 2 print in full;
// each later section adds lines while they fit, keeping room for its
// "… n more" line.
func fitBudget(short string, budget int, secs []ctxSection) ctxOut {
	limit := budget * 4
	out := ctxOut{Task: short, Budget: budget}
	used := 0
	fixed := 0
	for i, s := range secs {
		if i < 2 {
			if s.Items == nil {
				s.Items = []string{}
			}
			if len(s.Items) == 0 {
				s.hidden = true
			} else {
				used += sectionSize(s.Name, s.Items, 0)
			}
			s.Omitted = 0
			if i == 1 {
				fixed = used
			}
			out.Sections = append(out.Sections, s)
			continue
		}
		out.Sections = append(out.Sections, cutSection(s, limit-used, &used))
	}
	out.Used = tokens(used)
	out.OverBudget = fixed > limit
	return out
}

// cutSection returns s cut to rem bytes, adding what it prints to *used.
func cutSection(s ctxSection, rem int, used *int) ctxSection {
	n := len(s.Items)
	cut := ctxSection{Name: s.Name, Items: []string{}}
	if n == 0 {
		cut.hidden = true
		return cut
	}
	reserve := len(moreLine(n))
	size := len(headingLine(s.Name))
	if size+reserve > rem {
		cut.Omitted = n
		cut.hidden = true
		return cut
	}
	for i, it := range s.Items {
		add := len(it) + 1
		after := size + add
		if i < n-1 {
			after += reserve
		}
		if after > rem {
			break
		}
		size += add
		cut.Items = append(cut.Items, it)
	}
	cut.Omitted = n - len(cut.Items)
	if cut.Omitted > 0 {
		size += len(moreLine(cut.Omitted))
	}
	*used += size
	return cut
}

func headingLine(name string) string { return "## " + name + "\n" }
func moreLine(n int) string          { return "… " + strconv.Itoa(n) + " more\n" }

// sectionSize is the bytes a section prints.
func sectionSize(name string, items []string, omitted int) int {
	n := len(headingLine(name))
	for _, it := range items {
		n += len(it) + 1
	}
	if omitted > 0 {
		n += len(moreLine(omitted))
	}
	return n
}

// renderContext prints the cut sections. A hidden section prints nothing.
func renderContext(o ctxOut) string {
	var b strings.Builder
	for _, s := range o.Sections {
		if s.hidden {
			continue
		}
		b.WriteString(headingLine(s.Name))
		for _, it := range s.Items {
			b.WriteString(it + "\n")
		}
		if s.Omitted > 0 {
			b.WriteString(moreLine(s.Omitted))
		}
	}
	return b.String()
}

// headerLines is section 1.
func (w *work) headerLines(v workflow.TaskView) []string {
	st := w.wf.States[v.State]
	action, who := st.Action, st.Who
	if _, ok := w.wf.States[v.State]; !ok {
		action = "unknown-state"
	}
	if who == "" {
		who = workflow.WhoAgent
	}
	lines := []string{
		fmt.Sprintf("%s %s: %s", v.Short, v.Slug, v.Title),
		fmt.Sprintf("state: %s  action: %s  who: %s", v.State, action, who),
	}
	if v.Branch != "" {
		line := "branch: " + v.Branch
		if v.PR > 0 {
			line += "  pr: #" + strconv.Itoa(v.PR)
		}
		lines = append(lines, line)
	} else if v.PR > 0 {
		lines = append(lines, "pr: #"+strconv.Itoa(v.PR))
	}
	var counters []string
	for _, k := range sortedKeys(v.Counters) {
		counters = append(counters, fmt.Sprintf("%s=%d", k, v.Counters[k]))
	}
	counters = append(counters, fmt.Sprintf("openFindings=%d", v.OpenFindings))
	return append(lines, "counters: "+strings.Join(counters, " "))
}

// bodyLines is section 2: the body, then each context field that is set.
func (w *work) bodyLines(t store.Task) []string {
	var lines []string
	if body := strings.TrimRight(t.Body, " \t\r\n"); body != "" {
		lines = append(lines, strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")...)
	}
	for _, name := range sortedKeys(w.wf.Fields) {
		if !w.wf.Fields[name].Context {
			continue
		}
		v, ok := t.Fields[name]
		if !ok {
			continue
		}
		if list := stringList(v); list != nil {
			lines = append(lines, name+": "+strings.Join(list, ", "))
		} else if s := fmt.Sprint(v); s != "" && s != "[]" {
			lines = append(lines, name+": "+s)
		}
	}
	return lines
}

// findingLines is section 3: open findings, blocking severities first, then
// in the order they were added.
func (w *work) findingLines(findings []store.Finding) []string {
	var open []store.Finding
	for _, f := range findings {
		if f.Status == store.StatusOpen {
			open = append(open, f)
		}
	}
	sort.SliceStable(open, func(i, j int) bool {
		return w.wf.Blocking(open[i].Severity) && !w.wf.Blocking(open[j].Severity)
	})
	lines := make([]string, 0, len(open))
	for _, f := range open {
		line := fmt.Sprintf("%s %s %s", f.Short, f.Severity, oneLine(f.Summary))
		if f.Path != "" {
			line += " (" + f.Path + ")"
		}
		lines = append(lines, line)
	}
	return lines
}

// ownsField is the name of the workflow's owns:true paths field, or "".
func (w *work) ownsField() string {
	for _, name := range sortedKeys(w.wf.Fields) {
		if f := w.wf.Fields[name]; f.Owns && f.Type == workflow.TypePaths {
			return name
		}
	}
	return ""
}

// ownedFiles lists the tracked files t's owns field matches.
func (w *work) ownedFiles(ctx context.Context, t store.Task, ownsName string) ([]string, error) {
	return w.repo.LsFiles(ctx, globSpecs(stringList(t.Fields[ownsName])))
}

// globSpecs turns paths into ":(glob)" pathspecs.
func globSpecs(paths []string) []string {
	var specs []string
	for _, p := range paths {
		if p = strings.TrimSpace(p); p != "" {
			specs = append(specs, ":(glob)"+p)
		}
	}
	return specs
}

// pathLines is section 4: each file with its line count.
func (w *work) pathLines(ctx context.Context, files []string) ([]string, error) {
	var lines []string
	for start := 0; start < len(files); start += blobChunk {
		end := min(start+blobChunk, len(files))
		specs := make([]string, 0, end-start)
		for _, f := range files[start:end] {
			specs = append(specs, ":"+f)
		}
		blobs, err := w.repo.ReadBlobs(ctx, specs)
		if err != nil {
			return nil, err
		}
		for i, b := range blobs {
			lines = append(lines, fmt.Sprintf("%s (%d lines)", files[start+i], countLines(b.Data)))
		}
	}
	return lines, nil
}

// countLines counts the lines in data: newline-terminated lines plus a final
// unterminated one. Data with a NUL byte is binary and counts as 0.
func countLines(data []byte) int {
	if bytes.IndexByte(data, 0) >= 0 {
		return 0
	}
	n := bytes.Count(data, []byte{'\n'})
	if len(data) > 0 && data[len(data)-1] != '\n' {
		n++
	}
	return n
}

// otherTasks builds sections 5 and 6 from the other tasks whose owned files
// meet files.
func (w *work) otherTasks(ctx context.Context, t store.Task, ownsName string, files []string) (overlap, history []string, err error) {
	if len(files) == 0 {
		return nil, nil, nil
	}
	mine := map[string]bool{}
	for _, f := range files {
		mine[f] = true
	}
	all, err := w.st.Tasks(ctx, store.TaskFilter{})
	if err != nil {
		return nil, nil, err
	}
	type entry struct {
		when time.Time
		id   string
		line string
	}
	var hist []entry
	for _, o := range all {
		if o.ID == t.ID {
			continue
		}
		theirs, err := w.repo.LsFiles(ctx, globSpecs(stringList(o.Fields[ownsName])))
		if err != nil {
			return nil, nil, err
		}
		var shared []string
		for _, f := range theirs {
			if mine[f] {
				shared = append(shared, f)
			}
		}
		if len(shared) == 0 {
			continue
		}
		if st, ok := w.wf.States[o.State]; !ok || !st.Final {
			shown := shared
			if len(shown) > overlapShown {
				shown = append(append([]string{}, shown[:overlapShown]...), fmt.Sprintf("+%d more", len(shared)-overlapShown))
			}
			overlap = append(overlap, fmt.Sprintf("%s %s [%s] %s", o.Short, o.Slug, o.State, strings.Join(shown, ", ")))
		}
		fs, err := w.st.Findings(ctx, o.ID)
		if err != nil {
			return nil, nil, err
		}
		for _, f := range fs {
			if f.Status == store.StatusOpen || (f.Path != "" && !mine[f.Path]) {
				continue
			}
			hist = append(hist, entry{f.Updated, f.ID, fmt.Sprintf("%s finding %s %s %s: %s", o.Short, f.Short, f.Severity, f.Status, oneLine(f.Summary))})
		}
		ns, err := w.st.Notes(ctx, store.NoteFilter{Task: o.ID})
		if err != nil {
			return nil, nil, err
		}
		for _, n := range ns {
			hist = append(hist, entry{n.Created, n.ID, fmt.Sprintf("%s note %s %s: %s", o.Short, n.Short, n.Kind, oneLine(n.Text))})
		}
	}
	sort.Strings(overlap)
	sort.SliceStable(hist, func(i, j int) bool {
		if !hist[i].when.Equal(hist[j].when) {
			return hist[i].when.After(hist[j].when)
		}
		return hist[i].id > hist[j].id
	})
	for _, h := range hist {
		history = append(history, h.line)
	}
	return overlap, history, nil
}

// commitLines is section 7: the last commits on the default branch touching
// files.
func (w *work) commitLines(ctx context.Context, files []string) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	branch, err := w.repo.DefaultBranch(ctx)
	if err != nil || branch == "" {
		return nil, err
	}
	specs := make([]string, len(files))
	for i, f := range files {
		specs[i] = ":(literal)" + f
	}
	cs, err := w.repo.LogPaths(ctx, branch, specs, commitLimit)
	if err != nil {
		return nil, err
	}
	return commitStrings(cs), nil
}

func commitStrings(cs []git.CommitInfo) []string {
	lines := make([]string, 0, len(cs))
	for _, c := range cs {
		lines = append(lines, c.SHA[:min(7, len(c.SHA))]+" "+c.Subject)
	}
	return lines
}

// stringList reads a field value that is a list of strings; nil otherwise.
func stringList(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			} else {
				out = append(out, fmt.Sprint(e))
			}
		}
		return out
	}
	return nil
}

// oneLine is s on one line, cut to noteTextRune runes.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > noteTextRune {
		r := []rune(s)
		s = string(r[:noteTextRune]) + "…"
	}
	return s
}

// sortedKeys returns m's keys in order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

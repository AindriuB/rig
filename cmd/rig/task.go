package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/AindriuB/rig/internal/proc"
	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/workflow"
)

const (
	taskUsage = "usage: rig task new|show|list|move|set|count|is|log|edit ..."

	// maxBodyFile is the most `rig task new --body-file` reads.
	maxBodyFile = 64 << 10
	// maxSlug is the longest task slug.
	maxSlug = 40

	// envEditorNoTTY, set to 1 in the app's environment, lets `rig task edit`
	// run without a terminal. It is for tests only.
	envEditorNoTTY = "RIG_EDITOR_NO_TTY"
)

var slugRE = regexp.MustCompile(`^[a-z0-9-]+$`)

// task implements `rig task`: new, show, list, move, set, count, is, log and
// edit. Exit codes: 0 done; 1 refused by a gate or the workflow (and, for
// `is`, "not in that state"); 2 usage, a missing task or any other error.
func (a *app) task(ctx context.Context, args []string) int {
	if len(args) == 0 {
		return a.fail("task", "%s", taskUsage)
	}
	rest := args[1:]
	switch args[0] {
	case "new":
		return a.taskNew(ctx, rest)
	case "show":
		return a.taskShow(ctx, rest)
	case "list":
		return a.taskList(ctx, rest)
	case "move":
		return a.taskMove(ctx, rest)
	case "set":
		return a.taskSet(ctx, rest)
	case "count":
		return a.taskCount(ctx, rest)
	case "is":
		return a.taskIs(ctx, rest)
	case "log":
		return a.taskLog(ctx, rest)
	case "edit":
		return a.taskEdit(ctx, rest)
	}
	return a.fail("task", "unknown subcommand %q\n%s", args[0], taskUsage)
}

// splitRepeated takes the repeatable string flags in repeated out of args
// (`--name value` and `--name=value`) and returns what is left, with their
// values in order. The flags in others take a value too: it is skipped, so a
// value that looks like a repeated flag is not mistaken for one.
func splitRepeated(args, repeated, others []string) (rest []string, vals map[string][]string, err error) {
	vals = map[string][]string{}
	for i := 0; i < len(args); i++ {
		name, val, hasVal := strings.Cut(args[i], "=")
		switch {
		case slices.Contains(repeated, name):
			if !hasVal {
				if i+1 >= len(args) {
					return nil, nil, fmt.Errorf("%s needs a value", name)
				}
				i++
				val = args[i]
			}
			vals[name] = append(vals[name], val)
		case slices.Contains(others, name):
			rest = append(rest, args[i])
			if !hasVal && i+1 < len(args) {
				i++
				rest = append(rest, args[i])
			}
		default:
			rest = append(rest, args[i])
		}
	}
	return rest, vals, nil
}

// openTask opens the store and workflow and resolves ref to a task.
func (a *app) openTask(ctx context.Context, repoFlag, ref string) (*work, store.Task, error) {
	w, err := a.openWork(ctx, repoFlag, true)
	if err != nil {
		return nil, store.Task{}, err
	}
	t, err := w.st.Task(ctx, ref)
	if err != nil {
		return nil, store.Task{}, err
	}
	return w, t, nil
}

// fieldType returns the type of the task field name, which may also be
// "title" or "body" when meta is set.
func fieldType(wf *workflow.Workflow, name string, meta bool) (workflow.Field, bool) {
	if meta && (name == "title" || name == "body") {
		return workflow.Field{Type: workflow.TypeText}, true
	}
	f, ok := wf.Fields[name]
	return f, ok
}

// parseFieldArgs reads `name=value` arguments against the workflow's fields.
// A list field may repeat, its items joining; any other field may not. With
// meta, "title" and "body" are fields too.
func parseFieldArgs(wf *workflow.Workflow, raw []string, meta bool) (map[string]any, error) {
	out := map[string]any{}
	for _, kv := range raw {
		name, val, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("--field %q: want name=value", kv)
		}
		f, ok := fieldType(wf, name, meta)
		if !ok {
			return nil, fmt.Errorf("unknown field %q", name)
		}
		v, err := workflow.ParseFieldValue(f, val)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", name, err)
		}
		if prev, dup := out[name]; dup {
			p, pok := prev.([]string)
			n, nok := v.([]string)
			if !pok || !nok {
				return nil, fmt.Errorf("field %s given more than once", name)
			}
			v = append(p, n...)
		}
		out[name] = v
	}
	if t, ok := out["title"].(string); ok && strings.TrimSpace(t) == "" {
		return nil, errors.New("title must not be empty")
	}
	return out, nil
}

func (a *app) taskNew(ctx context.Context, args []string) int {
	args, rep, err := splitRepeated(args, []string{"--field"}, []string{"--slug", "--title", "--body", "--body-file", "--repo"})
	if err != nil {
		return a.fail("task new", "%v", err)
	}
	fs := newFlags()
	slug, title, body, bodyFile, repo := fs.str("--slug"), fs.str("--title"), fs.str("--body"), fs.str("--body-file"), fs.str("--repo")
	asJSON := fs.boolean("--json")
	if pos, err := fs.parse(args); err != nil || len(pos) > 0 {
		return a.fail("task new", "usage: rig task new --slug <s> --title <text> [--body <text> | --body-file <path>] [--field <name>=<value>]... [--json] [--repo <dir>]")
	}
	if len(*slug) > maxSlug || !slugRE.MatchString(*slug) {
		return a.fail("task new", "--slug must be 1 to %d characters of a-z, 0-9 and -", maxSlug)
	}
	if strings.TrimSpace(*title) == "" {
		return a.fail("task new", "--title is required")
	}
	if *body != "" && *bodyFile != "" {
		return a.fail("task new", "--body and --body-file are exclusive")
	}
	text := *body
	if *bodyFile != "" {
		p := *bodyFile
		if !filepath.IsAbs(p) {
			p = filepath.Join(a.cwd, p)
		}
		f, err := os.Open(p)
		if err != nil {
			return a.fail("task new", "%v", err)
		}
		b, err := io.ReadAll(io.LimitReader(f, maxBodyFile+1))
		f.Close()
		if err != nil {
			return a.fail("task new", "read %s: %v", p, err)
		}
		if len(b) > maxBodyFile {
			return a.fail("task new", "--body-file is larger than %d KiB", maxBodyFile>>10)
		}
		text = string(b)
	}
	w, err := a.openWork(ctx, *repo, true)
	if err != nil {
		return a.storeFail("task new", err)
	}
	fields, err := parseFieldArgs(w.wf, rep["--field"], false)
	if err != nil {
		return a.fail("task new", "%v", err)
	}
	// The branch is not set here: it comes from the workflow's template and
	// the short id, which the store assigns, so it is derived when read.
	t, err := w.st.CreateTask(ctx, store.NewTask{
		Slug: *slug, Title: *title, Body: text, Fields: fields,
		Workflow: store.WorkflowRef{Name: w.wf.Name, Version: w.wf.Version},
		Initial:  w.wf.Initial,
	})
	if err != nil {
		return a.storeFail("task new", err)
	}
	return a.printTask(ctx, "task new", w, t, *asJSON)
}

// printTask prints a task as JSON, or as a one-line summary.
func (a *app) printTask(ctx context.Context, cmd string, w *work, t store.Task, asJSON bool) int {
	v, err := w.view(ctx, t)
	if err != nil {
		return a.storeFail(cmd, err)
	}
	if asJSON {
		return a.printJSON(v)
	}
	fmt.Fprintf(a.stdout, "%s  %s  %s\n", v.Short, v.State, v.Title)
	return 0
}

func (a *app) taskShow(ctx context.Context, args []string) int {
	fs := newFlags()
	repo := fs.str("--repo")
	asJSON, findings, notes, history := fs.boolean("--json"), fs.boolean("--findings"), fs.boolean("--notes"), fs.boolean("--history")
	pos, err := fs.parse(args)
	if err != nil || len(pos) != 1 {
		return a.fail("task show", "usage: rig task show <task> [--findings] [--notes] [--history] [--json] [--repo <dir>]")
	}
	w, t, err := a.openTask(ctx, *repo, pos[0])
	if err != nil {
		return a.storeFail("task show", err)
	}
	v, err := w.view(ctx, t)
	if err != nil {
		return a.storeFail("task show", err)
	}
	if *asJSON {
		return a.printJSON(v)
	}
	st := w.wf.States[v.State]
	opts := taskMarkdownOptions{showFindings: *findings, showNotes: *notes, showHistory: *history}
	if *findings {
		opts.findings, err = w.st.Findings(ctx, t.ID)
		if err != nil {
			return a.storeFail("task show", err)
		}
	}
	if *notes {
		opts.notes, err = w.st.Notes(ctx, store.NoteFilter{Task: t.ID})
		if err != nil {
			return a.storeFail("task show", err)
		}
	}
	if *history {
		opts.events, err = w.st.Events(ctx, t.ID)
		if err != nil {
			return a.storeFail("task show", err)
		}
	}
	fmt.Fprint(a.stdout, renderTaskMarkdown(v, st, opts))
	return 0
}

// sortedNames returns the keys of m in order.
func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (a *app) taskList(ctx context.Context, args []string) int {
	args, rep, err := splitRepeated(args, []string{"--state"}, []string{"--repo"})
	if err != nil {
		return a.fail("task list", "%v", err)
	}
	fs := newFlags()
	repo := fs.str("--repo")
	asJSON, all := fs.boolean("--json"), fs.boolean("--all")
	if pos, err := fs.parse(args); err != nil || len(pos) > 0 {
		return a.fail("task list", "usage: rig task list [--state <s>]... [--all] [--json] [--repo <dir>]")
	}
	w, err := a.openWork(ctx, *repo, true)
	if err != nil {
		return a.storeFail("task list", err)
	}
	tasks, err := w.st.Tasks(ctx, store.TaskFilter{})
	if err != nil {
		return a.storeFail("task list", err)
	}
	states := rep["--state"]
	views := []workflow.TaskView{}
	for _, t := range tasks {
		v, err := w.view(ctx, t)
		if err != nil {
			return a.storeFail("task list", err)
		}
		if len(states) > 0 && !slices.Contains(states, v.State) {
			continue
		}
		if len(states) == 0 && !*all && w.wf.States[v.State].Final {
			continue
		}
		views = append(views, v)
	}
	if *asJSON {
		return a.printJSON(views)
	}
	for _, v := range views {
		fmt.Fprintf(a.stdout, "%s  %-10s  %s  %s\n", v.Short, v.State, v.Slug, v.Title)
	}
	return 0
}

// moveOut is `rig task move --json`: the task and the hook failures.
type moveOut struct {
	workflow.TaskView
	HookErrors []string `json:"hookErrors"`
}

func (a *app) taskMove(ctx context.Context, args []string) int {
	fs := newFlags()
	repo, sha, pr, reason := fs.str("--repo"), fs.str("--sha"), fs.str("--pr"), fs.str("--reason")
	asJSON := fs.boolean("--json")
	pos, err := fs.parse(args)
	if err != nil || len(pos) != 2 {
		return a.fail("task move", "usage: rig task move <task> <state> [--sha <sha>] [--pr <n>] [--reason <text>] [--json] [--repo <dir>]")
	}
	var wargs workflow.Args
	wargs.Sha, wargs.Reason = *sha, *reason
	if *pr != "" {
		n, err := strconv.Atoi(*pr)
		if err != nil || n <= 0 {
			return a.fail("task move", "--pr must be a positive number, got %q", *pr)
		}
		wargs.PR = n
	}
	w, err := a.openWork(ctx, *repo, true)
	if err != nil {
		return a.storeFail("task move", err)
	}
	res, err := w.move(ctx, pos[0], pos[1], wargs)
	if err != nil {
		return a.storeFail("task move", err)
	}
	hookErrs := []string{}
	for _, he := range res.hookErrors {
		hookErrs = append(hookErrs, he.Error())
		fmt.Fprintf(a.stderr, "rig task move: hook: %v\n", he)
	}
	if *asJSON {
		return a.printJSON(moveOut{res.view, hookErrs})
	}
	fmt.Fprintf(a.stdout, "%s  %s\n", res.view.Short, res.view.State)
	return 0
}

// fieldEvents returns one field event per name in changes, in name order.
func fieldEvents(changes map[string]any) ([]store.Event, error) {
	var evs []store.Event
	for _, n := range sortedNames(changes) {
		ev, err := store.NewEvent(store.KindField, store.FieldData{Name: n, Value: changes[n]})
		if err != nil {
			return nil, err
		}
		evs = append(evs, ev)
	}
	return evs, nil
}

// changedFields returns the entries of vals that differ from t's own value.
func changedFields(t store.Task, vals map[string]any) map[string]any {
	changed := map[string]any{}
	for n, v := range vals {
		if !sameValue(currentValue(t, n), v) {
			changed[n] = v
		}
	}
	return changed
}

// currentValue is the task's value for a field name, or nil when unset.
func currentValue(t store.Task, name string) any {
	switch name {
	case "title":
		return t.Title
	case "body":
		return t.Body
	}
	return t.Fields[name]
}

// sameValue compares two field values by their JSON form, after the types
// the command parses and the types a store reads back are made alike.
func sameValue(a, b any) bool {
	return canonical(a) == canonical(b)
}

func canonical(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case []any:
		s := make([]string, len(x))
		for i, e := range x {
			s[i], _ = e.(string)
		}
		v = s
	case float64:
		if x == float64(int(x)) {
			v = int(x)
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// updateFields writes one field event per changed value and returns the task
// after. With nothing changed it writes nothing.
func (w *work) updateFields(ctx context.Context, t store.Task, changed map[string]any) (store.Task, error) {
	if len(changed) == 0 {
		return t, nil
	}
	evs, err := fieldEvents(changed)
	if err != nil {
		return store.Task{}, err
	}
	return w.st.Update(ctx, t.ID, func(cur store.Task, _ []store.Finding) ([]store.Event, error) {
		var moved []string
		for _, n := range sortedNames(changed) {
			if !sameValue(currentValue(cur, n), currentValue(t, n)) {
				moved = append(moved, n)
			}
		}
		if len(moved) > 0 {
			return nil, &refusal{reason: "changed since it was read: " + strings.Join(moved, ", ")}
		}
		return evs, nil
	})
}

func (a *app) taskSet(ctx context.Context, args []string) int {
	args, rep, err := splitRepeated(args, []string{"--field"}, []string{"--repo"})
	if err != nil {
		return a.fail("task set", "%v", err)
	}
	fs := newFlags()
	repo := fs.str("--repo")
	asJSON := fs.boolean("--json")
	pos, err := fs.parse(args)
	if err != nil || len(pos) != 1 || len(rep["--field"]) == 0 {
		return a.fail("task set", "usage: rig task set <task> --field <name>=<value>... [--json] [--repo <dir>]")
	}
	w, t, err := a.openTask(ctx, *repo, pos[0])
	if err != nil {
		return a.storeFail("task set", err)
	}
	vals, err := parseFieldArgs(w.wf, rep["--field"], true)
	if err != nil {
		return a.fail("task set", "%v", err)
	}
	after, err := w.updateFields(ctx, t, changedFields(t, vals))
	if err != nil {
		return a.storeFail("task set", err)
	}
	return a.printTask(ctx, "task set", w, after, *asJSON)
}

func (a *app) taskCount(ctx context.Context, args []string) int {
	fs := newFlags()
	repo := fs.str("--repo")
	asJSON := fs.boolean("--json")
	pos, err := fs.parse(args)
	if err != nil || len(pos) != 2 {
		return a.fail("task count", "usage: rig task count <task> <counter> [--json] [--repo <dir>]")
	}
	w, t, err := a.openTask(ctx, *repo, pos[0])
	if err != nil {
		return a.storeFail("task count", err)
	}
	counter := pos[1]
	if !slices.Contains(w.wf.Counters, counter) {
		return a.fail("task count", "%q is not a counter of workflow %s", counter, w.wf.Name)
	}
	ev, err := store.NewEvent(store.KindCount, store.CountData{Counter: counter, By: 1})
	if err != nil {
		return a.storeFail("task count", err)
	}
	after, err := w.st.Update(ctx, t.ID, func(store.Task, []store.Finding) ([]store.Event, error) {
		return []store.Event{ev}, nil
	})
	if err != nil {
		return a.storeFail("task count", err)
	}
	value := after.Counters[counter]
	if *asJSON {
		return a.printJSON(struct {
			Task    string `json:"task"`
			Counter string `json:"counter"`
			Value   int    `json:"value"`
		}{after.Short, counter, value})
	}
	fmt.Fprintf(a.stdout, "%s  %s  %d\n", after.Short, counter, value)
	return 0
}

func (a *app) taskIs(ctx context.Context, args []string) int {
	fs := newFlags()
	repo := fs.str("--repo")
	pos, err := fs.parse(args)
	if err != nil || len(pos) != 2 {
		return a.fail("task is", "usage: rig task is <task> <state> [--repo <dir>]")
	}
	w, t, err := a.openTask(ctx, *repo, pos[0])
	if err != nil {
		return a.storeFail("task is", err)
	}
	v, err := w.view(ctx, t)
	if err != nil {
		return a.storeFail("task is", err)
	}
	if v.State != pos[1] {
		return 1
	}
	return 0
}

func (a *app) taskLog(ctx context.Context, args []string) int {
	fs := newFlags()
	repo := fs.str("--repo")
	asJSON := fs.boolean("--json")
	pos, err := fs.parse(args)
	if err != nil || len(pos) != 1 {
		return a.fail("task log", "usage: rig task log <task> [--json] [--repo <dir>]")
	}
	w, t, err := a.openTask(ctx, *repo, pos[0])
	if err != nil {
		return a.storeFail("task log", err)
	}
	evs, err := w.st.Events(ctx, t.ID)
	if err != nil {
		return a.storeFail("task log", err)
	}
	if evs == nil {
		evs = []store.Event{}
	}
	if *asJSON {
		return a.printJSON(evs)
	}
	for _, e := range evs {
		fmt.Fprintf(a.stdout, "%d  %s  %s  %s  %s\n", e.Seq, e.Time.Format("2006-01-02T15:04:05Z07:00"), e.Actor, e.Kind, e.Data)
	}
	return 0
}

// renderValue is a field value as the edit file and the show page write it:
// a list as comma-separated items, or as JSON when an item would not survive
// that.
func renderValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []any:
		items := make([]string, len(x))
		for i, e := range x {
			items[i] = fmt.Sprint(e)
		}
		return renderList(items)
	case []string:
		return renderList(x)
	case float64:
		if x == float64(int(x)) {
			return strconv.Itoa(int(x))
		}
	}
	return fmt.Sprint(v)
}

func renderList(items []string) string {
	for _, s := range items {
		if strings.Contains(s, ",") || strings.HasPrefix(strings.TrimSpace(s), "[") || s != strings.TrimSpace(s) {
			b, _ := json.Marshal(items)
			return string(b)
		}
	}
	return strings.Join(items, ", ")
}

// escapeLines writes a value into the edit file: a line that starts with "#"
// or "\" gets one extra leading "\", so no value line can read as a heading.
// unescapeLines is its inverse: a line starting with "\" loses one.
func escapeLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "#") || strings.HasPrefix(l, `\`) {
			lines[i] = `\` + l
		}
	}
	return strings.Join(lines, "\n")
}

func unescapeLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimPrefix(l, `\`)
	}
	return strings.Join(lines, "\n")
}

// editTitle is the title as the edit file's first line holds it: one line.
func editTitle(t store.Task) string {
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(t.Title)
}

// editSection is the text of one "## <name>" section as editFile writes it.
func editSection(t store.Task, name string) string {
	v := t.Body
	if name != "body" {
		v = renderValue(t.Fields[name])
	}
	return escapeLines(strings.Trim(v, "\n"))
}

// editFile renders the task as the Markdown the editor gets: the title, then
// a "## body" section and one "## <field>" section per workflow field. Value
// lines are escaped (see escapeLines), so only a heading rig wrote can split
// the file.
func editFile(wf *workflow.Workflow, t store.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", editTitle(t))
	for _, n := range append([]string{"body"}, sortedNames(wf.Fields)...) {
		fmt.Fprintf(&b, "\n## %s\n\n%s\n", n, editSection(t, n))
	}
	return b.String()
}

// parseEdit reads an edit file back into the values that changed. A value
// changed when its section's text differs from the text editFile wrote for
// it, not when it differs from the stored value, so a value that does not
// survive the round trip (trailing spaces, a newline in a title, CRLF) is left
// alone unless the user touched it. A section that is missing leaves its field
// alone.
func parseEdit(wf *workflow.Workflow, t store.Task, text string) (map[string]any, error) {
	known := map[string]bool{"body": true}
	for n := range wf.Fields {
		known[n] = true
	}
	// An editor that saves CRLF changed no value: compare in LF, as editFile
	// writes it, and keep the stored value's own CRLFs out of the comparison.
	text = strings.ReplaceAll(text, "\r\n", "\n")
	title, sawTitle := "", false
	sections := map[string][]string{}
	cur := ""
	for _, l := range strings.Split(text, "\n") {
		if name, ok := strings.CutPrefix(l, "## "); ok && known[strings.TrimSpace(name)] {
			cur = strings.TrimSpace(name)
			if _, dup := sections[cur]; dup {
				return nil, fmt.Errorf("section %q appears twice", cur)
			}
			sections[cur] = []string{}
			continue
		}
		if cur == "" {
			if h, ok := strings.CutPrefix(l, "# "); ok && !sawTitle {
				title, sawTitle = strings.TrimRight(h, "\r"), true
			} else if strings.TrimSpace(l) != "" && !sawTitle {
				return nil, errors.New("the first line must be \"# <title>\"")
			}
			continue
		}
		sections[cur] = append(sections[cur], l)
	}
	if !sawTitle || strings.TrimSpace(title) == "" {
		return nil, errors.New("the title line \"# <title>\" is missing or empty")
	}
	changed := map[string]any{}
	if title != editTitle(t) {
		changed["title"] = strings.TrimSpace(title)
	}
	for n, ls := range sections {
		raw := strings.Trim(strings.Join(ls, "\n"), "\n")
		// The section as editFile wrote it (with its line end), in LF.
		if raw == strings.Trim(strings.ReplaceAll(editSection(t, n)+"\n", "\r\n", "\n"), "\n") {
			continue
		}
		raw = unescapeLines(raw)
		if n == "body" {
			changed[n] = raw
			continue
		}
		f := wf.Fields[n]
		if f.Type != workflow.TypeText {
			raw = strings.TrimSpace(raw)
		}
		v, err := workflow.ParseFieldValue(f, raw)
		if err != nil {
			return nil, fmt.Errorf("section %q: %w", n, err)
		}
		if !sameValue(currentValue(t, n), v) {
			changed[n] = v
		}
	}
	return changed, nil
}

// hasTerminal reports whether rig is attached to a terminal on both ends.
func (a *app) hasTerminal() bool {
	if a.getenv(envEditorNoTTY) == "1" {
		return true
	}
	out, ok := a.stdout.(*os.File)
	if !ok {
		return false
	}
	char := func(f *os.File) bool {
		fi, err := f.Stat()
		return err == nil && fi.Mode()&os.ModeCharDevice != 0
	}
	return char(os.Stdin) && char(out)
}

// editorArgv is $VISUAL, else $EDITOR, split on spaces: no shell is run.
func (a *app) editorArgv() []string {
	if v := strings.Fields(a.getenv("VISUAL")); len(v) > 0 {
		return v
	}
	return strings.Fields(a.getenv("EDITOR"))
}

func (a *app) taskEdit(ctx context.Context, args []string) int {
	fs := newFlags()
	repo := fs.str("--repo")
	asJSON := fs.boolean("--json")
	pos, err := fs.parse(args)
	if err != nil || len(pos) != 1 {
		return a.fail("task edit", "usage: rig task edit <task> [--json] [--repo <dir>]")
	}
	editor := a.editorArgv()
	if len(editor) == 0 {
		return a.fail("task edit", "set $VISUAL or $EDITOR")
	}
	if !a.hasTerminal() {
		return a.fail("task edit", "needs a terminal for the editor")
	}
	w, t, err := a.openTask(ctx, *repo, pos[0])
	if err != nil {
		return a.storeFail("task edit", err)
	}
	f, err := os.CreateTemp("", "rig-task-*.md")
	if err != nil {
		return a.fail("task edit", "%v", err)
	}
	path := f.Name()
	_, werr := f.WriteString(editFile(w.wf, t))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(path)
		return a.fail("task edit", "write %s: %v", path, werr)
	}
	kept := func(format string, v ...any) int {
		return a.fail("task edit", "%s; nothing written, your copy is at %s", fmt.Sprintf(format, v...), path)
	}
	code, err := proc.Interactive(ctx, proc.Options{Dir: w.dir}, editor[0], append(editor[1:], path)...)
	if err != nil {
		return kept("run %s: %v", editor[0], err)
	}
	if code != 0 {
		return kept("%s exited %d", editor[0], code)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return kept("%v", err)
	}
	changed, err := parseEdit(w.wf, t, string(b))
	if err != nil {
		return kept("the file does not parse: %v", err)
	}
	after, err := w.updateFields(ctx, t, changed)
	if err != nil {
		var r *refusal
		if errors.As(err, &r) {
			fmt.Fprintf(a.stderr, "rig task edit: %s; nothing written, your copy is at %s\n", r.reason, path)
			return 1
		}
		return kept("%v", err)
	}
	os.Remove(path)
	return a.printTask(ctx, "task edit", w, after, *asJSON)
}

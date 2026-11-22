package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/workflow"
)

// takeRepeated takes the repeatable string flags in repeated out of args
// (`--name value` and `--name=value`) and returns what is left, with their
// values in order. The flags in others take a value too: it is kept as it
// is and never read as a flag, so a value that looks like a repeated flag is
// not mistaken for one.
func takeRepeated(args, repeated, others []string) (rest []string, vals map[string][]string, err error) {
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

// findingAdded is the --json shape of `rig finding add`.
type findingAdded struct {
	Finding   store.Finding `json:"finding"`
	Duplicate bool          `json:"duplicate"`
}

const findingUsage = `usage: rig finding add <task> --severity <sev> --summary <text> [--path <p[:line]>] [--key <k>] [--field <name>=<value>]... [--json] [--repo <dir>]
       rig finding list <task> [--status <s>]... [--blocking] [--json] [--repo <dir>]
       rig finding set <task> <finding> --status <s> [--json] [--repo <dir>]`

// finding implements `rig finding add|list|set`.
func (a *app) finding(ctx context.Context, args []string) int {
	if len(args) == 0 {
		return a.fail("finding", "%s", findingUsage)
	}
	switch args[0] {
	case "add":
		return a.findingAdd(ctx, args[1:])
	case "list":
		return a.findingList(ctx, args[1:])
	case "set":
		return a.findingSet(ctx, args[1:])
	}
	return a.fail("finding", "unknown subcommand %q\n%s", args[0], findingUsage)
}

func (a *app) findingAdd(ctx context.Context, args []string) int {
	args, rep, err := takeRepeated(args, []string{"--field"}, []string{"--severity", "--summary", "--path", "--key", "--repo"})
	if err != nil {
		return a.fail("finding add", "%v", err)
	}
	fieldArgs := rep["--field"]
	fs := newFlags()
	repoFlag, jsonOut := fs.str("--repo"), fs.boolean("--json")
	sev, summary, path, key := fs.str("--severity"), fs.str("--summary"), fs.str("--path"), fs.str("--key")
	pos, err := fs.parse(args)
	if err != nil || len(pos) != 1 || *sev == "" || strings.TrimSpace(*summary) == "" {
		return a.fail("finding add", "%s", findingUsage)
	}
	w, err := a.openWork(ctx, *repoFlag, true)
	if err != nil {
		return a.storeFail("finding add", err)
	}
	if !slices.Contains(w.wf.Findings.Severities, *sev) {
		return a.fail("finding add", "severity %q is not one of %s", *sev, strings.Join(w.wf.Findings.Severities, ", "))
	}
	var fields map[string]any
	for _, kv := range fieldArgs {
		name, raw, ok := strings.Cut(kv, "=")
		def, known := w.wf.Findings.Fields[name]
		if !ok || !known {
			return a.fail("finding add", "%q: want <name>=<value> for a finding field of %s", kv, w.wf.Name)
		}
		v, err := workflow.ParseFieldValue(def, raw)
		if err != nil {
			return a.fail("finding add", "field %s: %v", name, err)
		}
		if fields == nil {
			fields = map[string]any{}
		}
		fields[name] = v
	}
	if *key == "" {
		*key = store.DefaultKey(*sev, *path, *summary)
	}
	data := store.FindingData{Severity: *sev, Key: *key, Summary: *summary, Path: *path, Fields: fields}

	var dup *store.Finding
	var newID string
	task, err := w.st.Update(ctx, pos[0], func(_ store.Task, cur []store.Finding) ([]store.Event, error) {
		dup = nil
		if f, ok := store.FindOpenByKey(cur, *key); ok {
			dup = &f
			return nil, nil
		}
		// The id is chosen here so the finding can be found again by it,
		// whatever its status or key become before the read-back.
		d := data
		d.ID = store.NewULID(time.Now())
		newID = d.ID
		ev, err := store.NewEvent(store.KindFinding, d)
		return []store.Event{ev}, err
	})
	if err != nil {
		return a.storeFail("finding add", err)
	}
	var out store.Finding
	if dup != nil {
		out = *dup
	} else {
		all, err := w.st.Findings(ctx, task.ID)
		if err != nil {
			return a.storeFail("finding add", err)
		}
		i := slices.IndexFunc(all, func(f store.Finding) bool { return f.ID == newID })
		if i < 0 {
			return a.fail("finding add", "the new finding is missing from the store")
		}
		out = all[i]
	}
	if *jsonOut {
		return a.printJSON(findingAdded{Finding: out, Duplicate: dup != nil})
	}
	if dup != nil {
		fmt.Fprintf(a.stdout, "%s already open on %s: %s\n", out.Short, task.Short, out.Summary)
	} else {
		fmt.Fprintf(a.stdout, "%s %s on %s: %s\n", out.Short, out.Severity, task.Short, out.Summary)
	}
	return 0
}

func (a *app) findingList(ctx context.Context, args []string) int {
	args, rep, err := takeRepeated(args, []string{"--status"}, []string{"--repo"})
	if err != nil {
		return a.fail("finding list", "%v", err)
	}
	statuses := rep["--status"]
	fs := newFlags()
	repoFlag, jsonOut, blocking := fs.str("--repo"), fs.boolean("--json"), fs.boolean("--blocking")
	pos, err := fs.parse(args)
	if err != nil || len(pos) != 1 {
		return a.fail("finding list", "%s", findingUsage)
	}
	if len(statuses) == 0 {
		statuses = []string{store.StatusOpen}
	}
	for _, s := range statuses {
		if !validFindingStatus(s) {
			return a.fail("finding list", "status %q is not one of open, fixed, wontfix, deferred", s)
		}
	}
	w, err := a.openWork(ctx, *repoFlag, true)
	if err != nil {
		return a.storeFail("finding list", err)
	}
	all, err := w.st.Findings(ctx, pos[0])
	if err != nil {
		return a.storeFail("finding list", err)
	}
	out := []store.Finding{}
	for _, f := range all {
		if !slices.Contains(statuses, f.Status) {
			continue
		}
		if *blocking && !(f.Status == store.StatusOpen && w.wf.Blocking(f.Severity)) {
			continue
		}
		out = append(out, f)
	}
	sevs := w.wf.Findings.Severities
	rank := func(f store.Finding) int {
		if i := slices.Index(sevs, f.Severity); i >= 0 {
			return i
		}
		return len(sevs)
	}
	slices.SortStableFunc(out, func(x, y store.Finding) int {
		bx, by := w.wf.Blocking(x.Severity), w.wf.Blocking(y.Severity)
		switch {
		case bx != by && bx:
			return -1
		case bx != by:
			return 1
		case rank(x) != rank(y):
			return rank(x) - rank(y)
		case !x.Created.Equal(y.Created):
			if x.Created.Before(y.Created) {
				return -1
			}
			return 1
		}
		return strings.Compare(x.ID, y.ID)
	})
	if *jsonOut {
		return a.printJSON(out)
	}
	for _, f := range out {
		p := ""
		if f.Path != "" {
			p = "  " + f.Path
		}
		fmt.Fprintf(a.stdout, "%s  %s  %s  %s%s\n", f.Short, f.Severity, f.Status, f.Summary, p)
	}
	return 0
}

func validFindingStatus(s string) bool {
	switch s {
	case store.StatusOpen, store.StatusFixed, store.StatusWontfix, store.StatusDeferred:
		return true
	}
	return false
}

func (a *app) findingSet(ctx context.Context, args []string) int {
	fs := newFlags()
	repoFlag, jsonOut, status := fs.str("--repo"), fs.boolean("--json"), fs.str("--status")
	pos, err := fs.parse(args)
	if err != nil || len(pos) != 2 || *status == "" {
		return a.fail("finding set", "%s", findingUsage)
	}
	if !validFindingStatus(*status) {
		return a.fail("finding set", "status %q is not one of open, fixed, wontfix, deferred", *status)
	}
	w, err := a.openWork(ctx, *repoFlag, true)
	if err != nil {
		return a.storeFail("finding set", err)
	}
	var id string
	task, err := w.st.Update(ctx, pos[0], func(_ store.Task, cur []store.Finding) ([]store.Event, error) {
		id = ""
		i := slices.IndexFunc(cur, func(f store.Finding) bool { return f.ID == pos[1] || f.Short == pos[1] })
		if i < 0 {
			return nil, fmt.Errorf("finding %q: %w", pos[1], store.ErrNotFound)
		}
		id = cur[i].ID
		if cur[i].Status == *status {
			return nil, nil
		}
		ev, err := store.NewEvent(store.KindFindingStatus, store.FindingStatusData{Finding: cur[i].ID, Status: *status})
		return []store.Event{ev}, err
	})
	if err != nil {
		return a.storeFail("finding set", err)
	}
	all, err := w.st.Findings(ctx, task.ID)
	if err != nil {
		return a.storeFail("finding set", err)
	}
	i := slices.IndexFunc(all, func(f store.Finding) bool { return f.ID == id })
	if i < 0 {
		return a.fail("finding set", "finding %q is missing from the store", pos[1])
	}
	if *jsonOut {
		return a.printJSON(all[i])
	}
	fmt.Fprintf(a.stdout, "%s is %s\n", all[i].Short, all[i].Status)
	return 0
}

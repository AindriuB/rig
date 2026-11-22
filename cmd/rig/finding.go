package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/workflow"
)

// takeRepeated removes every `name value` and `name=value` from args and
// returns the rest with the values, in order. The flag parser keeps only the
// last value of a flag, so a repeatable flag is taken out first.
func takeRepeated(args []string, name string) (rest, vals []string, err error) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == name:
			if i+1 >= len(args) {
				return nil, nil, fmt.Errorf("%s needs a value", name)
			}
			i++
			vals = append(vals, args[i])
		case strings.HasPrefix(args[i], name+"="):
			vals = append(vals, strings.TrimPrefix(args[i], name+"="))
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
	args, fieldArgs, err := takeRepeated(args, "--field")
	if err != nil {
		return a.fail("finding add", "%v", err)
	}
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
	task, err := w.st.Update(ctx, pos[0], func(_ store.Task, cur []store.Finding) ([]store.Event, error) {
		dup = nil
		if f, ok := store.FindOpenByKey(cur, *key); ok {
			dup = &f
			return nil, nil
		}
		ev, err := store.NewEvent(store.KindFinding, data)
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
		i := slices.IndexFunc(all, func(f store.Finding) bool { return f.Status == store.StatusOpen && f.Key == *key })
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
	args, statuses, err := takeRepeated(args, "--status")
	if err != nil {
		return a.fail("finding list", "%v", err)
	}
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
	task, err := w.st.Update(ctx, pos[0], func(_ store.Task, cur []store.Finding) ([]store.Event, error) {
		i := slices.IndexFunc(cur, func(f store.Finding) bool { return f.ID == pos[1] || f.Short == pos[1] })
		if i < 0 {
			return nil, fmt.Errorf("finding %q: %w", pos[1], store.ErrNotFound)
		}
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
	i := slices.IndexFunc(all, func(f store.Finding) bool { return f.ID == pos[1] || f.Short == pos[1] })
	if i < 0 {
		return a.fail("finding set", "finding %q is missing from the store", pos[1])
	}
	if *jsonOut {
		return a.printJSON(all[i])
	}
	fmt.Fprintf(a.stdout, "%s is %s\n", all[i].Short, all[i].Status)
	return 0
}

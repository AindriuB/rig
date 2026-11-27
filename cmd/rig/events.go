package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/workflow"
)

const eventsUsage = "usage: rig events [--since <file> | --cursor-file <file>] [--from-start] [--follow] [--json] [--repo <dir>]"

// followEvery is how often --follow scans for new events.
const followEvery = time.Second

func init() {
	// Registered here so this command's flags stay with it.
	for _, n := range []string{"--cursor-file", "--follow", "--from-start", "--since"} {
		knownOptions[n] = true
	}
}

// eventLine is one --json item of rig events.
type eventLine struct {
	Format int                `json:"format"`
	Event  store.Event        `json:"event"`
	Task   *workflow.TaskView `json:"task"`
}

// events prints every event across all tasks and the repository's notes,
// resuming from a cursor file and optionally following new ones.
func (a *app) events(ctx context.Context, args []string) int {
	fs := newFlags()
	repoFlag, since, cursorFile := fs.str("--repo"), fs.str("--since"), fs.str("--cursor-file")
	fromStart, follow, jsonOut := fs.boolean("--from-start"), fs.boolean("--follow"), fs.boolean("--json")
	pos, err := fs.parse(args)
	if err != nil || len(pos) > 0 {
		return a.fail("events", "%s", eventsUsage)
	}
	if *since != "" && *cursorFile != "" {
		return a.fail("events", "--since and --cursor-file are exclusive; %s", eventsUsage)
	}
	w, err := a.openWork(ctx, *repoFlag, true)
	if err != nil {
		return a.storeFail("events", err)
	}
	feeder, ok := w.st.(store.Feeder)
	if !ok {
		return a.fail("events", "this store cannot list events across tasks")
	}
	path := *since
	if path == "" {
		path = *cursorFile
	}
	var cur *store.Cursor
	if path != "" {
		if cur, err = store.LoadCursor(path); err != nil {
			return a.fail("events", "%v", err)
		}
	}
	if cur == nil && *fromStart {
		cur = store.NewCursor()
	}
	for {
		res, err := feeder.Feed(ctx, cur)
		if err != nil {
			if ctx.Err() != nil {
				return 0
			}
			return a.storeFail("events", err)
		}
		for _, h := range res.Held {
			fmt.Fprintf(a.stderr, "rig events: %s held: %d unreadable lines in its log\n", h.Name, h.Skipped)
		}
		for _, it := range res.Items {
			if err := a.printEvent(w.wf, it, *jsonOut); err != nil {
				return a.fail("events", "%v", err)
			}
		}
		cur = res.Next
		if *cursorFile != "" {
			if err := cur.Save(*cursorFile); err != nil {
				return a.fail("events", "%v", err)
			}
		}
		if !*follow {
			return 0
		}
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(followEvery):
		}
	}
}

// printEvent writes one item. The task is shown as recorded right after the
// event, so State equals RecordedState and there is no branch head.
func (a *app) printEvent(wf *workflow.Workflow, it store.FeedItem, asJSON bool) error {
	if !asJSON {
		slug := "-"
		if it.Task != nil {
			slug = it.Task.Slug
		}
		_, err := fmt.Fprintf(a.stdout, "%s %s %s %s\n", it.Event.Time.Format(time.RFC3339), slug, it.Event.Kind, it.Event.Actor)
		return err
	}
	line := eventLine{Format: 1, Event: it.Event}
	if it.Task != nil {
		v := wf.View(*it.Task, "")
		v.State, v.HeadSha = v.RecordedState, ""
		line.Task = &v
	}
	b, err := json.Marshal(line)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.stdout, "%s\n", b)
	return err
}

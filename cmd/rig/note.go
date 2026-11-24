package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/AindriuB/rig/internal/store"
)

const noteUsage = `usage: rig note add [<task>] --kind <k> --text <text> [--actor <name>] [--json] [--repo <dir>]
       rig note list [<task>] [--kind <k>] [--limit <n>] [--json] [--repo <dir>]`

// note implements `rig note add|list`.
func (a *app) note(ctx context.Context, args []string) int {
	if len(args) == 0 {
		return a.fail("note", "%s", noteUsage)
	}
	switch args[0] {
	case "add":
		return a.noteAdd(ctx, args[1:])
	case "list":
		return a.noteList(ctx, args[1:])
	}
	return a.fail("note", "unknown subcommand %q\n%s", args[0], noteUsage)
}

func (a *app) noteAdd(ctx context.Context, args []string) int {
	fs := newFlags()
	actorOK := a.actorFlags(fs)
	repoFlag, jsonOut := fs.str("--repo"), fs.boolean("--json")
	kind, text := fs.str("--kind"), fs.str("--text")
	pos, err := fs.parse(args)
	if err != nil || len(pos) > 1 || *kind == "" || strings.TrimSpace(*text) == "" {
		return a.fail("note add", "%s", noteUsage)
	}
	if err := actorOK(); err != nil {
		return a.fail("note add", "%v", err)
	}
	w, err := a.openWork(ctx, *repoFlag, true)
	if err != nil {
		return a.storeFail("note add", err)
	}
	n := store.Note{Kind: *kind, Text: *text}
	if len(pos) == 1 {
		n.Task = pos[0]
	}
	got, err := w.st.AddNote(ctx, n)
	if err != nil {
		return a.storeFail("note add", err)
	}
	if *jsonOut {
		return a.printJSON(got)
	}
	fmt.Fprintf(a.stdout, "%s %s\n", got.Short, got.Kind)
	return 0
}

func (a *app) noteList(ctx context.Context, args []string) int {
	fs := newFlags()
	repoFlag, jsonOut := fs.str("--repo"), fs.boolean("--json")
	kind, limit := fs.str("--kind"), fs.str("--limit")
	pos, err := fs.parse(args)
	if err != nil || len(pos) > 1 {
		return a.fail("note list", "%s", noteUsage)
	}
	n := 0
	if *limit != "" {
		if n, err = strconv.Atoi(*limit); err != nil || n < 0 {
			return a.fail("note list", "--limit wants a non-negative integer, got %q", *limit)
		}
	}
	w, err := a.openWork(ctx, *repoFlag, true)
	if err != nil {
		return a.storeFail("note list", err)
	}
	f := store.NoteFilter{Kind: *kind, Limit: n}
	if len(pos) == 1 {
		f.Task = pos[0]
	}
	notes, err := w.st.Notes(ctx, f)
	if err != nil {
		return a.storeFail("note list", err)
	}
	if notes == nil {
		notes = []store.Note{}
	}
	if *jsonOut {
		return a.printJSON(notes)
	}
	for _, x := range notes {
		fmt.Fprintf(a.stdout, "%s  %s  %s\n", x.Short, x.Kind, x.Text)
	}
	return 0
}

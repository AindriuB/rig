// Command rig owns the loop's mechanical steps: pushing a task branch,
// reading and opening pull requests, waiting on CI and reporting status.
//
// Commands hold policy and output. They reach a forge only through the
// forge.Forge interface, and git only through internal/git.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/AindriuB/rig/internal/forge"
	"github.com/AindriuB/rig/internal/store"
)

// app is what a command can touch. Tests swap each field.
type app struct {
	stdout, stderr io.Writer
	getenv         func(string) string
	cwd            string
	// forge picks the adapter for the repository at dir.
	forge func(ctx context.Context, dir string) (forge.Forge, error)
	// store opens the task store for the repository at dir.
	store func(ctx context.Context, dir string) (store.Store, error)
	// now is the clock events are stamped with.
	now func() time.Time
}

const usage = `usage: rig <command>

  rig push <task-id|branch> [--worktree <path>]
  rig pr view [<pr|branch> | --task <id>] [--json] [--repo <dir>]
  rig pr list [--mine] [--head <branch>] [--state open|closed|merged] [--limit <n>] [--json] [--repo <dir>]
  rig pr checks [<pr|branch> | --task <id>] [--json] [--repo <dir>]
  rig pr open --head <branch> --title <text> [--base <branch>] [--body <text>] [--draft] [--repo <dir>]
  rig pr wait <pr-number | --run <run-id>> [--repo <dir>]
  rig status [--json]
  rig task new --slug <s> --title <text> [--body <text> | --body-file <path>] [--field <name>=<value>]... [--json] [--repo <dir>]
  rig task show <task> [--findings] [--notes] [--history] [--json] [--repo <dir>]
  rig task edit <task> [--json] [--repo <dir>]
  rig task list [--state <s>]... [--all] [--json] [--repo <dir>]
  rig task move <task> <state> [--sha <sha>] [--pr <n>] [--reason <text>] [--json] [--repo <dir>]
  rig task set <task> --field <name>=<value>... [--json] [--repo <dir>]
  rig task count <task> <counter> [--json] [--repo <dir>]
  rig task is <task> <state> [--repo <dir>]
  rig task log <task> [--json] [--repo <dir>]
  rig finding add <task> --severity <sev> --summary <text> [--path <p[:line]>] [--key <k>] [--field <name>=<value>]... [--json] [--repo <dir>]
  rig finding list <task> [--status <s>]... [--blocking] [--json] [--repo <dir>]
  rig finding set <task> <finding> --status <s> [--json] [--repo <dir>]
  rig note add [<task>] --kind <k> --text <text> [--json] [--repo <dir>]
  rig note list [<task>] [--kind <k>] [--limit <n>] [--json] [--repo <dir>]
  rig workflow show [--json] [--repo <dir>]
  rig workflow validate [<file>] [--json] [--repo <dir>]
  rig next [--who agent|human] [--limit <n>] [--json] [--repo <dir>]
  rig context <task> [--budget <tokens>] [--json] [--repo <dir>]
  rig version
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "rig:", err)
		os.Exit(2)
	}
	a := &app{stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv, cwd: cwd, forge: selectForge, now: time.Now}
	a.store = a.openStore
	os.Exit(a.run(ctx, os.Args[1:]))
}

func (a *app) run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(a.stderr, usage)
		return 2
	}
	switch args[0] {
	case "push":
		return a.push(ctx, args[1:])
	case "pr":
		return a.pr(ctx, args[1:])
	case "status":
		return a.status(ctx, args[1:])
	case "task":
		return a.task(ctx, args[1:])
	case "finding":
		return a.finding(ctx, args[1:])
	case "note":
		return a.note(ctx, args[1:])
	case "workflow":
		return a.workflow(ctx, args[1:])
	case "next":
		return a.next(ctx, args[1:])
	case "context":
		return a.context(ctx, args[1:])
	case "version", "--version":
		fmt.Fprintln(a.stdout, version)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(a.stdout, usage)
		return 0
	}
	fmt.Fprintf(a.stderr, "rig: unknown command %q\n%s", args[0], usage)
	return 2
}

// fail prints "rig <cmd>: message" and returns exit code 2.
func (a *app) fail(cmd, format string, args ...any) int {
	fmt.Fprintf(a.stderr, "rig %s: %s\n", cmd, fmt.Sprintf(format, args...))
	return 2
}

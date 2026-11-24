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
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/AindriuB/rig/internal/forge"
	"github.com/AindriuB/rig/internal/proc"
	"github.com/AindriuB/rig/internal/secret"
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
	// stdin is where login reads a user and token from.
	stdin *os.File
	// secrets opens the secret store of kind "keychain" or "file".
	secrets func(kind string) (secret.Store, error)
	// interactive runs a program on the user's terminal (gh auth login).
	interactive func(ctx context.Context, o proc.Options, name string, args ...string) (int, error)
	// httpClient makes rig's own HTTP calls (the Jira check).
	httpClient *http.Client
	// tracker builds the tracker adapter for a resolved spec; nil is the real one.
	tracker trackerBuilder
	// actorFlag is --actor/--role for this run; it beats $RIG_ACTOR.
	actorFlag string
}

const usage = `usage: rig <command>

  rig push <task-id|branch> [--worktree <path>]
  rig pr view [<pr|branch> | --task <id>] [--json] [--repo <dir>]
  rig pr list [--mine] [--head <branch>] [--state open|closed|merged] [--limit <n>] [--json] [--repo <dir>]
  rig pr checks [<pr|branch> | --task <id>] [--json] [--repo <dir>]
  rig pr open --head <branch> --title <text> [--base <branch>] [--body <text>] [--draft] [--repo <dir>]
  rig pr wait <pr-number | --run <run-id>> [--repo <dir>]
  rig status [--json]
  rig task new --slug <s> --title <text> [--body <text> | --body-file <path>] [--field <name>=<value>]... [--actor <name>] [--json] [--repo <dir>]
  rig task new --from [<kind>:]<key> [--state <s>] [--slug <s>] [--field <name>=<value>]... [--actor <name>] [--json] [--repo <dir>]
  rig task show <task> [--findings] [--notes] [--history] [--children] [--format <text|json|markdown> | --json | --markdown] [--repo <dir>]
  rig task edit <task> [--actor <name>] [--json] [--repo <dir>]
  rig task list [--state <s>]... [--all] [--parent <task> | --roots | --tree] [--format <text|json|markdown> | --json | --markdown] [--repo <dir>]
  rig task parent <task> <parent|none> [--actor <name>] [--json] [--repo <dir>]
  rig task link <task> [<kind>:]<key> [--actor <name>] [--json] [--repo <dir>]
  rig task unlink <task> [--actor <name>] [--json] [--repo <dir>]
  rig task move <task> <state> [--sha <sha>] [--pr <n>] [--reason <text>] [--actor <name>] [--json] [--repo <dir>]
  rig task set <task> --field <name>=<value>... [--actor <name>] [--json] [--repo <dir>]
  rig task count <task> <counter> [--actor <name>] [--json] [--repo <dir>]
  rig task start <slug> [--task-file <path>] [--json] [--repo <dir>]
  rig task finish <task> --sha <sha> [--json] [--repo <dir>]
  rig task is <task> <state> [--repo <dir>]
  rig task log <task> [--json] [--repo <dir>]
  rig finding add <task> --severity <sev> --summary <text> [--path <p[:line]>] [--key <k>] [--field <name>=<value>]... [--actor <name>] [--json] [--repo <dir>]
  rig finding list <task> [--status <s>]... [--blocking] [--json] [--repo <dir>]
  rig finding set <task> <finding> --status <s> [--actor <name>] [--json] [--repo <dir>]
  rig note add [<task>] --kind <k> --text <text> [--actor <name>] [--json] [--repo <dir>]
  rig note list [<task>] [--kind <k>] [--limit <n>] [--json] [--repo <dir>]
  rig workflow show [--json] [--repo <dir>]
  rig workflow validate [<file>] [--json] [--repo <dir>]
  rig next [--who agent|human] [--limit <n>] [--json] [--repo <dir>]
  rig context <task> [--budget <tokens>] [--format <text|json|markdown> | --json | --markdown] [--repo <dir>]
  rig config get <key> [--global|--repo|--local] [--json]
  rig config set <key> <value> --global|--repo|--local
  rig config unset <key> --global|--repo|--local
  rig config list [--global|--repo|--local] [--show-origin] [--json]
  rig tracker pull <key> | --query <q> | --label <l> [--children[=<depth>]] [--limit <n>] [--state <s>] [--actor <name>] [--json] [--repo <dir>]
  rig tracker refresh [<task> | --all] [--dry-run] [--actor <name>] [--json] [--repo <dir>]
  rig tracker report <task> [--status] [--comment pr|state|summary] [--include findings|notes]... [--dry-run] [--actor <name>] [--json] [--repo <dir>]
  rig tracker show <key> [--json] [--repo <dir>]
  rig tracker status [--json] [--repo <dir>]
  rig login                                  (the forge, then Jira when tracker.kind is jira)
  rig login <service> [--url <site>] [--user <email>] [--pat] [--repo] [--store keychain|file]   (github, gitlab or jira; the flags are for jira)
  rig login status [--json]
  rig logout <service> [--url <site>] [--repo]   (jira; github and gitlab print the gh/glab command)
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
	a.stdin, a.secrets, a.interactive = os.Stdin, a.openSecrets, proc.Interactive
	a.httpClient = &http.Client{Timeout: 15 * time.Second}
	os.Exit(a.run(ctx, os.Args[1:]))
}

func (a *app) run(ctx context.Context, args []string) int {
	// --actor belongs to this run only; a reused app (tests) must not
	// stamp one run's events with another run's flag.
	a.actorFlag = ""
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
	case "config":
		return a.config(ctx, args[1:])
	case "tracker":
		return a.trackerCmd(ctx, args[1:])
	case "login":
		return a.login(ctx, args[1:])
	case "logout":
		return a.logout(ctx, args[1:])
	case "version", "--version":
		if len(args) > 1 {
			fmt.Fprintln(a.stderr, "usage: rig version")
			return 2
		}
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

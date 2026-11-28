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
	"runtime"
	"syscall"
	"time"

	"github.com/AindriuB/rig/internal/forge"
	"github.com/AindriuB/rig/internal/proc"
	"github.com/AindriuB/rig/internal/secret"
	"github.com/AindriuB/rig/internal/selfupdate"
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
	// onceKey is --once for this run (or its default inside a hook), and
	// onceJSON says a repeat is reported as JSON; see onceFlag.
	onceKey  string
	onceJSON bool
	// updateTransport is rig update's HTTP transport; nil is the default.
	updateTransport http.RoundTripper
	// updater runs an update; nil is selfupdate.Update.
	updater func(context.Context, selfupdate.Options) selfupdate.Result
	// exe is the path of the running rig; nil is os.Executable.
	exe func() (string, error)
	// goos is the OS rig believes it runs on; empty is runtime.GOOS.
	goos string
	// stderrTTY says whether stderr is a terminal; nil asks the OS.
	stderrTTY func() bool
	// autoRun is the update.auto pass; nil is selfupdate.Auto.
	autoRun func(context.Context, selfupdate.AutoOptions)
}

const usage = `usage: rig <command>

Tasks:
  rig task new                              Create a task from a slug and a title
      --slug <s> --title <text> [--body <text> | --body-file <path>] [--field <name>=<value>]...
      [--actor <name>] [--once <k>] [--json] [--repo <dir>]
  rig task new                              Create a task from a tracker issue
      --from [<kind>:]<key> [--state <s>] [--slug <s>] [--field <name>=<value>]...
      [--actor <name>] [--once <k>] [--json] [--repo <dir>]
  rig task show <task>                      Show a task, with its findings, notes or history
      [--findings] [--notes] [--history] [--children]
      [--format <text|json|markdown> | --json | --markdown] [--repo <dir>]
  rig task edit <task>                      Edit a task in your editor
      [--actor <name>] [--once <k>] [--json] [--repo <dir>]
  rig task list                             List tasks, as a flat list or a tree
      [--state <s>]... [--all] [--parent <task> | --roots | --tree]
      [--format <text|json|markdown> | --json | --markdown] [--repo <dir>]
  rig task parent <task> <parent|none>      Set a task's parent, or clear it with none
      [--actor <name>] [--once <k>] [--json] [--repo <dir>]
  rig task link <task> [<kind>:]<key>       Link a task to a tracker issue
      [--actor <name>] [--once <k>] [--json] [--repo <dir>]
  rig task unlink <task>                    Remove a task's tracker link
      [--actor <name>] [--once <k>] [--json] [--repo <dir>]
  rig task move <task> <state>              Move a task to another workflow state
      [--sha <sha>] [--pr <n>] [--reason <text>]
      [--actor <name>] [--once <k>] [--json] [--repo <dir>]
  rig task set <task>                       Set fields on a task
      --field <name>=<value>... [--actor <name>] [--once <k>] [--json] [--repo <dir>]
  rig task count <task> <counter>           Increment a task counter
      [--actor <name>] [--once <k>] [--json] [--repo <dir>]
  rig task start <slug>                     Create the task's branch and worktree
      [--task-file <path>] [--json] [--repo <dir>]
  rig task finish <task>                    Merge a task's verified commit, remove worktree
      --sha <sha> [--json] [--repo <dir>]
  rig task is <task> <state>                Exit 0 when the task is in the given state
      [--repo <dir>]
  rig task log <task>                       Show a task's history
      [--json] [--repo <dir>]

Findings and notes:
  rig finding add <task>                    Record a review finding on a task
      --severity <sev> --summary <text> [--path <p[:line]>] [--key <k>] [--field <name>=<value>]...
      [--actor <name>] [--once <k>] [--json] [--repo <dir>]
  rig finding list <task>                   List a task's findings
      [--status <s>]... [--blocking] [--json] [--repo <dir>]
  rig finding set <task> <finding>          Change a finding's status
      --status <s> [--actor <name>] [--once <k>] [--json] [--repo <dir>]
  rig note add [<task>]                     Add a note to a task
      --kind <k> --text <text> [--actor <name>] [--once <k>] [--json] [--repo <dir>]
  rig note list [<task>]                    List notes
      [--kind <k>] [--limit <n>] [--json] [--repo <dir>]

Planning:
  rig workflow show                         Show the workflow definition
      [--json] [--repo <dir>]
  rig workflow validate [<file>]            Check a workflow file for errors
      [--json] [--repo <dir>]
  rig next                                  List the tasks that are ready to work on
      [--who agent|human] [--limit <n>] [--json] [--repo <dir>]
  rig context <task>                        Print the context an agent needs for a task
      [--budget <tokens>] [--format <text|json|markdown> | --json | --markdown] [--repo <dir>]
  rig status                                Show a summary of tasks by state
      [--json]

Branches and pull requests:
  rig push <task-id|branch>                 Push a task or record branch to origin
      [--worktree <path>]
  rig pr view                               Show a pull request
      [<pr|branch> | --task <id>] [--json] [--repo <dir>]
  rig pr list                               List pull requests
      [--mine] [--head <branch>] [--state open|closed|merged] [--limit <n>] [--json] [--repo <dir>]
  rig pr checks                             Show a pull request's CI checks
      [<pr|branch> | --task <id>] [--json] [--repo <dir>]
  rig pr open                               Open a pull request
      --head <branch> --title <text> [--base <branch>] [--body <text>] [--draft] [--repo <dir>]
  rig pr wait                               Wait for CI on a pull request or run to finish
      <pr-number | --run <run-id>> [--repo <dir>]

Tracker:
  rig tracker pull                          Import tracker issues as tasks
      <key> | --query <q> | --label <l> [--children[=<depth>]] [--limit <n>] [--state <s>]
      [--actor <name>] [--json] [--repo <dir>]
  rig tracker push <task>                   Create or update tracker issues from tasks
      [--tree] [--dry-run] [--actor <name>] [--json] [--repo <dir>]
  rig tracker refresh                       Refresh tasks from their tracker issues
      [<task> | --all] [--dry-run] [--actor <name>] [--json] [--repo <dir>]
  rig tracker report <task>                 Post a task's status or a comment to its tracker issue
      [--status] [--comment pr|state|summary] [--include findings|notes]... [--dry-run]
      [--actor <name>] [--once <k>] [--json] [--repo <dir>]
  rig tracker show <key>                    Show a tracker issue
      [--json] [--repo <dir>]
  rig tracker status                        Check the tracker connection and configuration
      [--json] [--repo <dir>]

Hooks:
  rig hooks start <cmd> [<args>...]         Start long-running work that outlives the hook
      [--json]
  rig hooks deliver                         Deliver queued hooks' events, at least once
      [--follow] [--repo <dir>]
  rig hooks status                          Show queued hooks' runners and parked events
      [--json] [--repo <dir>]
  rig hooks retry <hook>                    Release a hook's parked events for another try
      [--repo <dir>]
  rig hooks skip <hook> <event>             Drop a parked event so it is never delivered
      [--repo <dir>]
  rig events                                List events across all tasks, from a cursor
      [--since <file> | --cursor-file <file>] [--from-start] [--follow] [--json] [--repo <dir>]

Setup:
  rig config get <key>                      Print a config value
      [--global|--repo|--local] [--json]
  rig config set <key> <value>              Set a config value
      --global|--repo|--local
  rig config unset <key>                    Remove a config value
      --global|--repo|--local
  rig config list                           List config values
      [--global|--repo|--local] [--show-origin] [--json]
  rig login                                 Log in to the forge, then Jira when tracker.kind is jira
  rig login <service>                       Log in to github, gitlab or jira; the flags are for jira
      [--url <site>] [--user <email>] [--token] [--repo] [--store keychain|file]
  rig login status                          Show which services you are logged in to
      [--json]
  rig logout <service>                      Log out of jira; github/gitlab print the gh/glab command
      [--url <site>] [--repo]
  rig update                                Update rig to the latest release
      [--check] [--to <version>] [--json]
  rig version                               Print the rig version
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
	os.Exit(a.runAndAutoUpdate(ctx, os.Args[1:]))
}

func (a *app) run(ctx context.Context, args []string) int {
	// --actor belongs to this run only; a reused app (tests) must not
	// stamp one run's events with another run's flag.
	a.actorFlag = ""
	a.onceKey, a.onceJSON = "", false
	// Only the Windows swap leaves <rig>.old; elsewhere it is the user's file.
	if a.goos == "windows" || (a.goos == "" && runtime.GOOS == "windows") {
		cleanupOld(a.executable)
	}
	if len(args) == 0 {
		fmt.Fprint(a.stderr, shortUsage())
		return 2
	}
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		if h, ok := commandHelp(args[0]); ok {
			fmt.Fprint(a.stdout, h)
			return 0
		}
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
	case "hooks":
		return a.hooks(ctx, args[1:])
	case "events":
		return a.events(ctx, args[1:])
	case "login":
		return a.login(ctx, args[1:])
	case "logout":
		return a.logout(ctx, args[1:])
	case "update":
		return a.update(ctx, args[1:])
	case "version", "--version":
		if len(args) > 1 {
			fmt.Fprintln(a.stderr, "usage: rig version")
			return 2
		}
		fmt.Fprintln(a.stdout, version)
		return 0
	case "help", "-h", "--help":
		return a.help(args[1:])
	}
	fmt.Fprintf(a.stderr, "rig: unknown command %q\n%s", args[0], shortUsage())
	return 2
}

// fail prints "rig <cmd>: message" and returns exit code 2.
func (a *app) fail(cmd, format string, args ...any) int {
	fmt.Fprintf(a.stderr, "rig %s: %s\n", cmd, fmt.Sprintf(format, args...))
	return 2
}

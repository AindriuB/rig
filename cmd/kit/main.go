// Command kit owns the loop's mechanical steps: pushing a task branch,
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

	"github.com/AindriuB/workspace/cli/internal/forge"
)

// app is what a command can touch. Tests swap each field.
type app struct {
	stdout, stderr io.Writer
	getenv         func(string) string
	cwd            string
	// forge picks the adapter for the repository at dir.
	forge func(ctx context.Context, dir string) (forge.Forge, error)
}

const usage = `usage: kit <command>

  kit push <task-id|branch> [--worktree <path>]
  kit pr view [<pr|branch> | --task <id>] [--json] [--repo <dir>]
  kit pr list [--mine] [--head <branch>] [--state open|closed|merged] [--limit <n>] [--json] [--repo <dir>]
  kit pr checks [<pr|branch> | --task <id>] [--json] [--repo <dir>]
  kit pr open --head <branch> --title <text> [--base <branch>] [--body <text>] [--draft] [--repo <dir>]
  kit pr wait <pr-number | --run <run-id>> [--repo <dir>]
  kit status [--json]
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kit:", err)
		os.Exit(2)
	}
	a := &app{stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv, cwd: cwd, forge: selectForge}
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
	case "help", "-h", "--help":
		fmt.Fprint(a.stdout, usage)
		return 0
	}
	fmt.Fprintf(a.stderr, "kit: unknown command %q\n%s", args[0], usage)
	return 2
}

// fail prints "kit <cmd>: message" and returns exit code 2.
func (a *app) fail(cmd, format string, args ...any) int {
	fmt.Fprintf(a.stderr, "kit %s: %s\n", cmd, fmt.Sprintf(format, args...))
	return 2
}

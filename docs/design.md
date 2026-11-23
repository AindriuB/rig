# `rig`: generic actions for agentic workflows

Status: phase 1 was released as 0.1.0. Phases 2 and 2.5 ship together as
0.3.0 and are described in [phase-2.md](phase-2.md) and
[phase-2.5.md](phase-2.5.md); [workflows.md](workflows.md) documents the
workflow file. Later phases are outlines, each planned separately before it is
built.

**Direction.** rig provides generic actions (tasks, findings, notes,
workflow transitions, push, pull requests, CI waits, context briefs, config,
login) that any loop can use, driven by any harness: Claude Code, other agent
CLIs, CI or a person. It has no built-in lifecycle: a workflow file defines
states, transitions and the gates that guard them. A review loop is one
such workflow, owned by the project that runs it. The sections below that
describe such a loop are the history of how rig started.

`rig` is its own tool. It began as a subdirectory of an agent toolkit and
moved here with its history in phase 1.5. Its callers use it only through the
contract below.

## Why

rig started as the mechanics of an agent review loop: plan → fanout →
review → verify → record. In that loop, state and rules lived in prose and
shell text:

- **State is markdown the model re-reads.** Task state is parsed out of
  `## Review round N` and `## Wave verified — <sha>` sections, then confirmed
  with `gh` and `git` on every turn.
- **Rules are enforced by parsing shell.** `scripts/push-guard.sh` decides
  whether a Bash command is a safe push by matching its text. A regex cannot
  understand bash: extending the guard to chained commands took four review
  rounds, each of which found another way to push to `main`, and was reverted.
- **It is tied to GitHub.** `gh` is called from `scripts/ci-wait.sh` and from
  the `status`, `wave`, `merged` and `handoff` commands.
- **It does not scale past one developer.** Sequential task numbers collide,
  and `PLAN.md` and `HISTORY.md` conflict on merge.

`rig` is a small Go program that owns the mechanical parts: pushing, pull
requests, CI waits and status now; tasks, findings and context later. A command
that takes typed arguments can refuse a bad push by construction, where a guard
has to recognise one in arbitrary shell.

## Decisions

- **Go, standard library where possible.** One static binary, no runtime.
- **Forge adapters only.** `gh` now, `glab` next. Git is called directly; there
  is no git abstraction to swap.
- **The forge is auto-detected** from the `origin` remote's host.
  `.claude/rig.json` overrides it.
- **rig holds a token only for a service with no CLI of its own.** GitHub and
  GitLab stay with `gh` and `glab`: the adapters shell out to them and use
  whatever login the user already has, and `rig login github|gitlab` only
  hands over to `gh auth login` or `glab auth login`. For Jira, which has no
  such tool, `rig login jira` stores a token, in the OS keychain unless the
  user explicitly chooses `--store file`. No config key holds a token or an
  account; see [phase-2.5.md](phase-2.5.md).
- **No built-in workflow.** States, transitions, gates, severities and task
  fields come from a workflow file; rig ships only a small `basic` one.
  What rig enforces whatever the workflow says is listed in
  [phase-2.md](phase-2.md#what-rig-enforces-whatever-the-workflow-says).
- **Harness-neutral.** The CLI with `--json` and exit codes, and later an
  MCP server, are the only surfaces. Nothing assumes Claude Code.
- **`scripts/push-guard.sh` is unchanged.** It stays as the backstop for a raw
  `git push`. `rig push` is the allowed route; the guard is not extended.

## Architecture

Three layers. Only adapters know a technology.

```
./
  cmd/rig/              commands: policy and output
  internal/forge/       port: Forge interface, PullRequest, Check
  internal/store/       port: Store interface (phase 2)
  internal/forge/github adapter: runs `gh … --json`
  internal/forge/gitlab adapter: runs `glab`
  internal/forge/fake   adapter: in memory, for tests
  internal/git/         rev-parse, push, worktree list, merge-tree
  internal/proc/        shell-free, time-limited subprocess
  internal/config/      .claude/rig.json
```

1. **Commands** (`cmd/rig`) hold policy and output. Text by default,
   `--json` for agents. A command never calls `gh`, `glab` or a store backend
   directly; it calls a port.
2. **Ports** (`internal/forge`, `internal/store`) are interfaces in neutral
   types. Nothing in a port names GitHub or GitLab.
3. **Adapters** implement a port for one technology. They translate that
   tool's JSON and vocabulary into the neutral types.

Supporting packages:

- `internal/proc` runs a subprocess from an argument vector with a deadline. It
  never builds a shell string, and it kills the child when the deadline passes.
- `internal/git` wraps the handful of git calls rig needs.
- `internal/config` reads `.claude/rig.json`:

  | Key | Meaning | Default |
  |---|---|---|
  | `forge` | `github` or `gitlab` | detected from the origin host |
  | `host` | forge host, for self-hosted instances | the origin host |
  | `pushPrefixes` | branch prefixes `rig push` may push | `["task", "record"]` |

Install with `go install github.com/AindriuB/rig/cmd/rig@<tag>`. A caller
that installs rig for its users can pin a tag and check `rig version` against
the minimum it needs.

## The contract between rig and its callers

A caller talks to rig only through its commands, their exit codes and their
`--json` output. It never imports rig's code or reads its files, and rig
never reads the caller's. That is what lets rig live in another repository and
be versioned on its own.

`rig version` prints a semantic version (`0.1.0` for phase 1, `0.3.0` with
phases 2 and 2.5) and exits 0. A
new command, flag or JSON field raises the minor version. A change to
anything in the tables below raises the major version.

**Stable commands and exit codes**

| Command | Exit codes |
|---|---|
| `rig version` | 0 |
| `rig push <task-id\|branch> [--worktree <path>]` | 0 pushed; 1 git ran and the push failed; 2 refused, git push never started |
| `rig pr view [<pr\|branch> \| --task <id>] [--json] [--repo <dir>]` | 0; 2 on any error, including no such pull request |
| `rig pr checks [<pr\|branch> \| --task <id>] [--json] [--repo <dir>]` | 0; 2 on any error |
| `rig pr list [--mine] [--head <branch>] [--state open\|closed\|merged] [--limit <n>] [--json] [--repo <dir>]` | 0; 2 on any error |
| `rig pr open --head <branch> --title <text> [--base <branch>] [--body <text>] [--draft] [--repo <dir>]` | 0; 2 on any error |
| `rig pr wait <pr-number \| --run <run-id>> [--repo <dir>]` | 0 green; 1 red; 2 error or timeout |
| `rig status [--json]` | 0; 2 on a usage error |
| `rig task new --slug <s> --title <text> [--body <text> \| --body-file <path>] [--field <name>=<value>]… [--json] [--repo <dir>]` | 0; 2 on any error |
| `rig task show <task> [--findings] [--notes] [--history] [--json] [--repo <dir>]` | 0; 2 on any error |
| `rig task edit <task> [--json] [--repo <dir>]` | 0 saved, or nothing changed; 1 a field changed underneath the edit, nothing written; 2 on any error, including no terminal and no `$VISUAL`/`$EDITOR`, or a file that does not parse |
| `rig task list [--state <s>]… [--all] [--json] [--repo <dir>]` | 0; 2 on any error |
| `rig task move <task> <state> [--sha <sha>] [--pr <n>] [--reason <text>] [--json] [--repo <dir>]` | 0 moved; 1 refused by a gate or the workflow, nothing written; 2 on any error |
| `rig task set <task> --field <name>=<value>… [--json] [--repo <dir>]` | 0; 1 a field changed underneath, nothing written; 2 on any error |
| `rig task count <task> <counter> [--json] [--repo <dir>]` | 0; 2 on any error |
| `rig task is <task> <state> [--repo <dir>]` | 0 the effective state is `<state>`; 1 it is not; 2 on any error |
| `rig task log <task> [--json] [--repo <dir>]` | 0; 2 on any error |
| `rig finding add <task> --severity <sev> --summary <text> [--path <p[:line]>] [--key <k>] [--field <name>=<value>]… [--json] [--repo <dir>]` | 0, also for a duplicate; 2 on any error |
| `rig finding list <task> [--status <s>]… [--blocking] [--json] [--repo <dir>]` | 0; 2 on any error |
| `rig finding set <task> <finding> --status <s> [--json] [--repo <dir>]` | 0; 2 on any error |
| `rig note add [<task>] --kind <k> --text <text> [--json] [--repo <dir>]` | 0; 2 on any error |
| `rig note list [<task>] [--kind <k>] [--limit <n>] [--json] [--repo <dir>]` | 0; 2 on any error |
| `rig workflow show [--json] [--repo <dir>]` | 0; 2 on any error |
| `rig workflow validate [<file>] [--json] [--repo <dir>]` | 0 valid; 1 invalid; 2 on any error |
| `rig next [--who agent\|human] [--limit <n>] [--json] [--repo <dir>]` | 0; 2 on any error |
| `rig context <task> [--budget <tokens>] [--json] [--repo <dir>]` | 0; 2 on any error |
| `rig config get <key> [--global\|--repo\|--local] [--json]` | 0; 1 not set; 2 error or unknown key |
| `rig config set <key> <value> --global\|--repo\|--local` | 0; 2 on any error |
| `rig config unset <key> --global\|--repo\|--local` | 0, also when not set; 2 on any error |
| `rig config list [--global\|--repo\|--local] [--show-origin] [--json]` | 0; 2 on any error |
| `rig login` | 0; 1 a login failed; 2 on any error |
| `rig login github\|gitlab\|jira [--url <site>] [--user <email>] [--repo] [--store keychain\|file]` | 0; 1 the login failed or Jira rejected the token, nothing stored; 2 on any error |
| `rig login status [--json]` | 0; 2 on any error |
| `rig logout jira [--url <site>] [--repo]` | 0, also when already logged out; 2 on any error, including a store that could not be checked when the entry was found nowhere |
| `rig logout github\|gitlab` | 2: prints the `gh`/`glab` command that does it |

An unknown flag is always exit 2: rig never ignores an argument it does not
understand. In the phase-2 commands, exit 1 means refused by a gate or the
workflow with nothing written, and exit 2 covers usage errors, an unknown or
ambiguous task, a bad workflow file, and store or git errors. The commands'
flags and behaviour are described in [phase-2.md](phase-2.md) and
[phase-2.5.md](phase-2.5.md), and the workflow file in
[workflows.md](workflows.md). `RIG_ACTOR` names who acted and `RIG_WORKFLOW`
the workflow to use; `RIG_JIRA_TOKEN` overrides a stored Jira token at use
time.

**Stable text output**

- `rig pr wait`, green: exactly one line, `CI green: PR #<n>` or
  `CI green: run <id>`. Red: `CI red: …` first, then the failed names, then
  at most 40 log lines after `--- log tail ---`. Errors go to stderr with the
  prefix `ci-wait: `.
- `rig pr list`, and the pull request lines of `rig status`:
  `#<n>  <ci>  <head>  <title, at most 50 characters>`, then `  conflict`
  and `  draft` when they apply.
- `rig push`, on success: `pushed <branch> to origin`.

**Stable JSON fields**

A pull request, from `rig pr view --json` and each element of
`rig pr list --json`:

| Field | Value |
|---|---|
| `number` | integer |
| `url`, `title`, `head`, `headSha`, `base` | strings |
| `state` | `open`, `closed` or `merged` |
| `draft` | boolean |
| `mergeable` | `mergeable`, `conflicting` or `unknown` |
| `mergeSha` | the merge commit; present only once merged |
| `ci` | `red`, `running`, `green` or `none` |
| `checks` | array of `{name, state, url}`; `state` is `pass`, `fail`, `cancel`, `pending` or `skipped` |

`rig pr checks --json` is `{number, ci, checks}` with the same meanings.

`rig status --json` is `{handoff, tasks, worktrees, prs, prsError}`:
`handoff` is `{written, lines}` or absent; `tasks` is an array of task file
names without `.md`; each worktree is `{path, branch, ahead, dirty}`; `prs`
holds pull requests as above; `prsError` is present when the forge could not
be reached, and the rest of the output is still valid.

## What `rig push` does and does not guarantee

Any command line that starts `rig push` can only make a non-forced push of
one local branch named `<prefix>/<name>` to the branch of the same name on
`origin`. It cannot name `main` or any bare branch, a tag, a refspec, another
remote, or a force or delete option. Exit 2 means `git push` never started.

Two things sit outside that guarantee.

- **`.claude/rig.json` is the user's file.** Its `pushPrefixes` decide which
  prefixes count, so whoever can write it can widen the pushes: to
  `release/x`, say, though still never to a bare branch, a tag or a force.
  An agent must not edit it. The project settings template makes any Edit or
  Write of `.claude/rig.json`, in the project or in a worktree below it, and any shell
  command that names `rig.json`, ask first (`tests/rig-settings.bats` pins
  those rules). A permission rule matches text, so this is a barrier, not a
  proof: a shell command can build the file name without spelling it, and
  two commands the template already allows can write and run a file
  (`git log --output=<path>` and `rg --pre <program>`). Narrowing those allow
  rules is a separate change.
- **Git's own configuration is not constrained.** A pre-push hook, or
  `remote.origin.receivepack`, runs whatever it says during any push, and an
  agent can set either with `git config`. `rig push` runs the repository's
  hooks like any push does, so it does not close this. The raw
  `git push origin task/<name>` form the push guard allows has the same
  limit. Closing it would mean pushing with hooks disabled, which would also
  disable the hooks a project relies on; that trade has not been made.

## The forge port

```go
type Forge interface {
    ViewPR(ctx context.Context, ref string) (PullRequest, error)
    ListPRs(ctx context.Context, opts ListOptions) ([]PullRequest, error)
    CreatePR(ctx context.Context, in CreatePRInput) (PullRequest, error)
    Checks(ctx context.Context, pr int) ([]Check, error)
    WaitChecks(ctx context.Context, target WaitTarget) (WaitResult, error)
    FailedLogs(ctx context.Context, target WaitTarget, failed []Check) ([]LogLine, error)
}

type PullRequest struct {
    Number    int
    URL       string
    Title     string
    State     PRState   // open | closed | merged
    Draft     bool
    Head      string    // head branch
    HeadSHA   string
    Base      string
    Mergeable Mergeable // mergeable | conflicting | unknown
    Checks    []Check
}

type Check struct {
    Name  string
    State CheckState // pass | fail | cancel | pending | skipped
    URL   string
}
```

`WaitTarget` is a pull request number or a CI run id. Later phases add `Merge`,
`Comment` and `Reviews`.

A pull request's overall CI state is derived from its checks in one place, in
the port, so both adapters report it the same way: `red` if any check failed or
was cancelled, else `running` if any is pending, else `green` if there is at
least one check, else `none`.

## Phase 1 commands

| Command | What it does | Exit codes |
|---|---|---|
| `rig push <task-id\|branch> [--worktree <path>]` | Pushes one loop branch to `origin` | 0 pushed, 1 rejected by the remote, 2 refused |
| `rig pr view\|list\|checks` | Prints neutral output from the forge | 0, 2 on error |
| `rig pr open --head <branch> …` | Opens a pull request. Never pushes, and takes its body only from `--body`, never from a file | 0, 2 on error |
| `rig pr wait <pr>` or `--run <id>` | Blocks until CI finishes | 0 green, 1 red, 2 error or timeout |
| `rig status [--json]` | The session brief, worktrees and one line per open PR | 0 |

**`rig push` is the security-relevant command.** It resolves a task id to its
branch (`task/<id>-<slug>`, the slug taken from `docs/plan/tasks/<id>-*.md`,
the same lookup as `derive_slug` in `scripts/_wt-common.sh`). It then refuses,
with exit 2 and without running git, anything that is not a plain branch name
under a configured prefix: a refspec (`a:b`), a `+` prefix, a `..` segment, a
tag, any force or delete flag, and any unknown flag. What remains is run as
`git -C <worktree> push -u origin <branch>:<branch>` — an argument vector, so
there is nothing for a shell to reinterpret.

**`rig pr wait` keeps the contract of `scripts/ci-wait.sh`**, so callers do not
change: the same exit codes, `CI green: PR #N` as the only line when green, the
failed checks and a 40-line log tail when red, and `CI_WAIT_TIMEOUT` (seconds,
default 1800). `ci-wait.sh` becomes a wrapper that execs `rig pr wait`.

**`rig status`** prints one line per open pull request in the format
`commands/status.md` already uses:
`#N  red|running|green|none  branch  title [conflict] [draft]`.

Phase 1 is read-only on documents. State still comes from the existing
`docs/plan` files, git and the forge.

## The store and workflows (phase 2)

A store holds work in progress outside the code tree: one append-only event
log per task under `refs/rig/tasks/<ULID>`, local to the clone unless the
user opts in to sharing it. A task's state is a fold over its events. Which
states exist, which transitions are allowed and which gates guard them come
from a workflow file, not from rig. Tasks, findings and notes carry typed
fields the workflow declares. [phase-2.md](phase-2.md) has the format, the
commands and the extension points.

The state machine this section used to define (planned → implementing →
reviewed → verified@sha → pr_open → ci_green → ready → merged) is now
an example workflow file, shown in phase-2.md as the worked example.

## Phases

| Phase | What it delivers |
|---|---|
| 0 | This document, and a baseline of permission prompts and tokens per task |
| 1 | `rig push`, `rig pr`, `rig status`, `rig version`; the github and gitlab adapters |
| 1.5 | `cli/` moves to its own repository, `AindriuB/rig`, with its history |
| 2 | Shipped in 0.3.0. Generic core: the store port and git-ref backend, workflow files with gates and hooks, `rig task`, `rig finding`, `rig note`, `rig workflow`, `rig next`, `rig context`. An example review loop is a test fixture, not built in |
| 2.5 | Shipped in 0.3.0. `rig config` (global, per-repo and local scopes; forge and tracker settings) and `rig login`/`rig logout` with keychain-held tokens |
| 3 | Views and exports: Markdown and HTML renderings of tasks, notes and history from the store, by template, for any workflow. Importers for existing plans (markdown `docs/plan` task files first) as programs the user runs |
| 4 | Team scale: opt-in `rig sync` of the store, `rig claim`, retention and `rig gc`, a search index rebuilt from refs, and tracker adapters (`jira`, `github`, `gitlab`, and `exec` for any program) |
| 5 | Metrics from event logs, for any workflow: time in each state, counter values, stop reasons, findings by path |
| 6 | An MCP server over the same core; each workflow transition becomes a typed tool |
| 1.0 | Phase 1's compatibility parts (`.claude/rig.json`, `claude-handoff.md` and `docs/plan/tasks` reads, default push prefixes) move behind config or are dropped, as a major change |

README, architecture, conventions, release notes and ADRs stay in the tree
throughout. Plans, tasks, findings and history are what move into the store,
for the projects that choose to use it.

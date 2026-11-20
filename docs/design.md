# `rig`: a CLI for the loop's mechanical parts

Status: phase 1 is released as 0.1.0. Phases 2 to 6 are outlines; each is
planned separately before it is built.

`rig` is its own tool. It was built inside claude-kit under `cli/` and moved
here with its history in phase 1.5. Nothing in it imports from or reads
claude-kit; the kit uses it only through the contract below.

## Why

The kit runs a plan → fanout → review → verify → record loop. Today its state
and its rules live in prose and shell text:

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
- **rig never holds a token.** Adapters shell out to `gh` and `glab`, so they
  use whatever `gh auth` or `glab auth` the user already has.
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

Install with `go install github.com/AindriuB/rig/cmd/rig@<tag>`. claude-kit's
`install.sh` and `install.ps1` install a pinned tag, check `rig version`
against the minimum the kit needs (`RIG_MIN_VERSION`, declared once in each
installer), and carry on with a message when Go or the fetch is missing: the
kit's older scripts still work without rig.

## The contract between the kit and rig

The kit talks to rig only through its commands, their exit codes and their
`--json` output. It never imports rig's code or reads its files, and rig
never reads the kit's. That is what lets rig live in another repository and
be versioned on its own.

`rig version` prints a semantic version (`0.1.0` for phase 1) and exits 0. A
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

An unknown flag is always exit 2: rig never ignores an argument it does not
understand.

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

## The store port (phase 2)

A store holds the loop's working state outside the code tree.

```go
type Store interface {
    CreateTask(ctx context.Context, spec TaskSpec) (Task, error)
    Task(ctx context.Context, id TaskID) (Task, error)
    Tasks(ctx context.Context, filter TaskFilter) ([]Task, error)
    Append(ctx context.Context, id TaskID, ev Event) error
    Events(ctx context.Context, id TaskID) ([]Event, error)
    Findings(ctx context.Context, id TaskID) ([]Finding, error)
    Claim(ctx context.Context, c Claim) error        // phase 4
    Release(ctx context.Context, c Claim) error      // phase 4
}
```

The first backend is git refs: one ref per task, `refs/rig/tasks/<ulid>`,
holding an append-only event log. Verify and review results are also written
as git notes on the commit they judged (`refs/notes/rig`), and commits carry a
`Task: <id>` trailer. Nothing is written into the working tree, so two
developers never conflict on a plan file.

### Data model

| Entity | Fields |
|---|---|
| **Task** | id, slug, spec, acceptance criteria, `Owns` paths, branch, PR, state, attempts |
| **Finding** | source (review, verify, external), severity, route, status, dedupe key, round |
| **Event** | task id, time, actor, kind, payload. Append-only; a task's state is a fold over its events |
| **Claim** | paths, holder, expiry. A lease on files, so two tasks do not edit the same ones |

Task ids are ULIDs: they sort by creation time and two developers cannot mint
the same one. They are shown short with the slug, for example
`t-3k9f add-parser`.

### State machine

```
planned → implementing → reviewed → verified@sha → pr_open → ci_green → ready → merged
                                                                              ↘ stopped(reason)
```

- `verified@sha` records the commit that was verified. A new commit on the
  branch returns the task to `implementing`.
- `stopped(reason)` is reachable from every state. It is how a task leaves the
  loop for a human decision: a spec change, a round cap or a new flake.
- A transition is an event. No transition edits an earlier one.

## Phases

| Phase | What it delivers |
|---|---|
| 0 | This document, and a baseline of permission prompts and tokens per task |
| 1 | `rig push`, `rig pr`, `rig status`, `rig version`; the github and gitlab adapters; the kit's commands and agents switched to them |
| 1.5 | `cli/` moves to its own repository, `AindriuB/rig`, with its history; the kit installs a pinned release |
| 2 | The store port, the git-ref backend, the data model above. `rig task`, `rig finding`, `rig verify`, `rig next`, and `rig context <task>` (a budgeted brief keyed by the task's `Owns` paths) |
| 3 | Docs out of the tree. An importer for existing `docs/plan` trees. PLAN, HISTORY and the handoff become generated views; the scribe promotes into ADRs, `architecture.md` and conventions, with size budgets checked in CI |
| 4 | Team scale. `rig claim`, tiered retention and `rig gc`, a local search index rebuilt from refs, and a tracker adapter for human-facing task identity |
| 5 | Agile checks and metrics. `rig ready`, `rig done`, `rig standup`; cycle time, rounds, stop reasons and findings by area |
| 6 | An MCP server over the same core, so `push(task_id)` is a typed tool |

README, architecture, conventions, release notes and ADRs stay in the tree
throughout. Plans, tasks, designs, findings and history are what move.

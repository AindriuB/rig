# Phase 2 plan: the store, tasks, findings and context

Status: draft, waiting for approval. Nothing here is built yet.

Phase 2 moves the loop's working state out of markdown task files and into
git refs that rig owns. It adds the store port and its git-ref backend from
[design.md](design.md#the-store-port-phase-2), and five commands on top:
`rig task`, `rig finding`, `rig verify`, `rig next` and `rig context`.

It ships as **0.2.0**: every change is a new command, flag or JSON field, and
nothing in the phase-1 contract changes. Switching the kit over to these
commands is a separate change in the kit repository.

## Goals and limits

- State a model would otherwise re-read and re-parse (task state, review
  rounds, findings, what was verified at which commit) becomes typed data
  behind commands with `--json` output and exit codes.
- Rules the kit states in prose become refusals: a task cannot be marked
  reviewed with a blocking finding open, and cannot be verified at a commit
  that is not its branch head.
- Nothing is written into the working tree. Worktrees share the store,
  because refs live in the repository's common directory.
- **Not in phase 2:** sharing the store between clones (`rig sync`, phase 4),
  claims (phase 4), importing `docs/plan` trees and generated PLAN/HISTORY
  views (phase 3), git notes (see Q3). `rig push`, `rig pr` and `rig status`
  do not change.

## Storage format

One ref per task: `refs/rig/tasks/<ULID>`. The ref points at a commit whose
tree holds one file:

```
log.jsonl     one event per line, in append order
```

Each append writes a new blob with the old lines plus one, a tree, and a
commit whose parent is the previous tip. The commit message is
`<kind> <short-id>`, so `git log refs/rig/tasks/<ULID>` reads as a history.
Commits are written with a fixed identity (`rig <rig@localhost>`); who acted
is recorded in the event, not the commit.

Why one growing blob rather than a file per event: a whole task reads in one
`git cat-file --batch` request, and every task in the repository reads in one
`git cat-file` process. A task has tens of events, so rewriting the blob is
cheap, and git's delta compression keeps the history small.

An event line:

```json
{"v":1,"id":"01JD…","seq":7,"time":"2026-11-22T10:04:05Z","actor":"andrew","kind":"finding","data":{…}}
```

- `v` is the event format version. A reader skips a line whose `v` it does
  not know, and skips an event `kind` it does not know, so an older rig can
  read a store a newer rig wrote. It reports how many it skipped.
- `seq` counts from 1 within the task. `id` is a ULID, unique across writers.
  Events are ordered by `(seq, id)`.
- `actor` is `$RIG_ACTOR`, else git's `user.name`, else `unknown`.

Plumbing only, each an argument vector through `internal/proc`:
`hash-object -w --stdin`, `mktree`, `commit-tree`, `update-ref`,
`for-each-ref`, `cat-file --batch`. None of these touch the index, `HEAD` or
the working tree.

`refs/rig/*` is outside `refs/heads` and `refs/tags`, so `git push`,
`git fetch` and `git clone` ignore it by default, and `rig push` cannot name
it. `git gc` keeps everything reachable from it.

### Ids

Task, finding and event ids are ULIDs, generated in rig with the standard
library (`crypto/rand` plus the clock, monotonic within one process).

A task's **short id** is `t-` and the last four characters of its ULID in
lower case, for example `t-3k9f`. If that collides with an existing task when
the task is created, rig takes five characters, then six, and records the
short id in the `created` event. A finding's short id is `f-` and four or more
characters the same way, unique within its task.

Anywhere a command takes a task, it accepts the full ULID, the short id or the
slug. Two matches is exit 2 with the candidates listed.

A task's branch is `task/<short-id>-<slug>`, for example
`task/t-3k9f-add-parser`. `rig push t-3k9f` already resolves that through its
existing `task/<id>-*` branch lookup, so `rig push` does not change.

## Concurrent writers and merges

**Within one clone** (several agents in worktrees, the normal phase-2 case),
every write is a compare-and-swap on one ref:

1. Read the tip and its log.
2. Fold the log to the current state and check the new event is legal
   against it. If it is not, exit 1 and write nothing.
3. Write the blob, tree and commit.
4. `git update-ref <ref> <new> <old>`. For a new task, `<old>` is the empty
   string, which makes git refuse if the ref exists.
5. If the ref moved, go back to 1. Step 2 runs again on the new state, so a
   transition that was legal before another writer's event is refused if it
   no longer is.

After 8 tries with jittered backoff (10 to 200 ms) rig gives up with exit 2
and writes nothing. Every operation touches one ref, so there is no
multi-ref transaction to get wrong. Two writers creating tasks at the same
moment can pick the same short id; resolution then reports it as ambiguous
and the full ULID still works.

**Between clones** (phase 4), two logs for the same task merge as a union:
events keyed by `id`, ordered by `(seq, id)`, written as a merge commit with
both tips as parents. Folding a merged log uses the same rules as a local
one. Phase 2 implements and tests that merge function in the backend, so the
format is proven merge-safe now, but no command calls it yet (see Q2).

## Data model

Types live in `internal/store` and name no backend.

**Task** (from `rig task show --json` and each element of `rig task list --json`):

| Field | Value |
|---|---|
| `id`, `short`, `slug`, `title` | strings |
| `spec` | string, the task's goal and context |
| `acceptance` | array of `{id, text}`; ids are `AC1`, `AC2`, … and never renumbered |
| `owns` | array of path globs, git pathspec glob syntax |
| `depends` | array of task ids (full ULIDs) |
| `branch` | `task/<short>-<slug>` |
| `pr` | integer; present once a pull request is linked |
| `state` | see the state machine; the effective state, after the head check below |
| `recordedState` | the state the log alone gives |
| `verifiedSha`, `ciSha`, `mergeSha` | present once recorded |
| `headSha` | the branch tip now; absent if the branch does not exist |
| `stopReason` | present when `state` is `stopped` |
| `round` | review rounds, external reviews and failed verifies, over the task's whole life |
| `openFindings` | integer, findings with status `open` |
| `created`, `updated` | RFC 3339 times |

**Finding:**

| Field | Value |
|---|---|
| `id`, `short`, `task` | strings |
| `source` | `review`, `verify` or `external` |
| `severity` | `P1`, `P2`, `P3`, `STYLE`, `SPEC` or `NOTE`, as the kit's code-reviewer uses them |
| `route` | `implementer` or `orchestrator`; absent for a `NOTE` |
| `status` | `open`, `fixed`, `wontfix` or `deferred` |
| `key` | dedupe key |
| `round` | the task's round when it was added |
| `summary` | one line |
| `path` | `path` or `path:line`; optional |
| `created`, `updated` | RFC 3339 times |

A finding is **blocking** while it is `open` and its severity is `P1`, `P2`,
`STYLE` or `SPEC`. `P3` and `NOTE` never block.

**Event kinds:** `created`, `transition` (`{to, sha?, pr?, reason?}`),
`round` (`{kind: review|external|attempt}`), `finding` (the finding),
`finding-status` (`{finding, status}`), `verify` (`{sha, result, summary}`).

### State machine

As in the design, with the transitions rig allows:

| From | To | How | Refused (exit 1) when |
|---|---|---|---|
| `planned` | `implementing` | `rig task mark <t> implementing` | a task in `depends` is not `merged` |
| `implementing` | `reviewed` | `rig task mark <t> reviewed` | a blocking finding is open |
| `reviewed` | `verified` | `rig verify <t> --pass` | the sha is not the branch head |
| `reviewed` | `implementing` | `rig verify <t> --fail` (also adds an `attempt` round) | the sha is not the branch head |
| `verified` | `pr_open` | `rig task mark <t> pr_open --pr <n>` | — |
| `pr_open` | `ci_green` | `rig task mark <t> ci_green --sha <sha>` | the sha is not the verified sha |
| `ci_green` | `ready` | `rig task mark <t> ready` | — |
| `ready` | `merged` | `rig task mark <t> merged --sha <merge-sha>` | — |
| `reviewed` … `ready` | `implementing` | `rig task mark <t> implementing` | — |
| any but `merged` | `stopped` | `rig task stop <t> --reason <text>` | — |
| `stopped` | the state before it | `rig task resume <t>` | — |

`merged` is final. The `verified` state carries its sha, so it prints as
`verified@<sha7>`.

**The head check.** "A new commit on the branch returns the task to
implementing" is computed when the task is read, not written as an event:
if the recorded state is `verified`, `pr_open`, `ci_green` or `ready` and the
branch head is not `verifiedSha`, the effective `state` is `implementing`.
The log is unchanged, so nothing has to notice the commit at the moment it
lands (see Q4).

## Commands

All take `[--repo <dir>]` (default: the current directory) and `--json`
where they print something. As in phase 1, an unknown flag is exit 2.

**Exit codes**, the same for all five unless a row says otherwise:
**0** done; **1** refused by the state machine or a rule above, nothing
written; **2** usage error, no such task or finding, an ambiguous id, a
store or git error, or the write retries ran out.

Text output is for people and is not part of the contract; `--json` and exit
codes are.

### `rig task`

| Command | What it does | `--json` |
|---|---|---|
| `rig task new --slug <s> --title <text> [--spec <text> \| --spec-file <path>] [--ac <text>]… [--owns <glob>]… [--depends <task>]…` | Creates a task in `planned`. `--spec-file` is read once, at most 64 KiB. `--ac` repeats, numbered in order. | the Task |
| `rig task show <task>` | One task. | the Task |
| `rig task list [--state <s>]… [--all]` | Tasks, newest first. Without `--all`, `merged` tasks are left out. | array of Task |
| `rig task mark <task> <state> [--pr <n>] [--sha <sha>]` | One transition from the table. | the Task after |
| `rig task stop <task> --reason <text>` | Stops a task. | the Task after |
| `rig task resume <task>` | Returns a stopped task to its earlier state. | the Task after |
| `rig task round <task> --kind review\|external\|attempt` | Starts a new round; prints its number. | `{task, round}` |
| `rig task log <task>` | The raw events, oldest first. | array of Event |

`slug` uses `[a-z0-9-]`, at most 40 characters. `--owns` globs may not be
absolute or contain `..`.

### `rig finding`

| Command | What it does | `--json` |
|---|---|---|
| `rig finding add <task> --source <s> --severity <sev> [--route <r>] --summary <text> [--path <p[:line]>] [--key <k>]` | Records a finding in the task's current round. `--route` is required unless the severity is `NOTE`. | `{finding, duplicate}` |
| `rig finding list <task> [--status <s>]… [--blocking]` | Findings, by severity then age. Default `--status open`. | array of Finding |
| `rig finding set <task> <finding> --status <s>` | Changes a status. | the Finding after |

**Dedupe.** The key defaults to the severity class, the path without its line
number, and the summary lower-cased with whitespace collapsed. If an `open`
finding on the same task has the same key, nothing is written and the
existing finding comes back with `"duplicate": true`, exit 0. So a reviewer
that reports the same thing in round 3 as in round 2 does not inflate the
count, and the round-2 finding keeps its round.

### `rig verify`

| Command | What it does | Exit codes |
|---|---|---|
| `rig verify <task> --pass [--sha <sha>] [--summary <text>]` | Records a pass at `--sha` (default: the branch head) and moves `reviewed` to `verified`. | 0; 1 not `reviewed`, or the sha is not the head; 2 |
| `rig verify <task> --fail [--sha <sha>] --summary <text>` | Records a fail, adds an `attempt` round and a `verify` finding (`P2`, route `implementer`) from the summary, and moves the task to `implementing`. | 0; 1; 2 |
| `rig verify <task> --check` | Writes nothing. | **0** verified at the current head; **1** not; 2 |

`--json` gives `{task, sha, result, verified}`. `--check` is what a caller
uses before opening a pull request: one exit code instead of a parsed
`## Wave verified — <sha>` heading.

### `rig next`

`rig next [--json] [--limit <n>]` reads the store and the branch heads and
says what each open task needs next. It never calls the forge and never
writes.

| Effective state | `action` |
|---|---|
| `planned`, depends all merged | `implement` |
| `planned`, a dependency open | `blocked` (with `blockedBy`) |
| `implementing`, blocking findings open | `fix` |
| `implementing`, none open | `review` |
| `reviewed` | `verify` |
| `verified` | `open-pr` |
| `pr_open` | `wait-ci` |
| `ci_green` | `mark-ready` |
| `ready` | `merge` (a merge is the user's call) |
| `stopped` | `decide` (with `reason`) |

Order: `decide` and `merge` first, as they wait on a person; then by state,
furthest along first; then oldest first. `--json` is
`{actions: [{task, short, slug, state, action, reason?, blockedBy?}], skipped}`
where `skipped` counts events the reader did not understand. Exit 0, or 2 on
a usage or store error.

### `rig context <task>`

`rig context <task> [--budget <tokens>] [--json]` prints a brief for an agent
starting on the task: what it must do and what it needs to know about the
code it owns, within a fixed budget. It never prints file contents: it names
paths, and the agent reads what it needs.

**Budget.** Tokens are estimated as bytes ÷ 4, rounded up. The default is
1500; the range is 200 to 20000. Sections are filled in this order:

| # | Section | Contents | Budget rule |
|---|---|---|---|
| 1 | Header | id, slug, title, effective state, branch, PR, round | always in full |
| 2 | Spec | the spec and every acceptance criterion | always in full |
| 3 | Findings | this task's open findings: blocking first, then by severity, one line each | fills what is left |
| 4 | Owns | tracked files matching `owns` (`git ls-files` with glob pathspecs), with line counts | fills what is left |
| 5 | Overlap | other unmerged tasks whose `owns` match any of the same files | fills what is left |
| 6 | History | closed findings from other tasks whose `path` is in this task's files, newest first | fills what is left |
| 7 | Commits | the last commits on the default branch touching those files, `<sha7> <subject>` | fills what is left |

Sections 3 to 7 add items one line at a time until the next line would pass
the budget, then print `… <n> more` (its size is reserved up front) and move
to the next section. A later section can still fit a short line after an
earlier one ran out. If sections 1 and 2 alone pass the budget they print in
full, nothing else is added, and `overBudget` is true.

The same store, branch heads and tree give the same output byte for byte.

`--json` is `{task, budget, used, overBudget, sections: [{name, items, omitted}]}`;
items are strings, one per line the text form would print.

## The kit contract gains

`docs/design.md`'s contract tables gain, as additions only:

- the five commands and their exit codes, with the shared rule
  (0 done, 1 refused, 2 error) and `rig verify --check`'s 0/1;
- the Task, Finding and Event JSON fields above, and the `next` and `context`
  shapes;
- the storage promise: rig writes only under `refs/rig/`, never the working
  tree, the index or `HEAD`; readers skip unknown event kinds and versions;
- `RIG_ACTOR` names who acted.

`rig version` prints `0.2.0`. The kit can require it by raising
`RIG_MIN_VERSION`; that change and the switch of the kit's commands belong to
the kit's follow-up.

## Package layout

```
internal/store/           port: Store, Task, Finding, Event, fold, transitions, ids
internal/store/memstore/  in memory, for command tests
internal/store/gitref/    the git-ref backend
internal/git/plumbing.go  hash-object, mktree, commit-tree, update-ref, for-each-ref, cat-file
cmd/rig/store.go          opens the store; app gains a store field and a clock
cmd/rig/task.go, finding.go, verify.go, next.go, context.go
```

The port's interface is the design's, with `Claim` and `Release` left out
until phase 4 and one addition, `Update(ctx, id, func(Task) (Event, error))`,
which is how the compare-and-swap loop re-checks a transition on each retry:

```go
type Store interface {
    CreateTask(ctx context.Context, spec TaskSpec) (Task, error)
    Task(ctx context.Context, id string) (Task, error)    // full, short or slug
    Tasks(ctx context.Context, filter TaskFilter) ([]Task, error)
    Update(ctx context.Context, id string, next func(Task) (Event, error)) (Task, error)
    Events(ctx context.Context, id string) ([]Event, error)
    Findings(ctx context.Context, id string) ([]Finding, error)
}
```

`Append` from the design becomes `Update`: a bare append cannot re-check
legality against a state another writer just changed. The head check needs
git, so it runs in the commands, not the port.

## Tasks

Four waves. Tasks in a wave own disjoint files and run in parallel. Each task
ends with `go vet ./...` and `go test ./...` passing on its branch, one
code-reviewer pass with nothing at Medium (P2) or above, and a pull request
against `main` from `task/<name>`, not merged.

### Wave 1

**01 store-core** — owns `internal/store/*.go`, `internal/store/memstore/**`, `internal/store/storetest/**`.
Depends on: none.
- AC1 `Fold` turns an event list into a Task; a table test covers every row of
  the transition table, legal and refused.
- AC2 Unknown `v` and unknown `kind` are skipped and counted, not errors.
- AC3 Short ids: four characters, longer on collision; resolving a full id,
  short id or slug works, and two matches is an `ErrAmbiguous` listing both.
- AC4 Finding dedupe by default key; a duplicate returns the existing finding.
- AC5 ULIDs from one process sort in creation order (test with an injected
  clock and 10 000 ids in one millisecond).
- AC6 `Merge(a, b)` of two logs is the union by id, ordered by `(seq, id)`,
  and is commutative (test both orders).
- AC7 memstore passes the shared contract test in `internal/store/storetest`
  (owned here, run by 03 against gitref too).

**02 git-plumbing** — owns `internal/git/plumbing.go`, `internal/git/plumbing_test.go`.
Depends on: none.
- AC1 `WriteBlob`, `MakeTree`, `CommitTree`, `ReadRefs(prefix)`,
  `CatFileBatch` and `UpdateRef(ref, new, old)` work in a throwaway repo and
  pass `-C` and an argument vector only.
- AC2 `UpdateRef` with a stale `old` returns `ErrRefMoved`; with `old` empty
  and the ref present, also `ErrRefMoved`.
- AC3 No call touches the index, `HEAD` or the working tree: a test runs each
  and checks `git status --porcelain` and `HEAD` are unchanged.
- AC4 Works in a sha256 repository (`git init --object-format=sha256`).

### Wave 2

**03 gitref-backend and wiring** — owns `internal/store/gitref/**`,
`cmd/rig/store.go`, `cmd/rig/main.go`, and stub files
`cmd/rig/{task,finding,verify,next,context}.go` that later tasks replace.
Depends on: 01, 02.
- AC1 gitref passes `storetest`.
- AC2 The layout is `refs/rig/tasks/<ULID>` → commit → `log.jsonl`, one event
  per line; a test reads it back with plain `git cat-file`.
- AC3 Concurrency: 8 goroutines × 25 appends to one task end with 200 events
  and contiguous `seq`; a transition made illegal by a racing writer is
  refused, not written.
- AC4 Reading 200 tasks starts at most three git processes.
- AC5 `main.go` dispatches the five commands and the usage text lists them;
  `app` gains `store` and `now`; each stub exits 2 "not built yet".

### Wave 3

Each command task owns its file and its `_test.go`, uses memstore through
`app.store` for logic and gitref in one end-to-end test.

**04 task-command** — owns `cmd/rig/task.go`, `cmd/rig/task_test.go`. Depends on: 03.
- AC1 Every `rig task` subcommand and flag above; unknown flag is exit 2.
- AC2 `mark` follows the transition table: exit 1 and no new event on a
  refused one.
- AC3 The head check: a commit on the branch after `verified` shows
  `state: implementing` with `recordedState: verified`.
- AC4 `--json` field names match this document exactly (golden test).

**05 finding-command** — owns `cmd/rig/finding.go`, `cmd/rig/finding_test.go`. Depends on: 03.
- AC1 `add`, `list`, `set` with their flags; `--route` required unless `NOTE`.
- AC2 A duplicate is exit 0, `"duplicate": true`, no new event.
- AC3 `--blocking` lists exactly the open P1, P2, STYLE and SPEC findings.

**06 verify-command** — owns `cmd/rig/verify.go`, `cmd/rig/verify_test.go`. Depends on: 03.
- AC1 `--pass` at the head moves `reviewed` to `verified`; at any other sha,
  exit 1.
- AC2 `--fail` adds an attempt round and a finding and moves to `implementing`.
- AC3 `--check` is 0 at the verified head, 1 after a new commit, and writes
  nothing (the event count is unchanged).

**07 next-command** — owns `cmd/rig/next.go`, `cmd/rig/next_test.go`. Depends on: 03.
- AC1 Each row of the action table, from a store built in the test.
- AC2 The ordering rule, with a test of six tasks in mixed states.
- AC3 No forge call: the test's forge fails any call.

**08 context-command** — owns `cmd/rig/context.go`, `cmd/rig/context_test.go`. Depends on: 03.
- AC1 The seven sections, in order, from a repo and store built in the test.
- AC2 Output never exceeds the budget unless `overBudget` is true; tested at
  200, 1500 and 20000.
- AC3 Truncation prints `… <n> more` with the right `n`.
- AC4 Two runs give identical bytes.
- AC5 No file contents: a test puts a unique marker string in an owned file
  and checks it is absent from the output.

### Wave 4

**09 docs-and-version** — owns `docs/design.md`, `README.md`,
`cmd/rig/version.go`, `docs/phase-2.md`. Depends on: 04 to 08.
- AC1 `version` is `0.2.0`.
- AC2 The contract section gains the additions under "The kit contract
  gains"; no existing row changes (`git diff` shows only added lines in the
  contract tables).
- AC3 README lists the new commands; design.md's status line says phase 2 is
  built; this document's status says built.

## Open questions

Each has a recommendation; the plan above assumes it.

**Q1. Task short ids and branch names.** `t-` plus the ULID's last four
characters, branch `task/t-3k9f-<slug>`, so `rig push` works unchanged. The
alternative, keeping the kit's sequential `NN` numbers, collides between
developers, which is one of the reasons for phase 2.
*Recommend: the ULID short id.*

**Q2. The union merge now or in phase 4.** Nothing calls it until `rig sync`.
Building and testing it now (AC6 of task 01, about 40 lines) proves the
format can be merged before anything depends on the format.
*Recommend: build the merge function now; `rig sync` stays in phase 4.*

**Q3. Git notes and the `Task:` trailer.** The design also writes verify and
review results as notes on `refs/notes/rig`. `git notes` has no
compare-and-swap, so concurrent writers can lose a note, and everything a
note would say is already in the task's log. The trailer is written by
whoever commits, which is the kit's implementer, not rig.
*Recommend: leave notes to phase 3, as a generated view if still wanted;
the trailer goes into the kit's follow-up.*

**Q4. New commits: derived or recorded.** Derived (above) needs no hook and
cannot be missed, but costs one `rev-parse` per task read. Recording an
event would need something to notice every commit.
*Recommend: derived.*

**Q5. Forge-observed states.** `pr_open`, `ci_green` and `merged` are set by
`rig task mark`, which the kit calls after `rig pr open`, `rig pr wait` and a
merge. A `rig task sync` that asks the forge and marks them itself would
save those calls but puts forge calls into the store's commands.
*Recommend: `mark` only in phase 2; add `rig task sync` later if the kit's
switch shows the calls are a burden.*

**Q6. The store stays in one clone.** Until `rig sync` (phase 4), the state
is in this clone's refs only: a fresh clone starts empty and deleting the
clone loses it. Worktrees are fine. For one developer on one machine that is
the same as today's uncommitted task files.
*Recommend: accept for phase 2, and say so in the README.*

**Q7. `rig status`.** It keeps listing `docs/plan/tasks` files. Adding store
tasks would be a new field (`storeTasks`), a minor change, but it was not
asked for. *Recommend: leave `rig status` alone; list it as a follow-up for
the kit switch.*

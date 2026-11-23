# Phase 2 plan: generic tasks, workflows and context

Status: shipped in 0.3.0, together with [phase 2.5](phase-2.5.md). This
document has been amended to describe what shipped; the workflow file format
has its own reference in [workflows.md](workflows.md).

Phase 2 gives rig a store for work in progress and a small set of
**generic actions** on it: tasks, findings, notes, transitions, "what next"
and a context brief. rig does not know any particular loop. A **workflow
file** says what states a task moves through, which transitions exist, and
which gates guard them. A plan → fanout → review → verify → record loop
is one workflow file among others, owned by the project that runs it.

It shipped in **0.3.0** with phase 2.5 (the two phases were released together,
so there was no 0.2.0): every change is a new command, flag or JSON field, and
nothing in the phase-1 contract changes.

## Goals and limits

- **Generic actions, no built-in lifecycle.** States, transitions, gates,
  finding severities, counters and task fields are data in a workflow file.
  rig ships one small default workflow, `basic`; any team can write its own.
- **Harness-neutral.** Claude Code, other agent CLIs, CI jobs and people all
  drive rig the same way: a CLI with `--json` and exit codes, and later the
  MCP server (phase 6). Nothing assumes a harness, its hooks or its agent
  roles. Who acted comes from `RIG_ACTOR`.
- **Bring your own workflow.** A team with its own tooling and repository
  layout can adopt rig's actions without adopting anyone else's loop (see
  "Extension points").
- **Rules become refusals.** A gate the workflow declares is checked by rig on
  every transition, so a caller cannot skip it by forgetting a step.
- **A repository looks the same whether or not anyone uses rig.** rig never
  edits a tracked file (one exception in phase 2.5: a `/.rig/` line in
  `.gitignore`), never makes a commit on a branch, and never pushes its own
  refs unless asked (see "What rig leaves in a repository").
- **Not in phase 2:** sharing the store between clones (`rig sync`, phase 4),
  claims (phase 4), tracker adapters and Markdown views (phase 3, see
  [phase-3.md](phase-3.md)), HTML views and a plan importer (later,
  unscheduled), and `rig config` and `rig login`, planned in
  [phase-2.5.md](phase-2.5.md). `rig push`, `rig pr` and `rig status` do not
  change.

## What rig enforces whatever the workflow says

A workflow decides the shape of the work. It cannot loosen these:

- `rig push` pushes only a plain branch under an allowed prefix: never the
  default branch, a tag, a refspec, a force or a delete (phase 1's rules).
- The store is append-only. No command edits or deletes an event; a
  correction is a new event.
- Every write is a compare-and-swap, and gates are re-checked on each retry.
- A transition that a gate refuses writes nothing. There is no `--force`;
  the way past a gate is to change the workflow file, which is the user's.
- rig writes only under `refs/rig/` and `<git common dir>/rig/` (and, in
  phase 2.5, `~/.rig/` and one `.gitignore` line). The second is a directory
  created with mode 0700 that holds `store.lock`: rig takes that lock around
  each store write, so writers in several worktrees queue instead of racing.
  Hooks and command gates run from an argument vector, never through a shell.
- No config key and no task field holds a secret (phase 2.5).

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

### What rig leaves in a repository

A repository must not depend on rig: someone who never installs it sees the
same tree, branches and history.

| What | Where | Shared? |
|---|---|---|
| Task logs | `refs/rig/tasks/*` in the local `.git` | **Local only.** No phase-2 command pushes or fetches them. Phase 4's `rig sync` will push them only when run, or when the user opts in with a config key; never as a side effect of `rig push` (see Q21) |
| Store commits | objects reachable only from `refs/rig/*` | local, as above; on no branch, so never in a pull request |
| `<git common dir>/rig/` | a 0700 directory holding `store.lock`, taken for the length of each store write; shared by every worktree of the clone | local; inside `.git`, so never in a tree or a pull request |
| Tracked files | — | never written, except one `/.rig/` line in `.gitignore` when phase 2.5's `rig config set --local` first creates `.rig/`; never committed by rig |
| Branches | named by the workflow's `branch` template, made by whoever runs the work | rig only reads them |
| Commit trailers, git notes | — | not written by rig in phase 2 (see Q3) |
| `.claude/rig.json` | read, never written | phase 1's file, kept for compatibility (see Q28) |

Removing rig's traces from a clone is
`git for-each-ref --format='%(refname)' refs/rig | xargs -n1 git update-ref -d`,
then deleting `<git common dir>/rig/`.

### Ids

Task, finding, note and event ids are ULIDs, generated in rig with the standard
library (`crypto/rand` plus the clock, monotonic within one process).

A task's **short id** is `t-` and the last four characters of its ULID in
lower case, for example `t-3k9f`. If that collides with an existing task when
the task is created, rig takes five characters, then six, and records the
short id in the `created` event. A finding's short id is `f-` and four or more
characters the same way, unique within its task.

Anywhere a command takes a task, it accepts the full ULID, the short id or the
slug. Two matches is exit 2 with the candidates listed.

A task's branch comes from the workflow's `branch` template; the default is
`task/{short}-{slug}`, for example `task/t-3k9f-add-parser`. `rig push t-3k9f`
already resolves that through its existing `task/<id>-*` branch lookup, so
`rig push` does not change.

## Concurrent writers and merges

**Within one clone** (several agents in worktrees, the normal phase-2 case),
every write is a compare-and-swap on one ref:

1. Read the tip and its log.
2. Fold the log to the current state and check the new event is legal
   against it and the workflow's gates. If it is not, exit 1 and write nothing.
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

## Workflows

A workflow is a JSON file. It names states, transitions, gates, hooks, task
fields, finding severities and counters. rig loads one per repository: the
config key `workflow` (phase 2.5) names a built-in preset or a file path;
until phase 2.5, `RIG_WORKFLOW` does, and the default is `basic`.

### The built-in `basic` workflow

```
open → active → done         any → stopped,  stopped → (the state before)
```

No gates, no finding severities beyond `blocking` and `info`. Moving to
`stopped` takes a reason. It exists so
rig is useful with no setup, and so the tests have a workflow that is not
the review loop.

### Shape of a workflow file

An example review loop, written as a workflow, shows every feature. rig does
not ship it; rig's tests keep it as a fixture to prove the format can express
it.

```json
{
  "name": "review-loop",
  "version": 1,
  "initial": "planned",
  "branch": "task/{short}-{slug}",
  "states": {
    "planned":      { "action": "implement" },
    "implementing": { "action": "review" },
    "reviewed":     { "action": "verify" },
    "verified":     { "action": "open-pr",  "shaBound": true },
    "pr_open":      { "action": "wait-ci",  "shaBound": true },
    "ci_green":     { "action": "mark-ready", "shaBound": true },
    "ready":        { "action": "merge", "who": "human", "shaBound": true },
    "merged":       { "final": true },
    "stopped":      { "action": "decide", "who": "human" }
  },
  "onNewCommit": "implementing",
  "transitions": [
    { "from": "planned", "to": "implementing",
      "gates": [{ "dependsIn": ["merged"] }] },
    { "from": "implementing", "to": "reviewed",
      "gates": [{ "noOpenFindings": { "severity": ["P1", "P2", "STYLE", "SPEC"] } }] },
    { "from": "reviewed", "to": "verified", "takes": "sha",
      "gates": [{ "shaIsHead": true }] },
    { "from": "reviewed", "to": "implementing", "count": "rounds",
      "gates": [{ "counterBelow": { "counter": "rounds", "max": 4 } }] },
    { "from": "verified", "to": "pr_open", "takes": "pr" },
    { "from": "pr_open", "to": "ci_green", "takes": "sha",
      "gates": [{ "shaIs": "verified" }] },
    { "from": "ci_green", "to": "ready" },
    { "from": "ready", "to": "merged", "takes": "sha" },
    { "from": ["verified", "pr_open", "ci_green", "ready"], "to": "implementing" },
    { "from": "*", "to": "stopped", "takes": "reason" },
    { "from": "stopped", "to": "$previous" }
  ],
  "fields": {
    "acceptance": { "type": "list", "context": true },
    "owns":       { "type": "paths", "context": true, "owns": true },
    "depends":    { "type": "tasks" }
  },
  "findings": {
    "severities": ["P1", "P2", "P3", "STYLE", "SPEC", "NOTE"],
    "fields": { "route": { "type": "enum", "values": ["implementer", "orchestrator"] } }
  },
  "counters": ["rounds"],
  "hooks": [
    { "on": "enter:stopped", "run": ["notify-user", "{short}", "{reason}"] }
  ]
}
```

- **States** carry an `action` label for `rig next`, and `who` (`agent` by
  default, or `human`) so a caller can tell work it may do from a decision it
  must ask for. `final` states take no transitions out.
- **`shaBound` and `onNewCommit`.** A sha-bound state is tied to the sha it
  was entered at. If the task's branch head has moved since, its effective
  state is `onNewCommit`. This is computed when the task is read, not
  written (see Q4).
- **Transitions** may list several `from` states, `*` for any non-final
  state, and `$previous` as a target. `takes` names an argument the
  transition records: `sha`, `pr` or `reason`. `count` adds one to a counter.
- **Gates** are checked before the transition is written. The built-in set:

  | Gate | Passes when |
  |---|---|
  | `noOpenFindings {severity}` | no finding with one of those severities is open |
  | `shaIsHead` | the `sha` argument is the branch head |
  | `shaIs <state>` | the `sha` argument is the sha recorded entering that state |
  | `dependsIn [states]` | every task in the `depends` field is in one of those states |
  | `fieldSet <field>` | the field has a value |
  | `counterBelow {counter, max}` | the counter is below `max` |
  | `command [argv]` | a program exits 0; it gets the task as JSON on stdin and a 30 s limit |

- **Fields** are typed (`string`, `text`, `int`, `bool`, `list`, `enum`,
  `paths`, `tasks`). `context: true` puts a field in `rig context`'s fixed
  part; `owns: true` marks the paths field that `rig context` keys on.
- **Findings** take the workflow's severities and its extra fields. `NOTE`
  or any severity not named in a `noOpenFindings` gate never blocks.
- **Hooks** run after a transition is written: `on` is `enter:<state>`,
  `leave:<state>` or `transition`. The program gets the event and the task as
  JSON on stdin. A hook that fails is reported (stderr, and
  `hookErrors` in `--json`) but does not undo the transition, which is
  already recorded. Hooks and `command` gates are argument vectors with
  `{short}`, `{id}`, `{slug}`, `{state}`, `{reason}`, `{sha}` substituted as
  whole arguments, never through a shell.
- **A sha-bound state entered without a sha** inherits the sha of the sha-bound
  state it came from (`verified → pr_open` carries `verified`'s sha); from a
  state that is not sha-bound it records none.

`reviewed` is left out of the from-list of the plain transition to
`implementing` on purpose: the gated `reviewed → implementing` (which counts a
round and has a `counterBelow` gate) is its only route, and a second route
would let a caller skip the round cap.

`rig workflow validate` checks a file: every state reachable from `initial`,
every transition's states declared, gates and fields well-typed, no
transition out of a final state, and no two routes the same. After `from`
lists and `*` are expanded, validation refuses two transitions with the same
(from, to), so `"*" → X` alongside an explicit `a → X` is refused too.
[workflows.md](workflows.md) has the whole format.

**A task records the workflow's name and version when it is created.**
Gates use the workflow loaded now. A task in a state the current workflow no
longer has is reported by `rig workflow validate` and by `rig next` as
`action: unknown-state`, and can still be moved by any `*` transition.

## Extension points

How a team fits rig to its own way of working:

| Need | How |
|---|---|
| Their own states and lifecycle | a workflow file |
| Their own rules before a step | built-in gates, or a `command` gate running their program |
| Something to happen after a step | hooks |
| Extra data on tasks and findings | typed `fields` |
| Their own severities | `findings.severities` |
| Their own tracker | phase 3's tracker port ([phase-3.md](phase-3.md)): `local`, `jira`, `github`, `gitlab`, and an `exec` tracker that speaks JSON over stdin/stdout to any program |
| Their own harness | the CLI with `--json` and exit codes, phase 6's MCP server |

## Data model

Types live in `internal/store` and name no workflow.

**Task** (from `rig task show --json` and each element of `rig task list --json`):

| Field | Value |
|---|---|
| `id`, `short`, `slug`, `title` | strings |
| `body` | string, free text |
| `fields` | object; keys and types from the workflow |
| `workflow` | `{name, version}` it was created under |
| `state` | the effective state, after the sha-bound check |
| `recordedState` | the state the log alone gives |
| `stateSha` | the sha recorded entering the current state; always present, `""` when there is none. After `stopped → $previous` into a sha-bound state it is `""` while `shas` still binds that state |
| `shas` | object: state → sha recorded entering it |
| `branch` | from the workflow's template, applied when the task is read: `task new` leaves the stored branch empty because the short id is assigned by the store. Changing the template therefore renames every task's branch |
| `pr` | integer, once a transition records one |
| `headSha` | the branch tip now; absent if the branch does not exist |
| `reason` | the last `reason` argument, when the state took one |
| `counters` | object: counter → integer |
| `openFindings` | integer |
| `created`, `updated` | RFC 3339 times |

**Finding:** `id`, `short`, `task`, `severity` (from the workflow), `status`
(`open`, `fixed`, `wontfix` or `deferred`), `key`, `summary`, `path`
(`path` or `path:line`, optional), `fields` (from the workflow), `counters`
(the task's counters when it was added, so a finding knows its round),
`created`, `updated`.

**Note:** `id`, `short`, `task` (optional: a note can belong to the
repository), `kind` (free text: `decision`, `history`, …), `text`, `created`.
Notes are append-only records: what was decided, what landed. They replace
markdown history sections with something a tool can query.

Notes that belong to no task are kept on one more ref, `refs/rig/notes`,
with the same log format.

**Event kinds:** `created`, `transition` (`{from, to, sha?, pr?, reason?}`),
`field` (`{name, value}`; `title` and `body` are field names too), `count` (`{counter, by}`), `finding`,
`finding-status` (`{finding, status}`), `note`.

## Commands

All take `[--repo <dir>]` and `--json` where they print something. As in
phase 1, an unknown flag is exit 2.

**Exit codes**, shared by every command below unless a row says otherwise:
**0** done; **1** refused by a gate or the workflow, nothing written;
**2** usage error, no such task, an ambiguous id, a bad workflow file, a store
or git error, or the write retries ran out.

Text output is for people; `--json` and exit codes are the contract.

### `rig task`

| Command | What it does | `--json` |
|---|---|---|
| `rig task new --slug <s> --title <text> [--body <text> \| --body-file <path>] [--field <name>=<value>]…` | Creates a task in the workflow's `initial` state. `--body-file` is read once, at most 64 KiB. A list field repeats `--field`. | the Task |
| `rig task show <task> [--findings] [--notes] [--history]` | One task. The text form is a readable Markdown page: title, state and action, body, fields, and with the flags its findings, notes and transitions. | the Task |
| `rig task edit <task>` | Opens the task's title, body and fields as Markdown in `$VISUAL` or `$EDITOR`; on save, writes one `field` event per changed value. Needs a terminal on standard output, so `rig task edit --json \| jq` is exit 2. | the Task after |
| `rig task list [--state <s>]… [--all]` | Tasks, newest first; final states left out without `--all`. | array of Task |
| `rig task move <task> <state> [--sha <sha>] [--pr <n>] [--reason <text>]` | One transition, gates checked. | the Task after, and `hookErrors` |
| `rig task set <task> --field <name>=<value>…` | Sets fields. Exit 1, writing nothing, when a field it changes was changed by someone else since rig read it. | the Task after |
| `rig task count <task> <counter>` | Adds one to a counter outside a transition. | `{task, counter, value}` |
| `rig task is <task> <state>` | Writes nothing. | exit **0** if the effective state is `<state>`, **1** if not |
| `rig task log <task>` | Raw events, oldest first. | array of Event |

**Looking at a task while planning.** A developer reads a task with
`rig task show t-3k9f --findings --notes` (or `rig context t-3k9f` for the
budgeted brief an agent would get), browses with `rig task list` and
`rig next`, and changes the plan with `rig task edit`. The edit file is
plain Markdown: a `# <title>` line, then a `## body` heading and one
`## <field>` heading per field. A value line that starts with `#` or `\` is
written with one extra leading `\`, so only a heading rig wrote can split the
file, and the extra `\` is removed on save. An editor that saves CRLF line
endings changes nothing. Only values whose section text you changed are
written; the others are left alone. If it no longer parses, nothing is written
and rig prints where the edited copy was kept. If a field the edit changes
moved while the editor was open, rig exits 1 and writes nothing.
State is not editable there: it changes only through `rig task move`, so
gates still apply.

`rig task is <t> verified` is what a review loop's "was this verified at the
current head?" becomes: sha-bound states fall back on a new commit, so one
exit code answers it.

### `rig finding`

| Command | What it does | `--json` |
|---|---|---|
| `rig finding add <task> --severity <sev> --summary <text> [--path <p[:line]>] [--key <k>] [--field <name>=<value>]…` | Records a finding. | `{finding, duplicate}` |
| `rig finding list <task> [--status <s>]… [--blocking]` | Default `--status open`. `--blocking` keeps those a `noOpenFindings` gate names. | array of Finding |
| `rig finding set <task> <finding> --status <s>` | Changes a status. | the Finding after |

**Dedupe.** The key defaults to the severity, the path without its line
number, and the summary lower-cased with whitespace collapsed. If an `open`
finding on the same task has the same key, nothing is written and the
existing finding comes back with `"duplicate": true`, exit 0.

### `rig note`

| Command | What it does | `--json` |
|---|---|---|
| `rig note add [<task>] --kind <k> --text <text>` | Appends a note to a task, or to the repository. | the Note |
| `rig note list [<task>] [--kind <k>] [--limit <n>]` | Newest first. | array of Note |

### `rig workflow`

| Command | What it does |
|---|---|
| `rig workflow show [--json]` | The workflow in effect, and where it came from. |
| `rig workflow validate [<file>] [--json]` | Checks a file, or the one in effect, and any task in a state it lacks. 0 valid, 1 invalid, 2 error. |

### `rig next`

`rig next [--who agent|human] [--limit <n>] [--json]` reads the store, the
workflow and the branch heads and lists what each open task needs. It never
calls the forge and never writes.

For each task: its effective state's `action` and `who`, the transitions out
of that state, and for each whether its gates pass now
(`{to, ok, failing: [gate], gates: [{gate, ok, message?}]}`). `ok` is `true`
when every gate passes, `false` when one fails, and `null` when none fails but
a `command` gate was not run: `rig next` never runs a program, so a command
gate's own `ok` is `null` ("not run"). A task whose every transition fails a
gate is listed with the failing gates, so a caller sees *why* it is stuck.
A `$previous` target is resolved as a move resolves it, to the task's previous
state; a task with no previous state gets `ok: false`, failing `previous`.

Order: `who: human` first, as they wait on a person; then states nearer a
final state first (by shortest path); then oldest first.

`--json`: `{items: [{task, short, slug, state, action, who, transitions}], skipped}`.

### `rig context <task>`

`rig context <task> [--budget <tokens>] [--json]` prints a brief for whoever
picks up the task, person or agent, within a fixed budget. It names paths
and never prints file contents.

**Budget.** Tokens are estimated as bytes ÷ 4, rounded up. Default 1500;
range 200 to 20000. Sections are filled in this order:

| # | Section | Contents | Budget rule |
|---|---|---|---|
| 1 | Header | id, slug, title, effective state, action, branch, PR, counters | always in full |
| 2 | Body | the body and every field marked `context: true` | always in full |
| 3 | Findings | this task's open findings, blocking first, one line each | fills what is left |
| 4 | Paths | tracked files matching the `owns: true` field, with line counts | fills what is left |
| 5 | Overlap | other open tasks whose owned paths match any of the same files | fills what is left |
| 6 | History | closed findings and notes from other tasks on these files, newest first | fills what is left |
| 7 | Commits | the last commits on the default branch touching the owned paths, matched by the owns globs, so commits that touched a path since deleted or renamed are included | fills what is left |

A workflow with no `owns` field gets sections 1 to 3 only. Sections 3 to 7
add one line at a time until the next would pass the budget, then print
`… <n> more` (its size reserved up front) and move on. If sections 1 and 2
alone pass the budget, they print in full and `overBudget` is true. The same
inputs give the same bytes.

`--json`: `{task, budget, used, overBudget, sections: [{name, items, omitted}]}`.

## The contract gains

`docs/design.md`'s contract gains, as additions only: the commands above
with the shared exit codes; the Task, Finding, Note, Event, `next` and
`context` shapes; the workflow file format at `version: 1`; the storage
promise (only `refs/rig/` and `<git common dir>/rig/`, append-only, unknown
events skipped); `RIG_ACTOR` and `RIG_WORKFLOW`. `rig version` prints
`0.3.0`, the two phases having shipped together.

A workflow file's format is versioned on its own. A new optional key is
compatible for the new rig, but 0.3.0 reads a workflow file strictly and
rejects a key it does not know, so a file that uses a new key cannot be
loaded by 0.3.0. Phase 3's `tracker` key is the first case (see
[phase-3.md](phase-3.md)). Anything that changes the meaning of an existing key
raises the workflow format version, which rig reads alongside the old one.

## Package layout

```
internal/store/           port: Store, Task, Finding, Note, Event, fold, ids, merge
internal/store/memstore/  in memory, for tests
internal/store/storetest/ the contract test every backend passes
internal/store/gitref/    the git-ref backend
internal/workflow/        file format, validation, gates, hooks, the basic preset
internal/git/plumbing.go  hash-object, mktree, commit-tree, update-ref, for-each-ref, cat-file
cmd/rig/store.go          opens the store and workflow; app gains store, workflow and a clock
cmd/rig/task.go, finding.go, note.go, workflow.go, next.go, context.go
```

The port, without `Claim` and `Release` until phase 4:

```go
type Store interface {
    CreateTask(ctx context.Context, t NewTask) (Task, error)
    Task(ctx context.Context, ref string) (Task, error)    // full, short or slug
    Tasks(ctx context.Context, filter TaskFilter) ([]Task, error)
    Update(ctx context.Context, ref string, next func(Task, []Finding) ([]Event, error)) (Task, error)
    Events(ctx context.Context, ref string) ([]Event, error)
    Findings(ctx context.Context, ref string) ([]Finding, error)
    Notes(ctx context.Context, filter NoteFilter) ([]Note, error)
}
```

`Update` is the compare-and-swap loop: the commands pass a function that
asks the workflow whether the step is allowed on the state as it is now, so
a gate is re-checked on every retry. The store knows no workflow; the
workflow package knows no git.

## Open questions

Each has a recommendation; the design above assumes it.

**Q1. Task short ids.** *Recommend: `t-` plus four or more ULID characters,
growing on collision.*

**Q2. The union merge now.** *Recommend: build and test `Merge` now so the
format is proven mergeable; `rig sync` stays in phase 4.*

**Q3. Git notes and commit trailers.** *Recommend: rig writes neither in
phase 2. A workflow that wants a `Task:` trailer asks its committer for one;
a hook can check it.*

**Q4. Sha-bound states: derived or recorded.** *Recommend: derived when
read, as above; nothing has to watch for commits.*

**Q5. Forge-observed transitions.** Moving to `pr_open` or `merged` is a
`rig task move` the caller makes after `rig pr`. *Recommend: no forge calls
in the store commands in phase 2; a hook or a later `rig task sync` can
automate it.*

**Q6. The store stays in one clone.** *Recommend: accept for phase 2 and say
so in the README.*

**Q7. `rig status`.** *Recommend: unchanged in phase 2 (see Q28).*

**Q21. Sharing the store.** *Recommend: opt-in only, via an explicit
`rig sync` or a config key, in phase 4.*

**Q22. Where a review loop lives.** *Recommend: in the repository of the
project that runs it, as a workflow file. rig ships only `basic`, plus an
example loop as a test fixture proving the format can express it. rig
carries no loop preset to keep in step.*

**Q23. Gates: a fixed set plus `command`.** A fixed set keeps common rules
fast and checkable by `rig next` without running programs; `command` covers
the rest. An embedded expression language would be more to secure and
document. *Recommend: the seven built-ins above, nothing more.*

**Q24. Trusting a workflow file.** A workflow can run programs. *Recommend:
rig loads a workflow only from a preset name or a path the user's own
config or `RIG_WORKFLOW` names; it never discovers one inside a repository,
so cloning a repository never runs its programs.*

**Q25. Hooks after the write.** *Recommend: hooks run after the event is
recorded and cannot undo it; a step that must be able to refuse is a
`command` gate.*

**Q26. No `--force`.** *Recommend: no override flag. Changing the workflow
file is the escape, and it is the user's file.*

**Q27. Findings and notes as two primitives.** *Recommend: both. A finding
has a status and can block; a note is an append-only record of a decision or
an outcome. Merging them would give every note a status nobody uses.*

**Q28. Phase 1's compatibility parts.** `rig status` reads
`claude-handoff.md` and `docs/plan/tasks`; `rig push` resolves a task id from
`docs/plan/tasks`; `.claude/rig.json` sits under a Claude directory; the
default push prefixes are `task` and `record`. Changing any of them is a
major change. *Recommend: keep them through 0.x. Phase 2.5 adds the generic
homes (`~/.rig/config.json`, `push.prefixes`); phase 3 gives `rig status`
a store-based view (its last 0.4.0 task); 1.0 drops the compatibility reads.*

**Q29. `rig verify` and a review loop's names.** *Recommend: no `rig verify`, no
"round" or "review" in rig. Verifying is a transition with a `shaIsHead`
gate, a round is a counter, and the check is `rig task is <t> verified`.*

**Q30. Reading and editing a task by hand.** *Recommend: `rig task show`
prints Markdown for people, with `--findings`, `--notes` and `--history`;
`rig task edit` opens title, body and fields in the user's editor. A
Markdown rendering of one task or the task list is phase 3's `--format
markdown`; an export of the whole plan and HTML views are later,
unscheduled.*

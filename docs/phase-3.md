# Phase 3 plan: views and trackers

Status: planned, not built. It is cut as two releases: **0.4.0** (formats,
parent and external links, branch stamping, intake and refresh, a store-based
`rig status`) and **0.5.0** (`rig tracker report`). This document is the plan
the tasks in "Tasks" are built from. Where it names code by `path:line`, the
line is where the code was when the plan was written, at `42c7ee1`.

Phase 3 lets rig take its planning from an issue tracker and show its tasks
as Markdown. [phase-2.md](phase-2.md) gave rig a store and generic actions;
[phase-2.5.md](phase-2.5.md) gave it config and logins, and settled that
`tracker.kind` can be `local`, `jira`, `github` or `gitlab`. Until now nothing
read a tracker. Phase 3 does, and adds `exec` for any other.

## Goals and limits

Four decisions from the user shape everything below:

- **Local first.** The store stays the record rig works from. `rig next`,
  `rig context` and every `rig task` command work with no network and no
  tracker, and a tracker being down never blocks a transition.
- **The tracker owns the "what", rig owns the execution.** The tracker is the
  source of the planning fields: title, description, acceptance criteria,
  priority, assignee, labels. rig owns state, findings, notes, counters,
  shas, branch and pull request.
- **Linked tasks, not two-way sync.** A task is *linked* to one tracker issue.
  Refresh copies the planning fields from the issue into the task. Nothing
  copies rig's state back unless someone runs `rig tracker report`. Split
  ownership of fields replaces any merge between two writers.
- **A task can start from an issue in a chosen state.** `rig task new --from
  github:42 --state design` creates a task in `design` rather than the
  workflow's `initial` state, when the workflow allows it.

Goals that follow:

- **Hierarchy is the tracker's.** A task can have a parent task, and a linked
  task carries the tracker's own level label (`epic`, `story`, `Sub-task`).
  rig has no fixed levels and no built-in hierarchy of its own.
- **Reports are explicit.** A tracker is told about a task's progress only
  when someone runs `rig tracker report`, or a workflow hook does. There is no
  automatic mirror of every transition.
- **Markdown is a stable format.** `--format markdown` on `task show`,
  `task list` and `context` is for people and for agents that read prose. Its
  layout is versioned in the contract the way `--json` is.
- **Everything is additive.** An older rig keeps reading a store a newer rig
  wrote (see "Compatibility with 0.3.0"), with one exception, the workflow
  file's `tracker` key, which is documented below.

Limits:

- A task links to **one** issue. An issue is linked from **one** task per
  (kind, host, key).
- Tracker text is untrusted input and is treated as such (see "Security and
  trust").
- Tracker adapters are read-mostly. Phase 3 writes to a tracker only through
  `rig tracker report`: a status change and a comment, nothing else.

## What is out of scope

| Not in phase 3 | Where it goes |
|---|---|
| HTML views and templates | later, unscheduled |
| An importer for `docs/plan` task files | later, unscheduled; tracker intake covers most of its use |
| `rig sync`, claims, retention, a search index | phase 4 |
| Metrics from event logs | phase 5 |
| An MCP server | phase 6 |
| Two-way sync, writing planning fields to a tracker, creating issues from tasks | not planned |
| Dropping phase 1's compatibility reads | 1.0, decided after 0.5.0 |

The numbering of the other phases does not change. The phase-4 row of
[design.md](design.md) loses "tracker adapters", which are here.

The store-based `rig status` that [phase-2.md](phase-2.md) Q28 promised is in
0.4.0, as its last task. It is the precondition for dropping the
`claude-handoff.md` and `docs/plan` reads in 1.0. Whether 1.0 follows 0.5.0 is
decided once 0.5.0 is out.

## Store and data model

All changes are additions. The event format version `v` stays 1.

### New event kinds

| Kind | Data | Meaning |
|---|---|---|
| `parent` | `{task}` | The task's parent: a task ULID, or `""` to clear. The last one wins. |
| `external` | `{kind, host, project, key, url, type, owned[], parentKey}` | Links the task to an issue. `owned` lists the rig fields the tracker owns. `type` is the tracker's own level label. `parentKey` is the issue's parent when that parent has no task yet. |
| `tracker-sync` | `{at, rev, values{field: value}}` | A snapshot of the owned fields as the tracker last gave them, and the tracker's revision. It is what drift is measured against. |
| `tracker-report` | `{kind, key, status?, comment?, at}` | What `rig tracker report` sent. It makes report idempotent (0.5.0). |

`created` data gains the optional `parent`, `external` and `snapshot`, so
intake creates a linked task in one write rather than create-then-link. A
reader folds them the same whichever event carries them.

### Task and TaskView

`Task` gains `Parent` (a ULID, `omitempty`) and `External`:

```
external: {kind, host, project, key, url, type, owned[], syncedAt, rev}
```

The `--json` task gains `parent`, `external` and `external.drift`. Drift is
computed offline: the names of owned fields whose current local value differs
from the last snapshot. JSON only gains fields, so gates, hooks and scripts
that read the task keep working; a workflow gate or hook can read the new
fields from the task they are given.

Children are never stored. `--children` and `--tree` find them by scanning
tasks for a matching parent. A task's depth is the length of its parent chain.
`external.type` is the tracker's label, copied as text; rig does not order
levels.

### Parent rules

- The parent must exist. A cycle is refused (exit 1, nothing written).
- A local parent change is refused while the task is linked and `parent` is in
  its `owned` set: the tracker decides, and the next refresh moves it.

### Links and uniqueness

- `(kind, host, key)` is unique across tasks. `CreateTask` checks it under the
  store lock (`internal/store/gitref/gitref.go:317-335`). `NewTask` gains
  `External`, `Parent` and `BranchTemplate`. When the key is already linked,
  `CreateTask` returns the existing task and says so.
- Linking an *existing* task (`rig task link`) checks uniqueness before it
  writes, but two writers on two tasks are not atomic across tasks. The
  window is small and the doc for the command says so.
- `Resolve` (`internal/store/ids.go:28-55`) also matches `external.key`, after
  a full ULID, a short id and a slug. A key that matches more than one task is
  the existing ambiguous-id exit 2. To verify during implementation: a bare
  GitHub key such as `42` can clash with a numeric slug; decide whether
  `Resolve` should refuse numeric keys without their `kind:` prefix.

### Refresh writes plain field events

Refresh writes ordinary `field` events for the owned fields, as well as a
`tracker-sync` snapshot. A 0.3.0 rig reading the store therefore sees the
current title and body without knowing about trackers. An edit made in 0.3.0
shows as drift the next time 0.4.0 refreshes.

### Branch stamping

Today a task's branch is derived whenever it is read, from the workflow's
`branch` template, so changing the template renames every branch
([phase-2.md](phase-2.md) data model). Phase 3 stamps it for new tasks:
`Options.Created` applies a template given in `NewTask` (`{short}`, `{slug}`,
`{id}`, and the new `{key}`) and stores the result in `CreatedData.Branch`.
Existing tasks stay derived. `{key}` is the issue key lowercased with every
character outside `[a-z0-9-]` replaced by `-`. A tracker key in a branch name
is the reason for doing this: the branch must not move if the issue is later
renamed or the template edited.

## Workflow file: the `tracker` key

An optional top-level `tracker` key in the workflow file says how issues map
to tasks. The workflow file is global-only (phase 2.5, Q10), so this mapping
is chosen by the user and never by a cloned repository.

```json
"tracker": {
  "intake": { "states": ["design", "ready"] },
  "map": {
    "title": "title", "body": "description", "acceptance": "acceptance",
    "priority": "priority", "assignee": "assignee",
    "labels": "labels", "type": "type"
  },
  "report": { "status": { "review": "In Review", "done": "Done" } }
}
```

(The workflow file is JSON; this is the fragment that goes in it. The exact
spelling is fixed when the key is added to [workflows.md](workflows.md).)

- `intake.states`: the states a task may be created in from an issue. Default
  is the workflow's `initial` state alone. `--state` outside the list is
  refused (exit 1).
- `map`: a rig field to a neutral attribute: `title`, `description`,
  `acceptance`, `priority`, `assignee`, `labels`, `type`. The mapped fields
  are the **owned set**. The default is `title` and `body` only.
- `report.status`: a rig state to the tracker's status *name*. The adapter
  looks up the transition that reaches that name. A state with no entry has
  nothing to report.

**Validation**, in `rig workflow validate`: every named state is declared;
intake states are not final states; a `map` key is a declared field, `title`
or `body`; a mapped field is not `owns: true` (an owned path glob is not a
planning field); and the declared value type fits the attribute (a list field
for `labels` and `acceptance`).

There is no automatic report. A workflow hook may run
`rig tracker report {short}` after a transition, which is how a team that
wants a mirror builds one.

**0.3.0 cannot load a workflow file that uses `tracker`.** It reads the file
with unknown keys disallowed (`internal/workflow/workflow.go:265`) and exits 2
naming the key. A team that shares one global workflow file across machines
must update rig everywhere before adding the key. Phase 2's statement that a
new optional key is compatible was wrong for readers older than the key and is
corrected in [phase-2.md](phase-2.md).

## Tracker port and adapters

### The port

A new package `internal/tracker` mirrors `internal/forge`.

```go
type Tracker interface {
    Capabilities(ctx context.Context) (Caps, error)
    Issue(ctx context.Context, key string) (Issue, error)
    Search(ctx context.Context, q Query) ([]IssueRef, error)
    Children(ctx context.Context, key string) ([]IssueRef, error)
    Comment(ctx context.Context, key string, c Comment) error
    SetStatus(ctx context.Context, key, status string) (StatusResult, error)
}
```

`Issue` has `key`, `url`, `title`, `description`, `acceptance` (a list),
`priority`, `assignee`, `labels`, `type`, `status`, a parent reference and
`rev`, the tracker's own change marker. `Caps` is `{Parent, Children, Types,
Search, Comment, SetStatus, Levels}`.

A capability a tracker lacks returns `ErrUnsupported`. A command turns that
into a `warnings[]` entry and carries on; it is not an error. `ErrNotFound`
and `ErrAuth` are exit 2. `internal/tracker/fake` and
`internal/tracker/trackertest` give a fake and the contract test every adapter
passes, as `memstore` and `storetest` do for the store.

`cmd/rig/tracker.go` gains `selectTracker(ctx, dir)`, following `selectForge`
(`cmd/rig/forge.go:50-61`). `tracker.kind = local` means no tracker: every
`rig tracker` command exits 2 saying so.

### Adapters

| Adapter | How it talks | Notes |
|---|---|---|
| `jira` | `net/http`; Jira Cloud REST v3 with Basic auth, or Jira Server/Data Center REST v2 with a personal access token over Bearer | Token from the same keychain entry `rig login jira` writes; `RIG_JIRA_TOKEN` overrides. |
| `github` | `gh` through `internal/proc`, argv only | Sub-issues and types by `gh api graphql` with `-F` variables. |
| `gitlab` | `glab api` through `internal/proc`, argv only | Work items and issues. |
| `exec` | one program per operation, JSON on stdin and stdout | For any tracker rig has no adapter for. |

**Jira.** The host check and the account lookup that `rig login` already has
move into `internal/auth/jira`, so the login and the adapter share one rule
for which token goes to which host. One adapter speaks both Jira Cloud (REST
API v3) and Jira Server/Data Center (REST API v2) in 0.4.0 (Q42). It has two
small dialects behind one set of calls, and both pass the same `trackertest`
contract against recorded responses:

- *Text format.* Cloud descriptions are Atlassian Document Format JSON and
  Server/DC descriptions are wiki markup. Each converts to Markdown for
  reading, and report comments are written in the dialect's own format from
  Markdown.
- *Search.* Cloud uses `/rest/api/3/search/jql` with its token-based paging;
  Server/DC uses `/rest/api/2/search` with `startAt` paging.
- *Hierarchy.* Cloud uses the standard `parent` field for epics and sub-tasks.
  Server/DC uses the "Epic Link" custom field for epics and `parent` for
  sub-tasks. The Epic Link field id is found once by the field name through the
  fields endpoint, and `tracker.jira.epicField` overrides it.

`tracker.jira.api` (`auto`, `v2` or `v3`; default `auto`) picks the dialect.
`auto` reads `/rest/api/2/serverInfo` once, takes `deploymentType`, and caches
the result: Cloud is v3, Server/DC is v2.

Auth differs by deployment. Cloud keeps email plus API token over Basic.
Server/DC uses a personal access token over `Bearer`, stored by
`rig login jira --pat` under the same `jira:<host>` account (and the
repo-scoped one) with no user. This changes phase-2.5's `rig login jira`: it
gains `--pat`, and its token check uses `GET /rest/api/2/myself` for v2 rather
than only `/rest/api/3/myself`. The trust rules are the same for both. The adapter sends the token only to the
trusted `tracker.url` host, over https (plain http only for a loopback
address), and refuses a redirect to another host itself. To verify during
implementation: Go's `net/http` drops the `Authorization` header on a
cross-host redirect, which would make the explicit refusal redundant; keep it
anyway. Jira has no standard acceptance-criteria field (to verify): acceptance
maps from a field id given under `tracker.map`, and is unmapped without one.
Jira also changes status by transition (to verify): `SetStatus` first looks up
the transition to the named status and returns an error naming the available
ones when there is none.

**GitHub and GitLab.** Calls follow `internal/forge/github`'s pattern
(`github.go:33-35`): an argument vector through `proc.Run`, never a shell, no
interpolated query text. Issue types are null on repositories owned by a user
rather than an organisation, and GitLab epics are `ErrUnsupported` on the Free
tier; both become warnings. The host comes from `forge.KindForHost` or
`tracker.url`. To verify during implementation: that the `gh api graphql`
sub-issue calls and the `glab api` work-item calls behave as this plan
describes; the plan author could not run either.

**exec.** The program is the argument vector in the new global-only list key
`tracker.command`. rig starts it once per operation. It writes one JSON line
to the program's stdin:

```json
{"v":1,"op":"issue","key":"ABC-1"}
```

where `op` is `caps`, `issue`, `search`, `children`, `comment` or `status`,
with the operation's own arguments beside it. The program answers with one
JSON object on stdout:

```json
{"v":1,"ok":true,"issue":{"key":"ABC-1","title":"…"}}
{"v":1,"ok":false,"error":"no such issue","code":"not-found"}
```

`code` is `not-found`, `unsupported` or `auth`; an `ok:false` with no code is
a plain error. A non-zero exit, output that is not one JSON object, or no
answer in 30 seconds is an error carrying the tail of stderr. The program gets
only that operation's data, and a scrubbed environment (see "Security and
trust"). `tracker.kind = exec` with no trusted `tracker.command` is exit 2.

### Config changes

`internal/config/keys.go:133-135` accepts `exec` as a `tracker.kind`.
`tracker.command` is a new list key, global-only like `push.prefixes` and
`workflow` (`keys.go:45`). The lookup order for the tracker keys is today
local, repo, `.claude/rig.json`, global (`internal/config/layers.go:140-150`);
`tracker.command` follows the workflow key's order (`layers.go:143-148`),
reading the global file only.

Two keys are new for Jira. `tracker.jira.api` is `auto`, `v2` or `v3`, default
`auto`. `tracker.jira.epicField` is the Server/DC Epic Link field id, unset by
default so that it is discovered by name. Both follow the lookup order of the
other tracker keys. Neither carries a URL or a command, so a tracked
`.rig/config.json` may set them.

## Commands

All take `--repo <dir>` and `--json` where they print something, and the exit
codes of [phase-2.md](phase-2.md): **0** done; **1** refused, nothing written;
**2** usage or other error. Where a command can stop partway, it says so.

### Intake and refresh

| Command | What it does | `--json` |
|---|---|---|
| `rig task new --from <kind>:<key>\|<key> [--state <s>] [--slug <s>] [--field <name>=<value>]…` | Creates a linked task from an issue. A thin wrapper over the intake function `rig tracker pull` also uses. The slug is made from the key and title, at most 40 characters, unless `--slug` is given. If the key is already linked, returns the existing task, exit 0, with a line on stderr. `--state` outside the workflow's intake states is exit 1. | the Task |
| `rig tracker pull <key> \| --query <q> \| --label <l> [--children[=<depth>]] [--limit <n>] [--state <s>]` | Creates tasks from one issue, a search or a label, and with `--children` the issue's descendants to `<depth>`. A parent already in the store is linked; otherwise `external.parentKey` is kept and resolved at the next refresh. Parents are never created for you. | `{items: [{task, key, existing, parent, warnings[]}], skipped}` |
| `rig tracker refresh [<task> \| --all] [--dry-run]` | Re-reads the issue and brings the owned fields up to date. The tracker wins. Writes `field` events and one `tracker-sync`. A deleted issue is `gone` and writes nothing. | `{tasks: [{task, key, status, changed[], replaced[], error?}]}` |
| `rig tracker show <key>` | Reads one issue and prints it. Writes nothing. | the Issue |
| `rig tracker status` | Tracker kind, whether the config is trusted, the adapter's capabilities and whether login works. Writes nothing. | `{kind, trusted, capabilities, login}` |

`refresh` `status` is `unchanged`, `updated`, `gone` or `error`. `changed`
lists the owned fields whose value changed; `replaced` lists those where the
local value had been edited since the last snapshot and was overwritten, with
the value that was lost. There is no `--force`: the tracker owns the field, so
a refresh always applies (Q31). `refresh` continues past a failed task and
exits 2 at the end if any failed. `--dry-run` computes and prints the same
result and writes nothing.

`pull` exits 1 for a disallowed `--state`, before reading anything. A
capability the tracker lacks, such as children, is a warning on the item.

### Hierarchy and links

| Command | What it does | `--json` |
|---|---|---|
| `rig task parent <task> <parent\|none>` | Sets or clears the parent. | the Task after |
| `rig task link <task> <ref>` | Links an existing task to an issue, then does one refresh. | the Task after |
| `rig task unlink <task>` | Removes the link; the fields keep their last values and become the task's own. | the Task after |
| `rig task list --parent <t> \| --roots \| --tree` | Children of a task, tasks with no parent, or the whole forest. | array of Task |
| `rig task show <task> --children` | Adds the task's children. | the Task |

`task set` and `task edit` on a linked task refuse a change to an owned field
(exit 1). The check is repeated inside `Update`, so a refresh racing an edit
cannot slip through. The message points to the tracker or to `rig task unlink`.

### Report (0.5.0)

| Command | What it does | `--json` |
|---|---|---|
| `rig tracker report <task> [--status] [--comment pr\|state\|summary] [--include findings\|notes]… [--dry-run]` | Sends the task's progress to its issue: a status change from `report.status`, and a comment. Writes a `tracker-report` event, and a repeat of the same report is skipped. Never writes planning fields. | `{task, key, status: {from, to}\|null, comment: {kind, id}\|null, skipped?}` |

Exit 1 when the task's state has no `report.status` entry or the tracker has
no such transition; nothing is sent. `--dry-run` prints exactly what would be
sent. `--comment pr` is the pull request URL; `state` is the new state and the
short id; `summary` adds the counters. Findings and notes go in a comment only
with `--include`.

### Store-based `rig status`

`rig status` gains a view built from the store (open tasks, their effective
states and open findings) and keeps its phase-1 reads for projects that have
not moved. Its exact output is fixed in that task; it must stay fast and make
no network call.

## Markdown format

`--format text|markdown|json` is added to `rig task show`, `rig task list` and
`rig context`. `--json` stays as an alias of `--format json`, `--markdown` is
an alias of `--format markdown`, and naming two different formats or an
unknown one is exit 2. `cmd/rig/args.go` has no alias support, so the flag
handling is one helper in `cmd/rig/format.go`.

Text stays free-form, as it is today. **Markdown is stable like JSON:** fields
and sections are only added; removing or reordering one is a major change.

Rules that hold everywhere: UTF-8, LF line endings, one trailing newline, no
ANSI codes, no line wrapping, times in RFC 3339 UTC. A field with no value is
left out. A single-line value has its newlines collapsed to a space.

### `task show`

`# <title>`, then a bullet block with these labels, in this order, each only
when it has a value: `id`, `slug`, `state`, `recorded state`, `branch`, `pr`,
`head`, `reason`, `parent`, `external`, `created`, `updated`. Then H2
sections in this order: `Body`, `Fields`, `Counters`, `Findings`
(with `--findings`), `Notes` (`--notes`), `History` (`--history`),
`Children` (`--children`). A requested section with nothing in it prints
`none`.

`Body` is printed verbatim, except that an ATX heading outside a code fence is
demoted to level 4 or deeper, so a task body cannot add sections of its own.
`Fields` and `Counters` are `- name: value` lines sorted by name. A finding is
`- <short> [<severity>] <status>: <summary> (<path>)`.

### `task list`

`# Tasks`, then one bullet per task, newest first:
`- <short> [<state>] <title> (<slug>)`, followed by ` — <key>` for a linked
task and ` ← <parent short>` for a task with a parent. No tasks prints `none`.
`--tree` indents children under their parent instead of using `←`.

### `context`

H2 sections named as the JSON `sections[].name`: `Header`, `Body`,
`Findings`, `Paths`, `Overlap`, `History`, `Commits`. A section the budget hid
is left out. `Body` is verbatim as above; the others are `- ` bullets. An
omitted count is the line `_… n more_`. `Header` gains `parent:` and
`external:` rows. In Markdown, the Markdown's own bytes are charged against the
budget; text and JSON accounting do not change. There is no `Hierarchy`
section in 0.4.0 (Q36).

## Security and trust

**The tracker trust rule moves into `internal/config`.** `tracker.url` decides
where a token may be sent, and a cloned repository can carry a committed
`.rig/config.json`. Today the rule (a tracked `.rig/config.json` may not supply
`tracker.url`) is enforced only by `rig login`
(`cmd/rig/login.go:274-300`, `rigConfigCommitted`, and `319-330`). Every
command that contacts a tracker must apply it, so it becomes one shared check
in `internal/config`, called by every `rig tracker` command, `rig task new
--from` and `rig task link`. A tracked file may still give `tracker.kind` and
`tracker.project`, never `tracker.url` or `tracker.command`.

**`tracker.command` is a program rig runs, so it is global-only**: read from
`~/.rig/config.json` and never from a project, refused by `rig config set` in
every scope, and edited by hand. Cloning a repository cannot make rig start a
program.

**Tokens.** A Jira token, an API token or a personal access token alike, goes
only to the trusted `tracker.url` host, over https (plain http for loopback
only), and a redirect to another host is refused. `internal/proc` (`proc.go:36,76`) lets `Options.Env` only *add* to the
parent's environment, so an `exec` adapter would inherit `RIG_JIRA_TOKEN`.
`proc` gains an environment scrub that removes `RIG_*_TOKEN` variables, used
for the `exec` adapter and for `gh` and `glab`.

**What leaves the machine.** Intake sends a key or a query. Report sends a
status name and a comment built from a fixed template: the pull request URL,
the state, the short id and counters. Findings, notes, the body and fields are
sent only with `--include` or an explicit `--text`. `--dry-run` shows exactly
what would be sent. An `exec` program receives only the data of the operation
it was asked for.

**Tracker text is untrusted.** Titles, descriptions and comments reach agents
through `rig task show` and `rig context`, so a hostile issue can try to
instruct an agent. rig does not filter it. It marks the source: the
`external:` row says which fields the tracker supplied, and the Markdown body
heading rule above stops tracker text from adding sections.

## Compatibility with 0.3.0

| Change | What 0.3.0 does with it |
|---|---|
| New event kinds | Skips and counts them (`internal/store/fold.go:37`); appending keeps the skipped lines (`gitref.go:110-113`). |
| New fields in `created` | Ignores them. A task created by intake shows up in 0.3.0 without its parent or link. |
| `field` events written by refresh | Read as ordinary edits; 0.3.0 sees current titles and bodies. |
| New JSON fields | Not present in 0.3.0 output; scripts that read only old fields are unaffected. |
| A workflow file with a `tracker` key | **Not loadable.** 0.3.0 exits 2 on the unknown key. |
| A task created by 0.3.0 | A 0.4.0 rig reads it as unlinked and parentless. |

Only the reader behaviour of 0.3.0 was checked in the code; it was not run. To
verify during implementation: write a store with the new events using the new
code and read it with a 0.3.0 binary.

## Open questions

Each has a recommendation; the design above assumes it. Q numbers continue from
[phase-2.md](phase-2.md).

**Q31. Drift on refresh. Decided.** The tracker owns the field, so refresh
overwrites a locally edited owned field and lists it under `replaced`. No
`--force`, because there is nothing to force: unlink gives the field back.

**Q32. Tracker mappings. Decided.** They live in the workflow file's optional
`tracker` key, which is global-only, not in a new config key. 0.3.0 cannot load
a file using it, and the docs say so.

**Q33. Phase numbering and cut. Decided.** Phase 3 is views and trackers;
phases 4 to 6 and 1.0 keep their numbers; "tracker adapters" leaves the phase-4
row.

**Q34. Releases. Decided.** 0.4.0 is formats, parent and external links,
branch stamping, intake and refresh, `tracker show` and `status`, `task
parent`, `link` and `unlink`, and the store-based `rig status`. 0.5.0 is
`rig tracker report`.

**Q35. HTML, templates, importer, 1.0. Decided.** HTML and templates, and the
`docs/plan` Markdown importer, are later and unscheduled. Whether 1.0 drops the
compatibility reads is decided after 0.5.0.

**Q36. Hierarchy in context.** *Recommend: no `Hierarchy` section in 0.4.0;
the `parent:` and `external:` header rows are enough, and a section can be
added later.*

**Q37. Jira acceptance criteria.** *Recommend: a configurable field id under
the workflow's `tracker.map`; acceptance is unmapped without one.* Jira has no
standard field for it (to verify).

**Q38. One tracker per repository.** *Recommend: one by default, from
`tracker.kind`; `external.kind` is stored per task so a second tracker can be
added later without a migration.*

**Q39. `task new --from` and `tracker pull`.** *Recommend: keep both, one
implementation: the one-task form is the shorthand people reach for, the
many-task form is for a query or an epic.*

**Q40. `task link` in 0.4.0.** *Recommend: yes. Without it, tasks made before
a tracker was adopted cannot join it, and the cost is a small command over
intake's own code.*

**Q41. Report in 0.5.0, not 0.4.0.** *Recommend: yes. It is the only command
that writes to a tracker, so it gets its own release and its own review of what
leaves the machine.*

**Q42. Jira API versions. Decided.** The Jira adapter ships with both REST API
v3 (Jira Cloud) and v2 (Jira Server/Data Center) in 0.4.0, as one adapter with
two dialects chosen by `tracker.jira.api`, default `auto`. Server/DC logs in
with a personal access token (`rig login jira --pat`).

## Tasks

The plan is built as waves of tasks. Tasks in one wave own disjoint files and
can run in parallel; each later wave starts when the earlier one has merged.
Every task also keeps `go vet`, `gofmt` and the package tests green.

### Wave 0: foundations

| Task | Owns | Delivers | Done when |
|---|---|---|---|
| 3.1 store | `internal/store/**` (memstore, storetest, gitref) | The new event kinds, fold, `Parent` and `External`, `Resolve` by key, `NewTask` fields, uniqueness under the lock, branch stamping, `{key}` | `storetest` passes on both backends with linked, parented and branch-stamped tasks; an old log still folds; a repeated key returns the existing task |
| 3.2 config | `internal/config/**`, one call in `cmd/rig/login.go` | The `exec` kind, global-only `tracker.command`, the `tracker.jira.api` and `tracker.jira.epicField` keys, the shared trust check; `rig login` uses it | A tracked `.rig/config.json` cannot supply `tracker.url` or `tracker.command` through any path; `rig config set tracker.command` is refused in every scope |
| 3.3 workflow | `internal/workflow/**` (after 3.1) | The `tracker` key, its validation, the `TaskView` fields | The validation cases in "Workflow file" each have a failing and a passing test; `rig workflow validate` reports them |

3.1 and 3.2 are independent. 3.3 needs the `TaskView` types 3.1 adds.

### Wave 1: in parallel, after wave 0

| Task | Owns | Delivers | Done when |
|---|---|---|---|
| 3.4 views | `cmd/rig/format.go`, `cmd/rig/render_md.go`, and the show, list and context code in `cmd/rig/task.go` and `cmd/rig/context.go` | `--format`, the aliases, the Markdown renderings | Golden tests for each section and for empty cases; two formats or an unknown one is exit 2; body headings are demoted outside code fences; context Markdown stays within budget |
| 3.5 port | `internal/tracker/{tracker.go,fake,trackertest}` | The port, errors, the fake and the contract test | The fake passes `trackertest`; `ErrUnsupported` is distinguishable from `ErrNotFound` and `ErrAuth` |
| 3.6 proc | `internal/proc/**` | The environment scrub | A child started with the scrub does not see `RIG_JIRA_TOKEN` or any `RIG_*_TOKEN`; existing callers are unchanged |

3.4 owns `cmd/rig/task.go` in this wave. Nothing else edits it until 3.4 has
merged.

### Wave 1b: adapters, after 3.5 and 3.6

| Task | Owns | Delivers | Done when |
|---|---|---|---|
| 3.7a exec | `internal/tracker/exec` | The exec protocol | Passes `trackertest` against a local test program; a timeout and bad JSON are errors |
| 3.7b jira | `internal/tracker/jira`, `internal/auth/jira`, `cmd/rig/login.go` (after 3.2) | The Jira adapter with both dialects; the account lookup moves out of `cmd/rig/login.go`; `rig login jira --pat` and the v2 `myself` check | Both dialects pass `trackertest` against recorded responses; `auto` picks the dialect from `serverInfo` and caches it; a cross-host redirect is refused; a PAT is sent only to the trusted host |
| 3.8 github | `internal/tracker/github` | The GitHub adapter | `trackertest` passes against a fake `gh`; no interpolated query text |
| 3.9 gitlab | `internal/tracker/gitlab` | The GitLab adapter | `trackertest` passes against a fake `glab`; an unsupported epic is a warning |

3.7b owns `cmd/rig/login.go` once 3.2 has merged: it moves the lookup helper
and adds the `--pat` login path and the v2 `myself` check. It runs after 3.2.
3.7a and 3.7b are independent of each other.

### Wave 2: commands, 0.4.0

| Task | Owns | Delivers | Done when |
|---|---|---|---|
| 3.10 tracker commands | `cmd/rig/tracker.go` | `selectTracker`, `pull`, `refresh`, `show`, `status` | JSON shapes as above; refresh `replaced` and `gone` covered; a failed task does not stop the rest and exit is 2 |
| 3.11 task commands | `cmd/rig/task.go` (after 3.4), `cmd/rig/task_link.go` | `task new --from`, `task parent`, `link`, `unlink`, `list` and `show` hierarchy flags, the owned-field refusal | A cycle is refused; a linked task refuses an owned-field edit even when a refresh races it; `--from` of a linked key returns the existing task |
| 3.12 status | `cmd/rig/status.go` | The store-based `rig status` view | Makes no network call; works with no `claude-handoff.md` and no `docs/plan`; the phase-1 output is unchanged where those exist |

3.10 and 3.11 share the intake function. 3.10 owns it and 3.11 starts after it
has merged, or the function goes in a file of its own that 3.10 owns. 3.12 is
last, because it needs the finished task view. **0.4.0 is cut after wave 2.**

### Wave 3: 0.5.0

| Task | Owns | Delivers | Done when |
|---|---|---|---|
| 3.13 report | `cmd/rig/tracker_report.go`, the `tracker-report` event in `internal/store/**` | `rig tracker report` | A repeat is skipped; exit 1 when the state has no mapping or the tracker has no such transition, with nothing sent; `--dry-run` matches what a real run sends |

### Docs, last

| Task | Owns | Delivers |
|---|---|---|
| 3.14 docs | `docs/workflows.md`, `README.md`, `docs/design.md` contract section, `docs/release.md` | The `tracker` key and the 0.3.0 incompatibility, the new commands and the Markdown format in the contract, the release notes for each of 0.4.0 and 0.5.0 |

Docs for 0.4.0 are written before 0.4.0 is cut, and for 0.5.0 before 0.5.0.
This plan itself changes only `docs/`.

### To verify during implementation

These are the plan's guesses, collected. None is a fact until a task has
checked it.

- Go's `net/http` drops `Authorization` on a cross-host redirect (refuse
  explicitly regardless).
- Jira Cloud has no standard acceptance-criteria field.
- `gh api graphql` sub-issue calls and `glab api` work-item calls behave as
  described.
- A Jira status change needs a transition lookup first.
- The exact Jira Cloud `search/jql` paging (token-based), and that
  `/rest/api/2/serverInfo` reports `deploymentType` as assumed.
- How much of Atlassian Document Format a usable Markdown conversion must
  cover, and the same for wiki markup in the other direction.
- That personal access tokens exist on Server/DC (8.14 and later), and what
  older instances can use instead.
- The Epic Link field name on localized Server/DC instances, where the
  by-name lookup may fail and `tracker.jira.epicField` is needed.
- A store written with the new events reads cleanly in a 0.3.0 binary.
- The code not read when this plan was written: the body of `status.go`
  beyond the handoff constant, `finding.go`, `note.go`, the other `config`
  Set and Unset paths, workflow validation and gates beyond the lines cited,
  `memstore` and `storetest`, and the tests. About 14 test files call
  `CreateTask` and may need the new `NewTask` fields.

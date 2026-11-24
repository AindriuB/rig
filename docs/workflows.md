# Writing a workflow

A **workflow** tells rig what states a task moves through, which moves are
allowed, and what must be true before each move. rig has no lifecycle of its
own: the rules live in a JSON file you choose. This page is the reference for
that file. [phase-2.md](phase-2.md) explains why the store and workflows
exist; this page explains how to write one.

## Choosing the workflow rig uses

rig loads one workflow per command, from the first of these that is set:

1. `RIG_WORKFLOW`, a preset name or an absolute path to a file.
2. The `workflow` config key (a preset name or an absolute path). It is read
   only from the global file, `~/.rig/config.json`, and `rig config set`
   refuses it: you edit the file by hand.
3. The built-in preset `basic`.

A relative path is refused. rig never looks for a workflow inside a
repository, so cloning a repository never makes rig run its programs.

`rig workflow show` prints the workflow in effect and where it came from.
`rig workflow validate [<file>]` checks a file, or the one in effect, and
reports any task that is in a state the workflow does not have. It exits 0 when
valid, 1 when invalid and 2 on an error.

## The `basic` preset

```text
open -> active -> done        any -> stopped        stopped -> (the state before)
```

States are `open` (action `start`), `active` (action `finish`), `done` (final)
and `stopped` (action `decide`, for a human). Moving to `stopped` needs a
reason. There are no gates. Findings have two severities, `blocking` and
`info`. Use `rig workflow show --json` to see the exact file.

## The file

A workflow is one JSON object. rig refuses a key it does not know, so a typo is
an error and not a silent no-op.

| Key | Meaning |
|---|---|
| `name`, `version` | A label and a positive integer. A task records both when it is created. |
| `initial` | The state a new task starts in. |
| `branch` | Branch name template; `{short}`, `{slug}`, `{id}` and `{key}` are replaced. Optional. A task made from an issue (`rig task new --from`, `rig tracker pull`) has its branch stamped at creation, with `{key}` as the issue's key, lowercased, with every character outside `a-z`, `0-9` and `-` turned into `-`; a later change to the template or the issue does not rename it. Other tasks derive the branch from the template whenever it is read, and there `{key}` is the task's short id, even after `rig task link`, so linking never renames a branch. |
| `states` | An object: state name to `{action, who, final, shaBound}`, all optional. |
| `onNewCommit` | The state a sha-bound task counts as in when its branch has moved on. Needed if any state is sha-bound. |
| `transitions` | The allowed moves, in order (see below). |
| `fields` | Typed fields on tasks. |
| `findings` | `{severities: [...], fields: {...}}`. |
| `counters` | Names of counters a task can carry. |
| `hooks` | Programs to run after a move. |
| `tracker` | How issues from a tracker become tasks. Optional. See "Linking tasks to a tracker": 0.3.0 cannot load a file that has this key. |

### States

- `action` is a label that `rig next` prints for a task in this state, such as
  `review` or `merge`. rig attaches no meaning to it.
- `who` is `agent` (the default) or `human`. `rig next --who human` lists the
  tasks waiting on a person.
- `final: true` means no transition leaves the state, and `rig task list`
  hides tasks in it unless you pass `--all`. A final state cannot be sha-bound.
- `shaBound: true` ties a task to the commit it was moved in at. When the
  task's branch head is no longer that commit, the task's effective state is
  `onNewCommit`. This is worked out when the task is read; nothing is written.
  `onNewCommit` itself must be neither final nor sha-bound.

State names may not be empty, `*`, or start with `$`.

### Transitions

```json
{ "from": ["a", "b"], "to": "c", "takes": "sha", "count": "rounds", "gates": [ ... ] }
```

- `from` is a state, a list of states, or `*` for every non-final state other
  than the target. `to` is a state, or `$previous`.
- `$previous` means the state the task was in before its latest move. A task
  that has never moved has none, and the move is refused. `rig next` shows
  such a move as blocked by `previous`.
- `takes` names an argument the move records: `sha` (`--sha`), `pr` (`--pr`) or
  `reason` (`--reason`). A move that takes one refuses to run without it.
- `count` adds one to the named counter when the move is written.
- A move into a sha-bound state that takes no sha inherits the sha of the
  sha-bound state it came from, so `verified -> pr_open` carries `verified`'s
  sha. Otherwise it records none and the state keeps the sha it was last
  entered at.
- `gates` are checked in order before anything is written. Every gate is
  evaluated and the move happens only if all pass. A refusal is exit 1 and
  writes nothing. There is no `--force`: to get past a gate, change the file.

**Routes must be unique.** After `from` lists and `*` are expanded, no two
transitions may take the same state to the same target, and one transition may
not name a route twice. So `{"from": "*", "to": "X"}` beside
`{"from": "a", "to": "X"}` is refused, because `*` already covers `a`.
`rig workflow validate` reports each clash as
`route a -> X is also taken by transitions[N]`. To give one state different
rules from the rest, list the other states explicitly instead of using `*`.

Validation also requires that every transition names declared states, that no
transition leaves a final state, and that every state can be reached from
`initial`. Each problem is reported with its JSON path, for example
`transitions[3].gates[0].shaIsHead`.

### Gates

A gate is an object with one key, the gate's name.

| Gate | Passes when |
|---|---|
| `{"noOpenFindings": {"severity": ["P1", "P2"]}}` | no finding with one of those severities is `open`. Each severity must be declared in `findings.severities`. |
| `{"shaIsHead": true}` | the `sha` the move was given is the branch head. The move must take `sha`. |
| `{"shaIs": "<state>"}` | the `sha` the move was given is the one recorded when the task entered `<state>`. The move must take `sha`. |
| `{"dependsIn": ["merged"]}` | every task named in the `depends` field is in one of those states. The workflow must declare a `depends` field of type `tasks`. A dependency in a sha-bound state is judged by its effective state. |
| `{"fieldSet": "<field>"}` | the field has a value. `title` and `body` count as fields. |
| `{"counterBelow": {"counter": "rounds", "max": 4}}` | the counter is below `max`. `max` is at least 1. |
| `{"command": ["program", "arg", "{short}"]}` | the program exits 0. |

A `command` gate gets the task as JSON on standard input and has 30 seconds.
`rig next` never runs one: it reports the move's `ok` as `null` and the gate
as `"ok": null`, meaning "not run". Only `rig task move` runs them.

### Fields, severities and counters

Task fields have a type: `string`, `text`, `int`, `bool`, `list`, `enum`
(with `values`), `paths` (file globs) or `tasks` (task references). `title`
and `body` are built in and cannot be declared. Two flags change how a field
is used:

- `context: true` prints the field in the fixed part of `rig context`.
- `owns: true` marks the one `paths` field `rig context` uses to find the
  task's files. At most one task field may carry it.

`findings.fields` are the extra fields a finding takes, such as a `route`
enum. They cannot carry `owns`. A finding's severity must be one of
`findings.severities`; `NOTE` or any severity no `noOpenFindings` gate names
never blocks anything.

### Hooks

```json
{ "on": "enter:stopped", "run": ["notify-user", "{short}", "{reason}"] }
```

`on` is `enter:<state>`, `leave:<state>` or `transition`. Hooks run, in file
order, after a move has been written. A hook gets `{event, task}` as JSON on
standard input and has 30 seconds. A hook that fails is reported on standard
error, and in `hookErrors` with `--json`, but it never undoes the move.
Something that must be able to refuse a move is a `command` gate, not a hook.

### Arguments and placeholders

`command` gates and hooks are argument vectors. rig runs the program directly
and never through a shell. An argument that is exactly `{short}`, `{id}`,
`{slug}`, `{state}`, `{reason}` or `{sha}` is replaced by that value, and the
value stays one argument whatever it contains, spaces and `;` included. A
placeholder inside longer text, such as `--to={state}`, is refused by
validation. In a hook `{state}` is the state entered; in a gate it is the state
the move leaves.

## Linking tasks to a tracker

A **tracker** is an issue tracker: Jira, GitHub issues, GitLab issues, or any
other that you reach through a program of your own (`exec`). A task can be
*linked* to one issue. The tracker then owns the task's planning fields, such
as its title and description, and rig owns everything about the work itself:
state, findings, notes, counters, shas, branch and pull request. Nothing is
sent back to the tracker in 0.4.0. `rig tracker pull`, `rig task new --from`
and `rig task link` are the commands that use this; see the README for the
list.

### The `tracker` key

The optional top-level `tracker` key says how an issue maps to a task. Like the
rest of the workflow file, it comes from the global file, so a cloned
repository cannot change it.

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

- `intake.states` lists the states a task may be created in from an issue. The
  default is the workflow's `initial` state alone. `--state` outside the list
  is refused (exit 1). A listed state must be declared and must not be final.
- `map` goes from a rig field to one attribute of the issue. The attributes
  are `title`, `description`, `acceptance`, `priority`, `assignee`, `labels`
  and `type`. The mapped fields are the **owned fields**: the tracker supplies
  them, and `rig task set` and `rig task edit` refuse to change them on a
  linked task (exit 1). A key of `map` is a field the workflow declares, or
  `title`, or `body`. An owned field is not `owns: true`. `labels` and
  `acceptance` need a `list` field; every other attribute needs a `string` or
  `text` field. Each attribute is mapped from at most one field. With no `map`,
  the owned fields are `title` and `body`.
- `report.status` goes from a rig state to the name of a tracker status. rig
  checks that the states are declared and the names are not empty. Nothing in
  0.4.0 sends a status; `rig tracker report` is planned for 0.5.0.

`rig workflow validate` reports each broken rule with its JSON path, for
example `tracker.map.priority`.

**0.3.0 cannot load a workflow file that has a `tracker` key.** It rejects the
unknown key and exits 2. If one global file is shared between machines, update
rig on all of them before you add the key. Stores are not affected: 0.3.0
still reads a store that 0.4.0 wrote, and sees the current title and body of a
linked task; it skips the events it does not know and shows a task made from an
issue without its link or parent.

### Where the tracker is

These config keys (set with `rig config`) say which tracker and where:

| Key | Meaning |
|---|---|
| `tracker.kind` | `local` (no tracker, the default), `jira`, `github`, `gitlab` or `exec`. |
| `tracker.url` | The tracker's address, `https://` only. Jira needs it; GitHub and GitLab need it only for a self-hosted server. |
| `tracker.project` | The Jira project key, or `owner/name` (`group/.../name` on GitLab). For GitHub and GitLab the default is the origin remote's path. |
| `tracker.command` | For `exec`: the program and its arguments, a list. Global file only. |
| `tracker.jira.api` | `auto` (default), `v2` (Server or Data Center) or `v3` (Cloud). |
| `tracker.jira.epicField` | The Server or Data Center "Epic Link" field id. Found by name when unset. |
| `tracker.jira.acceptanceField` | The id of the Jira field that holds acceptance criteria. Without it, an issue has none. |

**Trust.** `tracker.url` decides where a login token is sent, so a repository
cannot supply it. If `.rig/config.json` is tracked by git (or is a symlink, or
rig cannot tell), rig ignores a `tracker.url` in it and the tracker commands
stop with an error; `rig tracker status` still exits 0 and reports
`trusted: false`. Set it in the global file or an untracked local file.
`tracker.command` is a program rig would run, so it is read only from the
global file.

**GitHub and GitLab hosts.** For `github` and `gitlab` rig takes the host from
`tracker.url`. With no `tracker.url`, it takes the host from the `origin`
remote, but only if that host is `github.com` (for GitHub), `gitlab.com` (for
GitLab), or the host you named in `forge.host`. Any other host is refused, and
the message says `set tracker.url to the tracker's address`. This is on
purpose: a guessed host could read or link an issue on the wrong server.

### Linking, refreshing, unlinking

A task is unique by its link: one issue is linked from one task. The link is
identified by the tracker kind, the host, the key and, for GitHub and GitLab,
the project. Keys match whatever their case. Creating a task from an issue
that is already linked (`rig task new --from`, `rig tracker pull`) returns the
task that has it; `rig task link` on an issue another task has is refused
(exit 1).

A worked example, with a GitHub repository:

```text
rig config set tracker.kind github --repo        # use this repository's issues
rig tracker pull 42                               # make a task from issue 42
rig tracker pull --label ready --children        # make tasks from a label, and their sub-issues
rig tracker refresh --all                         # re-read every linked issue
rig tracker refresh --all --dry-run               # show what a refresh would change
rig task link 07 43                               # link the existing task 07 to issue 43
rig task unlink 07                                # drop the link; the fields stay
```

`pull` and `task new --from` copy the owned fields from the issue into a new
task. `refresh` copies them again. The tracker wins: a refresh overwrites an
owned field you edited locally, and lists it under `replaced` in `--json` with
the value that was lost. A deleted issue is reported as `gone` and nothing is
written. `task unlink` leaves the fields as they are; from then on they are the
task's own.

## Worked example: a review loop

An example review workflow: plan, implement, review, verify, pull request,
merge. It is one workflow among others. The file below is the fixture rig
tests against (`internal/workflow/testdata/review-loop.json`); a project that
uses it keeps its own copy.

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

Reading it through:

- **Starting.** A task begins in `planned`. It cannot move to `implementing`
  until every task in its `depends` field is `merged`.
- **Review.** `implementing -> reviewed` is refused while any P1, P2, STYLE or
  SPEC finding is open. P3 and NOTE findings never block.
- **Verifying.** `reviewed -> verified` takes the sha that was reviewed and
  needs it to be the branch head. `verified`, `pr_open`, `ci_green` and
  `ready` are sha-bound, so a new commit on the branch drops the task back to
  `implementing` with no one having to notice. `rig task is <t> verified`
  answers "was this verified at the current head?" with one exit code.
- **Rounds.** `reviewed -> implementing` is the only way back from `reviewed`.
  It counts a round and is refused once `rounds` reaches 4. The from-list of
  the plain `-> implementing` transition therefore leaves `reviewed` out: if it
  were there, the same route would exist twice, and validation would refuse the
  file, and the round cap could be skipped.
- **Pull request.** `verified -> pr_open` records the pull request number and
  carries `verified`'s sha. `pr_open -> ci_green` needs the sha it is given to
  be the one recorded at `verified`.
- **Merge.** `ready` is for a human. `ready -> merged` is final.
- **Stopping.** From any state but `merged`, a task can be stopped with a
  reason, which runs the `notify-user` hook. `stopped -> $previous` returns it
  to the state it left.

## When the workflow changes under a task

A task records the workflow's name and version when it is created, but gates
use the workflow loaded now. A task in a state the current workflow lacks is
reported by `rig workflow validate`, and by `rig next` with the action
`unknown-state`. A `*` transition can still move it.

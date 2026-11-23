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
| `branch` | Branch name template; `{short}`, `{slug}` and `{id}` are replaced. Optional. |
| `states` | An object: state name to `{action, who, final, shaBound}`, all optional. |
| `onNewCommit` | The state a sha-bound task counts as in when its branch has moved on. Needed if any state is sha-bound. |
| `transitions` | The allowed moves, in order (see below). |
| `fields` | Typed fields on tasks. |
| `findings` | `{severities: [...], fields: {...}}`. |
| `counters` | Names of counters a task can carry. |
| `hooks` | Programs to run after a move. |

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

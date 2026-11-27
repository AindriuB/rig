# Hooks

A **hook** is a program of your own that rig runs after it writes an event. Use
one to ping a chat, write back to a pull request or tracker, start a preview
environment, or start the next agent. A hook runs after the write is made, so
it can never refuse or undo it. Something that must be able to refuse a move is
a `command` gate (see [workflows.md](workflows.md)).

Hooks are part of the workflow file, which comes only from your own config,
never from a repository. This page is the full reference. It describes rig
0.7.0.

## Configuring a hook

```json
"hooks": [
  { "name": "chat-stopped", "on": "enter:stopped",
    "run": ["notify-chat", "{short}", "{reason}"] },
  { "name": "preview", "on": ["enter:pr_open", "final"],
    "env": ["GH_TOKEN"], "timeout": "20s", "run": ["preview-env", "{id}", "{state}"] }
]
```

Hooks run in file order. `rig workflow validate` checks every key below.

| Key | Values | Meaning |
|---|---|---|
| `name` | Lower-case letters, digits and `-`, starting with a letter or digit, at most 40 characters, unique in the workflow. | Names the hook in `RIG_ACTOR`, `RIG_HOOK_NAME`, messages and the `cause` of what it writes. Optional: a hook without one is called `hook-<n>`, where `n` is its position counting from 1, and rig prints a warning. |
| `on` | One selector, or a list of selectors. Required. | Which events fire the hook. Any selector in a list matching is enough. |
| `run` | A non-empty argument vector. Required. | The program and its arguments. rig never uses a shell. An argument that is exactly `{short}`, `{id}`, `{slug}`, `{state}`, `{reason}` or `{sha}` is replaced by its value. |
| `mode` | `inline` (the default) or `queued`. | `inline` runs the hook after the write, and rig waits. `queued` is accepted but **not acted on in 0.7.0**: the hook loads with a warning ("not delivered by this version") and never runs. Queued delivery is planned for 0.8.0. A queued hook must have a `name`. |
| `timeout` | A Go duration above zero, such as `10s` or `1m`. | How long the hook may run. The default is 30 seconds, which is also the longest for an inline hook. |
| `env` | A list of variable names, or `PREFIX*` entries such as `GH_*`. | Variables of rig's own environment the hook may also see. See [The environment](#the-environment). `*` alone and empty entries are refused. |
| `from` | `start` or `now`. | Where a queued hook starts. Accepted but **not acted on in 0.7.0**. |
| `maxDepth` | An integer, 1 or more. | A depth limit for queued hooks. Accepted but **not acted on in 0.7.0**. |

0.7.0 reads every key a later version will use, so agents in different
worktrees can run different rig versions against one config without a
validation problem stopping every move.

### Selectors

| `on` | Fires when |
|---|---|
| `transition` | A `transition` event is written: a task moved. |
| `enter:<state>` | A transition whose target is `<state>`. The state must be declared. |
| `leave:<state>` | A transition whose source is `<state>`. The state must be declared. |
| `final` | A transition into a state marked `"final": true` (`merged` in the kit-loop workflow). |
| `kind:<kind>` | An event of that kind. `<kind>` is one of `created`, `transition`, `field`, `count`, `finding`, `finding-status`, `note`, `parent`, `external`, `tracker-sync`, `tracker-report`, `tracker-push`. |
| `kind:*` | Every event of every kind, bookkeeping kinds such as `count` and `field` included. |

`on` may be a list, such as `["enter:pr_open", "final"]`. One hook then sees
its setup and its teardown events, in the order they were written.

`stopped` is not final, so a teardown hook that must also run when a task is
stopped lists both: `["final", "enter:stopped"]`.

`{state}` is the task's state after the event, for every kind. `{reason}` and
`{sha}` mean something only for a `transition`; a hook that uses one with a
selector that can fire for another kind is refused when the workflow loads.

### When hooks run

Every event written by a single-task command fires its own hooks. `rig task
move` writes a `transition` and then a `count` when the transition counts
something, and each fires the hooks that match it. A hook for a note is fired
by `rig note add`, one for a finding by `rig finding add` and `set`, and so on.
Hooks run after the store lock is released, so a hook may itself call `rig`.

A hook that fails (non-zero exit, timeout, not found) is reported on standard
error and, for `rig task move --json`, in `hookErrors`. Later hooks still run,
the write stands, and the command's exit code does not change.

## What a hook receives

Standard input is one JSON object:

```json
{"format": 1,
 "event": {"v": 1, "id": "01J...", "seq": 7, "time": "...", "actor": "ann", "kind": "transition", "data": {"from": "reviewed", "to": "verified"}},
 "task": {"id": "...", "short": "07", "slug": "auth", "state": "verified", "...": "..."},
 "effective": "verified"}
```

- `format` is the payload's own version, separate from the event's `v`.
- `event` is the stored event: `{v, id, seq, time, actor, kind, data}`, plus
  `cause` when a hook made the write.
- `task` is the task as recorded right after that event. It is not the task
  now: a delayed `enter:pr_open` reports `pr_open`, not `merged`. It has the
  same fields as `rig task show --json`.
- `effective` is the task's state after branch-head rules, which only the
  live command can know. It is in the payload of inline hooks only, and
  absent from `rig events`.
- For a repository note that belongs to no task (`kind:note`), `task` is
  `null` and `effective` is absent.

### The contract, and what may change

- **New event kinds are additive.** A selector never matches a kind it does
  not name, except `kind:*`. A consumer of `kind:*` must ignore kinds it does
  not know.
- **New event fields are additive**, as `cause` was. Readers ignore fields
  they do not know.
- **Changing the shape of an existing kind's `data` bumps the event's `v`.**
  `format` changes only when the payload's own shape does.

## The environment

A hook gets an allowlisted environment, not rig's. These variables pass when
set (names match case-insensitively on Windows):

| Group | Variables |
|---|---|
| System | `PATH`, `HOME`, `USER`, `LOGNAME`, `SHELL`, `TMPDIR`, `TZ`, `LANG`, `LC_*`, `TERM` |
| Windows | `SystemRoot`, `SystemDrive`, `windir`, `ComSpec`, `PATHEXT`, `TEMP`, `TMP`, `USERPROFILE`, `USERNAME`, `HOMEDRIVE`, `HOMEPATH`, `APPDATA`, `LOCALAPPDATA`, `ProgramData`, `ProgramFiles*`, `COMPUTERNAME`, `PROCESSOR_ARCHITECTURE`, `NUMBER_OF_PROCESSORS` |
| Network | `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`, in both upper and lower case; `SSL_CERT_FILE`, `SSL_CERT_DIR` |
| Desktop session | `XDG_*`, `DBUS_SESSION_BUS_ADDRESS` (the Linux secret store and the gh keyring need them) |
| Tool config | `GH_CONFIG_DIR`, `GLAB_CONFIG_DIR`, `GIT_CONFIG_GLOBAL`, `GIT_SSH_COMMAND`, `GIT_ASKPASS` |
| rig | `RIG_WORKFLOW` |

Nothing else passes: not `GH_TOKEN`, `RIG_*_TOKEN`, cloud credentials or
`SSH_AUTH_SOCK`. `gh`, `glab` and `git` still find their stored logins through
their config directories. A hook that needs another variable lists it:

```json
{ "name": "pr-comment", "on": "enter:pr_open", "env": ["GH_TOKEN"], "run": ["pr-comment", "{short}"] }
```

An `env` entry is an exact name, or a prefix ending in `*` (`"GH_*"`). It
lets that variable through only if rig has it. It cannot invent a value. Keep
webhook URLs and secrets in the hook program's own config, never in
placeholders.

`command` gates and `exec` tracker programs keep their inherited environment.
Only hooks are allowlisted.

rig then sets these for every hook:

| Variable | Value |
|---|---|
| `RIG_ACTOR` | `hook:<name>`. Anything the hook writes with `rig` is recorded under this actor, unless it passes `--actor`. |
| `RIG_HOOK_NAME` | The hook's name. |
| `RIG_HOOK_EVENT` | The id of the event being handled. |
| `RIG_HOOK_TASK` | The id of that event's task. Unset for a repository note. |

### Time limit

A hook has its `timeout`, 30 seconds by default and at most 30 seconds. At the
limit rig sends the hook's process group SIGTERM (Unix), waits 5 seconds, then
kills it. On Windows the hook is killed at once. Handle SIGTERM to clean up.
A `rig` command the hook started that is killed by SIGTERM releases the store
lock before it exits, so a timed-out write-back does not hold up other writers.

## Writing back without looping

A hook that calls `rig` writes events, and those events fire hooks. rig stops
this from running away.

A write made while `RIG_HOOK_EVENT` is set records a `cause` on its event:
`{"event": <id>, "hook": <name>, "depth": <n>}`. The depth is read from the
log, not from the environment: it is the depth recorded on the cause event plus
1, or 1 when that event has no `cause`. A cause event that cannot be found (a
leaked variable, an event from another clone) counts as depth 1 and is still
recorded. Because the chain is on the log, it is bounded across processes.

A write at depth 3 or more still happens, but runs **no inline hooks**, and rig
prints `hook depth 3 reached: no hooks run for event <id>`. So a hook that
writes can cause at most two further rounds of hooks.

### Long work

An agent or a build that a hook starts and that outlives the hook has two
problems:

1. It would inherit `RIG_HOOK_EVENT`, so every write it makes would count as
   caused by the hook. After three rounds its writes would stop firing hooks.
2. The hook's process group is killed when the hook ends or times out, so
   anything left in it dies.

Start such work with `rig hooks start`:

```
rig hooks start [--json] [--] <cmd> [args...]
```

It starts `<cmd>` with no `RIG_HOOK_*` variables, detached in its own session
(Unix) or outside the hook's job (Windows), with no standard input or output,
and prints its process id (`{"pid": n}` with `--json`) and returns at once. rig
flags before `<cmd>` belong to rig. Everything after `<cmd>` belongs to the
program. It starts the program in the repository folder. On Windows, if the
hook's job forbids breakaway, rig warns that the child will not outlive the job.

If you start work some other way, clear `RIG_HOOK_*` and detach it yourself
(`setsid`, closing its pipes).

## Recipes

Each recipe is one `hooks` entry. The programs named are yours.

**Ping chat when a task stops or is ready.**

```json
{ "name": "chat", "on": ["enter:stopped", "enter:ready"], "run": ["notify-chat", "{short}", "{state}", "{reason}"] }
```

`{reason}` is empty for a transition that has none. A slow chat call delays
the command, so keep `timeout` short: `"timeout": "5s"`.

**Write back to a pull request or the tracker.** The hook runs under its own
name, so the audit trail shows who wrote what.

```json
{ "name": "pr-label", "on": "enter:stopped", "env": ["GH_TOKEN"], "timeout": "20s",
  "run": ["label-pr", "{short}", "needs-decision"] }
```

Inside, `label-pr` may call `rig note add "$RIG_HOOK_TASK" --kind note --text ...`
or `rig tracker report`. The note records `cause` and actor `hook:pr-label`.
Nothing makes a write-back safe to repeat in 0.7.0: if your program can run
twice for one event, check for its own earlier result first. The event id in
`RIG_HOOK_EVENT` is a stable key to deduplicate by.

**Preview environment up and down.** One hook with a list sees setup and
teardown in order. Stopped tasks are not final, so list both.

```json
{ "name": "preview", "on": ["enter:pr_open", "final", "enter:stopped"], "env": ["GH_TOKEN"],
  "run": ["preview-env", "{id}", "{state}"] }
```

`preview-env` brings the preview up for `pr_open` and down for `merged` or
`stopped`, from `{state}`. It does the work within the timeout, or hands it to
`rig hooks start`.

**Worktree or devcontainer setup and cleanup.**

```json
{ "name": "worktree", "on": ["enter:implementing", "final"], "run": ["setup-or-clean", "{slug}", "{state}"] }
```

It runs on the machine that made the write. It misses a task that drops back to
`implementing` because a commit moved it, which writes nothing (see
[Known limits](#known-limits)).

**Drive the agent loop.** Start the next agent when a task enters `changes` or
`reviewed`. The agent outlives the hook, so use `rig hooks start`:

```json
{ "name": "next-agent", "on": ["enter:changes", "enter:reviewed"],
  "run": ["rig", "hooks", "start", "--", "run-agent", "{short}", "{state}"] }
```

The agent's own `rig` writes are not counted as hook-caused, so the loop does
not stop after three rounds.

**A dashboard.** Hooks are inline, so they can lose an event if the endpoint
is down. A dashboard that must lose none reads the feed instead:

```
rig events --follow --json --cursor-file ~/.rig-dashboard.cursor | send-to-dashboard
```

The cursor file holds the place, so a restart picks up where the last run
stopped. See [rig events](#rig-events).

## rig events

```
rig events [--since <file> | --cursor-file <file>] [--from-start] [--follow] [--json] [--repo <dir>]
```

Prints every event across all tasks, plus notes that belong to no task, from a
cursor.

- **Output.** One line per event: time, task slug (`-` for a repository note),
  kind and actor. With `--json`, one object per line, in the payload shape
  above: `{"format": 1, "event": ..., "task": ...}`, with no `effective`. The
  `task` is the task as recorded right after that event.
- **Where it starts.** With no cursor, `rig events` starts at the current end of
  every log and prints nothing old. `--from-start` replays everything.
- **The cursor file.** `--cursor-file <file>` reads the cursor if the file
  exists, and rewrites it after every scan. A missing file starts at the
  current end (or at the beginning with `--from-start`). `--since <file>`
  reads a cursor but never writes it. The two are exclusive. The cursor is a
  file because one for thousands of tasks is too long for a command line. Its
  format is opaque: do not edit it.
- **`--follow`.** Scans every second until interrupted, so an event written
  while you were busy is seen on the next scan.
- **Held logs.** If a task's log has lines this rig cannot read (written by a
  newer rig), the feed does not advance past that task and prints
  `rig events: <task> held: <n> unreadable lines in its log` on standard error.
  After you upgrade, the events are delivered. They are never skipped silently.
- **Order.** Each task's events come in order. There is no order across tasks.
- **Delivery.** At least once across a crash between printing and saving. Use
  the event `id` to deduplicate.

## Known limits

- **Bulk `rig tracker` commands run no inline hooks.** `rig tracker pull`,
  `refresh`, `report` and `push` write to many tasks in one run, and a 1-second
  hook would turn a 500-task sync into minutes. A consumer that must see their
  events reads `rig events`.
- **A state change that writes nothing fires nothing.** When a commit lands on a
  task bound to a sha, the task is read as back in `implementing` and nothing is
  written. No hook sees it.
- **Hooks are best-effort.** A failed or timed-out hook is reported once and not
  retried. Queued, retried delivery is planned for 0.8.0.
- **No hook can change a command's exit code.**

# Changelog

All notable changes to rig are recorded in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Hooks have a `name`, a per-hook `timeout` (up to 30 seconds), `env` and a `mode`. Selectors are `transition`, `enter:<state>`, `leave:<state>`, `final`, `kind:<kind>` and `kind:*`, and `on` may be a list. `queued` mode and the `from` and `maxDepth` keys are accepted but not acted on yet. A hook without a name is called `hook-<n>` with a warning.
- Hooks run for every event a single-task write produces, not only the first event of `rig task move`. Their payload is `{"format":1,"event","task","effective"}`. Bulk `rig tracker` commands run no inline hooks.
- Hooks run as `RIG_ACTOR=hook:<name>`, with `RIG_HOOK_NAME`, `RIG_HOOK_EVENT` and `RIG_HOOK_TASK`. A write a hook makes records a `cause` with a depth read from the log, and a write at depth 3 runs no inline hooks. A hook that times out gets SIGTERM and 5 seconds before it is killed (Unix).
- `rig events [--since <file> | --cursor-file <file>] [--from-start] [--follow] [--json]` lists events across all tasks and repository notes, from a cursor.
- `rig hooks start [--json] [--] <cmd> [args...]` starts long work that outlives its hook, without the `RIG_HOOK_*` variables.
- `docs/hooks.md`: the hook reference, with recipes.

### Changed

- Hooks now get an allowlisted environment instead of rig's whole environment. `PATH`, `HOME`, locale, proxy, desktop-session and tool-config variables and `RIG_WORKFLOW` pass; `GH_TOKEN`, `RIG_*_TOKEN`, cloud credentials and `SSH_AUTH_SOCK` do not. A hook that relied on one lists it, for example `{ "on": "enter:pr_open", "env": ["GH_TOKEN"], "run": ["pr-comment", "{short}"] }`. `command` gates and `exec` tracker programs are unchanged.

## [0.6.0] - 2026-11-26

Tracker create and push have been tested against fakes only. A live check against GitHub, GitLab and Jira Cloud is planned for 0.7.0.

### Added

- `rig tracker push` creates issues in the linked tracker for tasks that have none, and sub-issues under a parent. It works with Jira, GitHub, GitLab and `exec` trackers. A retry does not create a duplicate: each attempt is recorded as an intent event and the issue carries a marker label `rig/<repo>/<slug>-<8 hex>` that a retry searches for. A partial failure returns the created key and URL with the error. It writes a `tracker-push` event, which older versions skip. (#78, #80)
- The workflow file's `tracker` key gains `push` with `levels` and `types`, which choose the issue type for each task level. `rig workflow show` prints them, and validation names the path of a bad value. (#69)
- The tracker port gains `Create` and `IssueTypes`, with a contract test suite that adapters must pass. (#70)
- GitHub adapter creates issues and sub-issues, with labels and, when the organisation has them, issue types. (#71)
- Jira adapter creates issues on Cloud and on Server/Data Center, including a parent through `fields.parent` or an Epic Link field. (#73)
- GitLab adapter creates project work items with a parent, creating missing labels first, and group epics are keyed `epic-<iid>`. (#74, #76)
- The `exec` tracker protocol gains the ops `create` and `types`. A program that answers `unsupported` keeps working, and the protocol version stays 1. (#72)
- Release runbook and site: a rig-site rebuild is dispatched on release. (#68)
- Docs: `rig tracker push` in the README, `docs/design.md`, `docs/workflows.md` and `docs/phase-3.5.md`, and Mermaid diagrams for the release flow, the command map, task moves, the review loop and tracker links. (#81, #82, #83)

### Changed

- `rig` with no command or an unknown one prints an indented usage screen with one-line descriptions, at most 100 columns wide. `rig tracker` usage lists `push`. (#79)
- Pushing a task under a parent already on the tracker picks the issue type by one rule: the task's own type, else its own issue's type, else one level below the parent. Push refuses only when the parent's type is not a known level. (#80)
- The CI `go test` step runs with a 20 minute timeout on all three operating systems. (#77)

### Fixed

- GitHub create sends the body and acceptance checklist exactly, and returns the created parent. When attaching or re-reading fails after the issue was created, it returns the issue's key and link with the error. (#75, #80)
- The `exec` adapter reports `create` and `types` only when the program supports them, validates the key a create returns, and returns `ErrUnsupported` for `types` when the program has none. (#75)

## [0.5.0] - 2026-11-24

### Added

- `rig tracker report <task>` sends a linked task's progress to its issue: a status change from the workflow's `tracker.report.status`, then a comment from a fixed template (`--comment pr|state|summary`). Findings and notes are sent only with `--include`. `--dry-run` prints exactly what a real run would send, and a repeated report is skipped. It exits 1, sending nothing, when the state has no mapping or the tracker has no such transition. It writes a `tracker-report` event, which older versions skip. (#47, #48)
- `--actor <name>` (alias `--role`) on the commands that write the store (`task new`, `edit`, `move`, `set`, `count`, `parent`, `link`, `unlink`; `finding add`, `set`; `note add`; `tracker pull`, `refresh`). Precedence: the flag, then `$RIG_ACTOR`, then git `user.name`. An empty value, a control character, or `--actor` and `--role` with different values exits 2. (#45)
- `rig login jira --token` as the new name for `--pat`; `--pat` still works. (#48)

### Changed

- `--actor` also rejects invisible formatting characters (bidi overrides, zero-width characters, line and paragraph separators). (#46)
- `rig task finish` refuses with exit 1 and changes nothing when the main checkout has uncommitted changes to tracked files, or a merge, cherry-pick, revert, rebase, bisect or sequencer operation in progress. (#46, #48)
- `rig version` with an argument exits 2. (#48)
- In `rig tracker report`, a dry run honours trackers that cannot comment, and a status the tracker does not support is dropped before the skip check. (#48)

## [0.4.0] - 2026-11-24

### Added

- `rig tracker pull | refresh | show | status` link tasks to issues in an external tracker. (#38)
- Tracker adapters: GitHub issues through `gh` (#35), GitLab issues through `glab` (#36), Jira Cloud REST v3 and Server/Data Center REST v2 (#37), and `exec`, which runs a program of your own over a JSON protocol (#34).
- Config keys `tracker.command` (global only), `tracker.jira.api` and `tracker.jira.epicField`, and the `exec` tracker kind. A `tracker.url` from a committed or unverifiable `.rig/config.json` is dropped. (#27)
- An optional top-level `tracker` key in the workflow file (`intake.states`, `map`, `report.status`), and `parent`, `external` and `drift` in the task view. (#30)
- Task hierarchy and external links: `task new --from`, `task parent`, `task link`, `task unlink`, `task list --parent`, `--roots` and `--tree`, and `task show --children`. Fields owned by a tracker link cannot be changed with `task set` or `task edit`. (#28, #40)
- `--format text|markdown|json` (aliases `--json`, `--markdown`) on `task show`, `task list` and `context`, with a stable Markdown rendering. (#33)
- `rig task start <slug>` and `rig task finish <task> --sha <sha>` create and merge task worktrees under `worktrees.root`. (#42)
- `rig status` gains a store section (`STORE TASKS`, `STORE FINDINGS`) and a `store` field in `--json`. (#41)

### Changed

- `rig push <task-id>` finds the task file in the enclosing workspace repository when the code repository is a child clone. (#39)
- Token environment variables (`RIG_<X>_TOKEN`) are removed from the environment of the `gh`, `glab` and tracker programs rig runs. (#31, #35, #36)

### Fixed

- A task with no external link no longer gets an invalid branch such as `rig/` from a branch template containing `{key}`; the short id is used instead. (#29)

## [0.3.0] - 2026-11-23

### Added

- A generic task store kept in git refs under `refs/rig/tasks`, with findings and notes. (#9, #11)
- `rig task new | show | list | move | set | count | is | log | edit`. Moves are checked against the workflow's gates, and a refused move exits 1 with no event written. (#18)
- `rig finding add | list | set` and `rig note add | list`. (#17)
- `rig workflow show | validate`, and the built-in `basic` workflow. Workflow files define states, gates and hooks, and are chosen by `$RIG_WORKFLOW`, the `workflow` config key, or the preset. (#10, #13, #14)
- `rig next [--who] [--limit] [--json]` lists the tasks that can move, human first. (#15)
- `rig context <task> [--budget] [--json]` prints a task's brief within a size budget. (#16)
- `rig config get | set | unset | list` with `--global`, `--repo` and `--local` scopes, `--json` and `--show-origin`. Layered config reads global, repository, local and `.claude/rig.json` files. `pushPrefixes`, `push.prefixes`, `workflow` and `jira.token` cannot be set this way. (#8, #12)
- `rig login github | gitlab | jira | status` and `rig logout`. Jira tokens are kept in the OS keychain or a `0600` file and are never taken from the command line. (#7, #19)
- `docs/workflows.md` describes the workflow file format. (#22)

### Fixed

- `rig login jira` refuses a `tracker.url` from a tracked `.rig/config.json`, including a tracked `.RIG` directory, a gitlink, or a symlinked `.rig`, and exits 2 before any prompt. (#21, #23)
- `rig login github` and `gitlab` name the host before running `gh` or `glab auth login`, and use `forge.host` only when it fits the forced kind. (#21)

## [0.1.0] - 2026-11-20

### Added

- `rig push <task-id|branch> [--worktree <path>]` pushes one branch to `origin`. It refuses the default branch, tags, refspecs and force pushes, and pushes only branches whose prefix is in `pushPrefixes` (default `task`, `record`).
- `rig pr view`, `rig pr list`, `rig pr checks`, `rig pr open` and `rig pr wait` give GitHub and GitLab one output form, with `--json` and stable exit codes. They run `gh` or `glab` and use your existing login.
- `rig status [--json]` prints the handoff note, task files, worktrees and open pull requests.
- `rig version`.
- Optional `.claude/rig.json` with the keys `forge`, `host` and `pushPrefixes`.

[Unreleased]: https://github.com/AindriuB/rig/compare/v0.6.0...HEAD
[0.6.0]: https://github.com/AindriuB/rig/compare/v0.5.3...v0.6.0
[0.5.0]: https://github.com/AindriuB/rig/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/AindriuB/rig/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/AindriuB/rig/compare/v0.1.0...v0.3.0
[0.1.0]: https://github.com/AindriuB/rig/releases/tag/v0.1.0

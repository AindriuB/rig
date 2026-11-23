# rig

`rig` is a small command-line tool for the mechanical parts of a
branch-per-task workflow: pushing a task branch, opening and reading pull
requests, waiting for CI, keeping tasks, findings and notes, and printing where
the work stands. It was built for loops where coding agents run these
steps many times a day, but it does not depend on any particular loop.

It exists for two reasons:

- **A narrow push.** `rig push` takes a task id or a branch name and nothing
  else. It cannot push the default branch, a tag, a refspec or a force push,
  so it is safe to allow without a prompt.
- **One interface over two forges.** The same commands and the same output
  work on GitHub and GitLab. rig runs `gh` or `glab` underneath and uses the
  login you already have. It stores a token only for Jira, which has no such
  tool, and then in the OS keychain.
- **Tasks and rules you define.** Tasks, findings and notes live in your
  repository's git directory, outside any branch. A workflow file you write
  says which states a task passes through and what must be true before each
  move, and rig refuses a move that breaks it. See
  [docs/workflows.md](docs/workflows.md).

## Install

rig needs Go 1.24 or later, `git`, and `gh` (GitHub) or `glab` (GitLab).

```bash
go install github.com/AindriuB/rig/cmd/rig@v0.3.0
```

## Commands

```text
rig push <task-id|branch> [--worktree <path>]
rig pr view [<pr|branch> | --task <id>] [--json] [--repo <dir>]
rig pr list [--mine] [--head <branch>] [--state open|closed|merged] [--limit <n>] [--json] [--repo <dir>]
rig pr checks [<pr|branch> | --task <id>] [--json] [--repo <dir>]
rig pr open --head <branch> --title <text> [--base <branch>] [--body <text>] [--draft] [--repo <dir>]
rig pr wait <pr-number | --run <run-id>> [--repo <dir>]
rig status [--json]
rig task new --slug <s> --title <text> [--body <text> | --body-file <path>] [--field <name>=<value>]... [--json] [--repo <dir>]
rig task show <task> [--findings] [--notes] [--history] [--json] [--repo <dir>]
rig task edit <task> [--json] [--repo <dir>]
rig task list [--state <s>]... [--all] [--json] [--repo <dir>]
rig task move <task> <state> [--sha <sha>] [--pr <n>] [--reason <text>] [--json] [--repo <dir>]
rig task set <task> --field <name>=<value>... [--json] [--repo <dir>]
rig task count <task> <counter> [--json] [--repo <dir>]
rig task is <task> <state> [--repo <dir>]
rig task log <task> [--json] [--repo <dir>]
rig finding add <task> --severity <sev> --summary <text> [--path <p[:line]>] [--key <k>] [--field <name>=<value>]... [--json] [--repo <dir>]
rig finding list <task> [--status <s>]... [--blocking] [--json] [--repo <dir>]
rig finding set <task> <finding> --status <s> [--json] [--repo <dir>]
rig note add [<task>] --kind <k> --text <text> [--json] [--repo <dir>]
rig note list [<task>] [--kind <k>] [--limit <n>] [--json] [--repo <dir>]
rig workflow show [--json] [--repo <dir>]
rig workflow validate [<file>] [--json] [--repo <dir>]
rig next [--who agent|human] [--limit <n>] [--json] [--repo <dir>]
rig context <task> [--budget <tokens>] [--json] [--repo <dir>]
rig config get <key> [--global|--repo|--local] [--json]
rig config set <key> <value> --global|--repo|--local
rig config unset <key> --global|--repo|--local
rig config list [--global|--repo|--local] [--show-origin] [--json]
rig login
rig login <service> [--url <site>] [--user <email>] [--repo] [--store keychain|file]
rig login status [--json]
rig logout <service> [--url <site>] [--repo]
rig version
```

| Command | What it does | Exit codes |
|---|---|---|
| `rig push` | Pushes one branch to `origin`. A task id such as `07` resolves to `task/07-<slug>`. | 0 pushed; 1 the push failed; 2 refused, git never ran |
| `rig pr view`, `checks`, `list` | Prints pull requests and their CI state in one neutral form. | 0; 2 on any error |
| `rig pr open` | Opens a pull request for a branch that is already pushed. It never pushes. | 0; 2 on any error |
| `rig pr wait` | Blocks until a pull request's checks, or one CI run, finish. | 0 green; 1 red; 2 error or timeout |
| `rig status` | Prints the handoff note, task files, worktrees and open pull requests. | 0; 2 on a usage error |
| `rig task`, `rig finding`, `rig note` | Create and read tasks, findings and notes, and move a task between states. `rig task is` answers "is it in this state?" with its exit code. | 0; 1 refused by a workflow gate, nothing written; 2 on any error |
| `rig workflow` | Shows the workflow in effect, or checks a workflow file. | 0; `validate` 1 invalid; 2 on any error |
| `rig next`, `rig context` | List what each open task needs, and print a size-limited brief for one task. They never call the forge. | 0; 2 on any error |
| `rig config` | Reads and writes settings in `~/.rig/config.json` (`--global`, `--repo`) or `<repo>/.rig/config.json` (`--local`). `rig config set` needs a scope. | 0; `get` 1 not set; 2 on any error |
| `rig login`, `rig logout` | `login` hands over to `gh` or `glab` for GitHub and GitLab, and stores a Jira token in the keychain. `rig login status` shows where you are logged in, never a token. | 0; 1 a login failed; 2 on any error |

An unknown flag always exits 2. `--json` output and the exit codes are a
stable contract, described in [docs/design.md](docs/design.md). The store and
the task commands are described in [docs/phase-2.md](docs/phase-2.md), config
and login in [docs/phase-2.5.md](docs/phase-2.5.md), and the workflow file
format, with a worked example, in [docs/workflows.md](docs/workflows.md).

Tasks live in `refs/rig/` and `.git/rig/` of the clone, so they stay on your
machine: rig does not push or fetch them, and a repository looks the same to
anyone who does not use rig. With no setup, rig uses a small built-in
`basic` workflow (`open`, `active`, `done`).

## What `rig push` will push

A branch is pushed only if all of these hold:

- it is `<prefix>/<name>`, and the prefix is in `pushPrefixes` (by default
  `task` and `record`);
- it uses only letters, digits and `. _ / -`;
- it is not the repository's default branch;
- it exists locally.

It is pushed to the branch of the same name on `origin`. There is no option
for another remote, a force push or tags.

## Configuration

`rig config` holds the newer settings (the forge, the tracker, the workflow);
see [docs/phase-2.5.md](docs/phase-2.5.md). `.claude/rig.json` in the
repository is optional and still read. Every key has a default.

| Key | Meaning | Default |
|---|---|---|
| `forge` | `github` or `gitlab` | detected from the origin host |
| `host` | the forge's host, for a self-hosted instance | the origin host |
| `pushPrefixes` | branch prefixes `rig push` may push | `["task", "record"]` |

`pushPrefixes` widens what `rig push` can do without a prompt, so treat the
file as yours: do not let an agent edit it unasked.

## Development

```bash
go vet ./...
```

```bash
go test ./...
```

The tests use throwaway git repositories and stub `gh` and `glab` programs, so
they need no network and no login. The code is in three layers: commands in
`cmd/rig`, the forge interface in `internal/forge`, and one adapter per forge
below it. [docs/design.md](docs/design.md) has the full design and the phases
planned next.

## Releasing

1. Set `version` in `cmd/rig/version.go`. A new command, flag or JSON field
   raises the minor version; a change to an existing one raises the major.
2. Tag the commit `v<version>` and push the tag.
3. The release workflow builds the binaries and drafts a GitHub release.

## License

Copyright 2026 Andrew Bannister. Licensed under the Apache License,
Version 2.0; see [LICENSE](LICENSE).

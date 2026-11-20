# rig

`rig` is a small command-line tool for the mechanical parts of a
branch-per-task workflow: pushing a task branch, opening and reading pull
requests, waiting for CI, and printing where the work stands. It was built
for [claude-kit](https://github.com/AindriuB/workspace), where coding agents
run these steps many times a day, but it does not depend on the kit.

It exists for two reasons:

- **A narrow push.** `rig push` takes a task id or a branch name and nothing
  else. It cannot push the default branch, a tag, a refspec or a force push,
  so it is safe to allow without a prompt.
- **One interface over two forges.** The same commands and the same output
  work on GitHub and GitLab. rig runs `gh` or `glab` underneath and uses the
  login you already have; it never holds a token.

## Install

rig needs Go 1.24 or later, `git`, and `gh` (GitHub) or `glab` (GitLab).

```bash
go install github.com/AindriuB/rig/cmd/rig@v0.1.0
```

While the repository is private, tell Go not to use the public proxy:

```bash
GOPRIVATE=github.com/AindriuB/* go install github.com/AindriuB/rig/cmd/rig@v0.1.0
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
rig version
```

| Command | What it does | Exit codes |
|---|---|---|
| `rig push` | Pushes one branch to `origin`. A task id such as `07` resolves to `task/07-<slug>`. | 0 pushed; 1 the push failed; 2 refused, git never ran |
| `rig pr view`, `checks`, `list` | Prints pull requests and their CI state in one neutral form. | 0; 2 on any error |
| `rig pr open` | Opens a pull request for a branch that is already pushed. It never pushes. | 0; 2 on any error |
| `rig pr wait` | Blocks until a pull request's checks, or one CI run, finish. | 0 green; 1 red; 2 error or timeout |
| `rig status` | Prints the handoff note, task files, worktrees and open pull requests. | 0; 2 on a usage error |

An unknown flag always exits 2. `--json` output and the exit codes are a
stable contract, described in [docs/design.md](docs/design.md).

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

`.claude/rig.json` in the repository is optional. Every key has a default.

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

# rig

`rig` is a command-line toolkit for branch-per-task development and agent
workflows. It combines guarded branch pushes and pull-request/CI operations
with a local task, finding, note, and workflow system. It works with GitHub
and GitLab, and is designed to be usable by people and automation through
stable exit codes and JSON output.

Forge operations use the `gh` or `glab` command-line client and its existing
login; rig does not store forge tokens. Git operations use `git` directly.

## Install

Requires Go 1.24 or later, `git`, and `gh` (GitHub) or `glab` (GitLab).

```bash
go install github.com/AindriuB/rig/cmd/rig@v0.1.0
```

If the module is private, configure Go to fetch it directly:

```bash
GOPRIVATE=github.com/AindriuB/* go install github.com/AindriuB/rig/cmd/rig@v0.1.0
```

## Commands

Run `rig --help` for the full syntax and command-specific flags. Repository
commands generally accept `--repo <dir>`; commands that have machine-readable
output support `--json`.

| Command group | What it does |
|---|---|
| `rig push` | Pushes one permitted local branch to the same branch on `origin`. |
| `rig pr` | Views and lists pull requests, reads checks, opens a PR for an already-pushed branch, and waits for checks or a CI run. Supports GitHub and GitLab. |
| `rig status` | Summarizes handoff notes, task files, worktrees, and open pull requests. |
| `rig task` | Creates, lists, shows, edits, and transitions tasks; manages task fields and counters and displays task history. |
| `rig finding` | Adds and lists findings attached to tasks, with workflow-defined severities and fields. |
| `rig note` | Adds and lists task or repository notes. |
| `rig workflow` | Shows or validates a workflow definition. |
| `rig next` / `rig context` | Previews available task transitions and assembles task context for a caller. |
| `rig config` | Reads, lists, and updates supported configuration at an explicit scope. |
| `rig login` / `rig logout` | Checks or manages GitHub, GitLab, and Jira authentication. |
| `rig version` | Prints the installed version. |

### Push safety

`rig push <task-id|branch>` accepts only an existing local branch under an
allowed prefix (defaults: `task` and `record`), and pushes it to the matching
branch on `origin`. It refuses the default branch, tags, refspecs, other
remotes, and force pushes. A task ID resolves to its task branch when one can
be derived. `pushPrefixes` in `.claude/rig.json` can widen the allowed prefixes,
so treat that file as trusted configuration.

### Tasks and workflows

Tasks are stored as append-only event logs in Git refs, not as mutable files in
the working tree. Current task state, fields, counters, findings, and notes are
derived from those events. The `basic` workflow is built in; workflows can
define states, transitions, gates, hooks, fields, findings, and counters.
Transition gates are checked before a write. Hooks run after it, so a hook
failure does not roll the transition back. `rig next` is a preview: command
gates are reported as not run.

### Configuration and authentication

Configuration supports global (`~/.rig/config.json`), repository-specific,
and checkout-local (`.rig/config.json`) settings, plus the legacy
`.claude/rig.json` forge and push settings. Supported keys include
`forge.kind`, `forge.host`, `push.prefixes`, `tracker.kind`, `tracker.url`,
`tracker.project`, and `workflow`. Run `rig config --help` for scope syntax.
Credentials do not belong in config; use `rig login` instead. GitHub and
GitLab authentication is delegated to `gh` and `glab`. Jira credentials can be
stored in the OS keychain or an explicitly selected file store; the
`RIG_JIRA_TOKEN` environment variable can override a stored token without
writing it to disk.

## Development

The implementation is organized into three layers: CLI commands in `cmd/rig`,
interfaces and domain logic in `internal`, and adapters for Git, forge CLIs,
credentials, and Git-backed task storage. Run:

```bash
go vet ./...
go test ./...
```

Tests use temporary Git repositories and stub forge executables, so they need
no network or account login.

## Releasing

Update the version in `cmd/rig/version.go`, tag the commit `v<version>`, and
push the tag. The release workflow tests the code, builds Linux, macOS, and
Windows binaries, generates SHA-256 checksums, and creates a draft GitHub
release for manual publication.

## License

Copyright 2026 Andrew Bannister. Licensed under the Apache License,
Version 2.0; see [LICENSE](LICENSE).

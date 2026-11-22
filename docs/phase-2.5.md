# Phase 2.5 plan: `rig config` and `rig login`

Status: shipped in 0.3.0, together with [phase 2](phase-2.md). This document
has been amended to describe what shipped.

Two commands the user asked for:

- **`rig config`** sets lasting settings at two levels, global in the user's
  home and local to one project, the way `git config --global` and
  `--local` do.
- **`rig login <service>`** stores what rig needs to reach a service's API,
  such as a Jira token, globally or for one project. `rig logout` removes it.

They also settle where a project declares its forge and its tracker. They
do not depend on the store from [phase 2](phase-2.md), so they are a
phase of their own. It shipped as **0.3.0** together with phase 2; there was no
0.2.0 release. Its first wave touched no file phase 2 owns and ran alongside
phase 2.

## What changes in the design

The design used to say **"rig never holds a token."** For GitHub and GitLab that
stays true: rig keeps using `gh` and `glab`, and `rig login github` hands
over to `gh auth login`. A service with no CLI of its own, Jira first,
has nothing to hand over to, so rig stores its token. The decision becomes:
*rig holds a token only for a service with no CLI, and only in the OS
keychain unless the user explicitly chooses a file.* This replaced the old
decision in [design.md](design.md). See Q9.

## Config

### Files, scopes and precedence

An organisation's choices, such as Jira as the tracker, are the same across
its projects, so **the global file is the main place for settings**. What
differs between projects, such as the Jira project key, goes in a section of
the global file keyed by the repository. A file in the project overrides
both for one checkout; rig never commits it and never hides it.

| Scope | Where | For |
|---|---|---|
| `--global` | `~/.rig/config.json` (`%USERPROFILE%\.rig\config.json` on Windows), top level | the organisation's forge and tracker |
| `--repo` | the same file, under `repos."<repo key>"` | values that differ per project: the tracker project key |
| `--local` | `<repo>/.rig/config.json`, `<repo>` being the main checkout's root, so worktrees share it | an override for this checkout; a developer may commit it to share it |

The **repo key** is the `origin` remote normalised to `host/path` without
`.git` or a user: `git@github.com:acme/rig.git` and
`https://github.com/acme/rig` are both `github.com/acme/rig`. So every clone
and worktree of a repository finds the same `--repo` section, a fresh clone
needs no setup, and backing up one file keeps all of it (see Q20).
A repository with no `origin` has no repo key; `--repo` is then exit 2.

A key is looked up `--local`, then `--repo`, then `.claude/rig.json` for the
two keys it holds (it is specific to the project, so it beats the global
file), then `--global`, then the built-in default.
JSON matches `.claude/rig.json` and needs only the standard library.

**rig does not tie a repository to itself.** It never commits, and the only
change it makes to a tracked file is one ignore line:

- `<repo>/.rig/` is created only by `rig config set … --local`.
- When rig creates that directory, it appends `/.rig/` to the main
  checkout's `.gitignore` if no line there already ignores it, and creates
  `.gitignore` if there is none. The change is left unstaged for the
  developer to commit or not.
- It does this only at the moment it creates `.rig/`. If `.rig/` already
  exists, or any file under it is tracked, rig never touches `.gitignore`.
  So a developer who removes the line to share `.rig/config.json` does not
  get it back: `.rig/` exists from then on. (If they delete both `.rig/`
  and the line, the next `--local` set adds the line again.)
- Nothing else is written: not `.git/info/exclude`, not any other file.

**`tracker.url` and trust.** `tracker.url` decides where a stored Jira token
may be sent, and a cloned repository could carry a committed
`.rig/config.json`. So `tracker.url` is taken from `--url`, `--global` or
`--repo`; from `--local` (`<repo>/.rig/config.json`) only while that file is
not tracked by git (decided with `git ls-files`). A tracked file's
`tracker.url` is refused with a message to use `--url` or to set it globally.

Keys are dotted names on an allow-list, and values are checked when they are
set and when they are read. `rig config set` refuses an unknown key or a bad
value (exit 2), so a typo is never silently stored.

### Forge and tracker are separate

Two settings vary independently, so they are two sections:

- **forge**: where code, pull requests and CI live. `github` or `gitlab`.
  rig detects it from the `origin` remote today and still does.
- **tracker**: where the human-facing record of a task lives. A GitHub
  repository can track its work in Jira.

| Key | Values | Default | Usual scope |
|---|---|---|---|
| `forge.kind` | `github`, `gitlab` | detected from the `origin` host | `--global`, or `--repo` for an exception |
| `forge.host` | a host name | the `origin` host | `--global` |
| `tracker.kind` | `local`, `jira`, `github`, `gitlab` | `local` | `--global` |
| `tracker.url` | `https://…`; required for `jira`, and for `github`/`gitlab` on a self-hosted host | none | `--global` |
| `tracker.project` | Jira: a project key (`[A-Z][A-Z0-9]+`); GitHub: `owner/name`; GitLab: `group/…/name` | `local`: none; `github`/`gitlab`: the forge repository | `--repo` |
| `workflow` | a preset name (`basic`) or an absolute path to a workflow file | `basic` | global file only, hand-edited |
| `push.prefixes` | list of branch prefixes | `["task", "record"]` | global file only, hand-edited |

`tracker.kind = local` means rig's own store from phase 2: tasks live in
`refs/rig/` and nowhere else. It is what "plain files" becomes once rig
holds the state: no service, nothing to log in to. The kit's markdown
`docs/plan/tasks` files are not a tracker kind; they are what the kit uses
without rig, and phase 3's importer moves them into the store (see Q17).

A global file for an organisation on GitHub that tracks in Jira:

```json
{
  "forge":   { "kind": "github" },
  "tracker": { "kind": "jira", "url": "https://acme.atlassian.net" },
  "repos": {
    "github.com/acme/rig":     { "tracker": { "project": "RIG" } },
    "github.com/acme/billing": { "tracker": { "project": "BILL" } }
  }
}
```

`rig config set tracker.project RIG --repo` in a clone of `acme/rig` writes
the `repos` entry.

**No key holds an account or a secret, in any scope.** A developer may
commit `.rig/config.json`, so it must be safe to commit by construction.
Tokens and the accounts they belong to stay in the keychain entry a login
creates (see Login and Q18). The key allow-list has no secret key, and
`rig config set` refuses any key whose last part looks like a credential
(`token`, `secret`, `password`, `apikey`, `key`, `auth`, `credential`, in
any case) with a message pointing to `rig login`, so the refusal stays
explicit even if the allow-list grows.

**With no config at all**, rig behaves as today: the forge is detected from
`origin`, and the tracker is `local`.

**Checks across keys** (`tracker.kind = jira` needs `tracker.url` and
`tracker.project`) are made when a command needs them, not on `set`, so the
keys can be set one at a time. `rig config list` prints a warning line for an
incomplete tracker, and the command that needs it exits 2 naming the missing
key.

**Keys that widen what rig may do are global-only and hand-edited.** Two
keys decide what rig does without asking: `push.prefixes` (which branches
`rig push` may push) and `workflow` (which can name programs for gates and
hooks to run). For both:

- they are read only from `~/.rig/config.json`, top level or a `repos`
  section, never from a project's `.rig/`, which may have arrived in a clone;
- `rig config set` and `unset` refuse them in every scope, so no command an
  agent may be allowed to run can change them; the user edits the file;
- `rig config get` and `list` show them.

So cloning a repository never changes what rig pushes or runs, whatever any
harness's permission rules allow. `pushPrefixes` in `.claude/rig.json` is
still honoured, as in phase 1, and wins when present (see Q10). `forge` and
`host` in `.claude/rig.json` keep working, read as `forge.kind` and
`forge.host`, above `--global` and below `--repo`.

### How the tracker relates to the store

The phase-2 store always holds the loop's state, whatever the tracker:
events, rounds, findings, verified shas. Jira cannot hold those, and
`rig next` must stay fast and work offline, so `rig task` and `rig next`
never route their state through a tracker.

The tracker holds the human-facing record: the issue people see, its title,
description and status. With `local`, the store is that record too. With
`jira`, `github` or `gitlab`, phase 4 adds a tracker port:

```go
type Tracker interface {
    Issue(ctx context.Context, key string) (Issue, error)        // title, description, status, url
    Transition(ctx context.Context, key string, to TaskState) error
    Link(ctx context.Context, key string, pr forge.PullRequest) error
}
```

and a task gains an `issue` field (`{kind, key, url}`), a new JSON field and
so a minor change. `rig task new --issue RIG-42` then fills the spec from the
issue, and each transition mirrors its status to the issue. Phase 2 and 2.5
only read and validate `tracker.kind`; `rig task` works the same under every
kind until phase 4. Nothing in phase 2's formats has to change for this.

### Commands

| Command | What it does | Exit codes |
|---|---|---|
| `rig config get <key> [--global\|--repo\|--local] [--json]` | Prints the value in effect, or in one scope. | 0; 1 not set; 2 error or unknown key |
| `rig config set <key> <value> --global\|--repo\|--local` | Sets it. The scope is required (see Q12). | 0; 2 |
| `rig config unset <key> --global\|--repo\|--local` | Removes it. Removing a key that is not set is exit 0. | 0; 2 |
| `rig config list [--global\|--repo\|--local] [--show-origin] [--json]` | Every key in effect. | 0; 2 |

`--json`: `get` gives `{key, value, scope}`; `list` gives an array of
`{key, value, scope}`, where `scope` is `local`, `repo`, `global`,
`rig.json` or `default`. Writes go to a temporary file and are renamed into place, so a
crash never leaves half a file.

## Login

### Where secrets go

**The OS keychain, by default:**

| OS | Store | How rig reaches it |
|---|---|---|
| macOS | Keychain | `security -i`, commands written on stdin so the token never appears in an argument list |
| Windows | Credential Manager | `CredWriteW`/`CredReadW`/`CredDeleteW` through `syscall` |
| Linux | Secret Service | `secret-tool store`, which reads the secret from stdin |

Entries use service `rig` and account `<service>:<host>` for a global login,
or `<service>:<host>:<repo key>` for a login used by one repository only. The entry's secret is
`{"user": …, "token": …}`: the account and its token are kept together, which
is why no config key names an account. Project scope is a
key in the keychain, never a file in the repository, so `git add .` cannot
pick up a token.

**A file, only when asked:** `--store file` writes
`~/.rig/credentials.json` with mode 0600, and refuses if the file or
`~/.rig` is readable by anyone else. Never in the project. Not offered on
Windows, which always has Credential Manager. On Linux with no Secret
Service running, `rig login` fails and says to use `--store file`; it never
falls back silently.

Since nothing secret is ever written under the repository, rig has no
`.gitignore` to manage.

### Token input

- From a terminal: a prompt, read without echo.
- Otherwise: one line from stdin, so a script can pipe it from a password
  manager.
- Never a flag, so it is never in shell history or a process list.
- At use time only, `RIG_JIRA_TOKEN` overrides the stored token, for CI.
  rig never writes it anywhere (see Q14).
- Never logged, never printed. `rig config list` has nothing to mask,
  because the token is not in config; `rig login status` shows only where it
  is stored.

### Commands

| Command | What it does | Exit codes |
|---|---|---|
| `rig login` | Logs in to this project's forge, then to Jira only when `tracker.kind` is `jira`, each as below. A `github` or `gitlab` tracker shares the forge login, so there is nothing more to do for it. The Jira flags (`--url`, `--user`, `--repo`, `--store`) are accepted only when `tracker.kind` is `jira`. | 0; 1 if either login failed; 2 |
| `rig login github\|gitlab` | If `gh auth status` (or `glab`) says logged in for the host, prints that and stops. Otherwise runs `gh auth login` (`glab auth login`) on the terminal. rig stores nothing. | 0; 1 the tool's login failed; 2 |
| `rig login jira [--url <site>] [--user <email>] [--repo] [--store keychain\|file]` | The site is `--url`, else `tracker.url`. Prompts for the user if `--user` is absent, then reads the token, checks both with `GET <site>/rest/api/3/myself`, and stores them in one keychain entry. Writes no config. `--repo` makes the login apply to this repository only; the default is every repository. | 0; 1 Jira rejected the token, nothing stored; 2 |
| `rig logout jira [--url <site>] [--repo]` | Deletes the keychain entry or file entry for the site. Without `--repo` it looks only for the global entry; if that is absent but this repository has its own, it says so and points to `--repo`. | 0, also when already logged out; 2 on an error, and also when a store could not be checked and the entry was found nowhere, because rig cannot then say the login is gone (the one exception to "0 when already logged out") |
| `rig logout github\|gitlab` | Does nothing and prints the `gh auth logout` (`glab`) command, because that login belongs to `gh` and every other tool that uses it. | 2 |
| `rig login status [--json]` | Each known service: logged in or not, as whom, which scope and store. Never the token. Checks each with one API or `gh`/`glab` call. | 0; 2 |

`rig login status --json` is an array of
`{service, loggedIn, user?, scope?, store?, error?}`. `rig status` does not
gain auth lines: it runs often and makes no extra network calls (see Q15).

The Jira check uses `net/http` from the standard library with a 15-second
timeout; it is the first time rig talks HTTP itself. The check is the only
Jira call in phase 2.5; reading issues is phase 4's tracker adapter.

## Package layout

```
internal/config/          layered config: rig.json, global, local; key allow-list; atomic writes
internal/secret/          port: Get, Set, Delete by account, under the fixed service "rig"
internal/secret/keychain_darwin.go, keychain_windows.go, keychain_linux.go, file.go
internal/tty/             ReadSecret: no echo on a terminal, one line otherwise
internal/proc/            gains an interactive mode with the terminal attached, for `gh auth login`
internal/auth/jira/       the /myself check
cmd/rig/config.go, login.go
```

## Tasks

Three waves. Same rules as phase 2: tests, one code-reviewer pass with
nothing at P2 or above, a `task/` branch and a pull request, not merged.

### Wave 1 (can run alongside phase 2)

**11 config-layers** — owns `internal/config/**`. Depends on: none.
- AC1 Lookup order local → repo → `.claude/rig.json` (its `forge` and `host`
  read as `forge.kind` and `forge.host`) → global → default; table test
  over every combination.
- AC8 The repo key normalises ssh, https and `.git` forms of one remote to
  the same string; no `origin` makes `--repo` an error.
- AC9 Creating `<repo>/.rig/` appends `/.rig/` to `.gitignore` once
  (creating the file if absent) and changes nothing else: a test compares
  `git status --porcelain` before and after, and the only change is
  `.gitignore`. A second `--local` set, a `.gitignore` that already ignores
  `.rig/`, an existing `.rig/` with the line removed, and a tracked
  `.rig/config.json` each leave `.gitignore` untouched.
- AC10 `Set` refuses credential-like keys (`*.token`, `*.password`,
  `*.secret`, `*.apikey`, `*.key`, `*.auth`, `*.credential`, any case) in
  every scope, with a message naming `rig login`.
- AC6 Each key's values are checked on `Set` and on load: `forge.kind`,
  `tracker.kind` enums; `tracker.url` https only; `tracker.project` by kind.
  With no files, `tracker.kind` is `local`.
- AC7 `Tracker()` returns the effective tracker or an error naming the
  missing key (`jira` without `tracker.url` or `tracker.project`).
- AC2 `Set` and `Unset` refuse unknown keys, `pushPrefixes` in any spelling,
  and `push.prefixes` and `workflow` in every scope; both are read only from
  the global file, and a project `.rig/` that sets them is ignored with a
  warning.
- AC3 Writes are atomic (temp file and rename) and leave no temp file behind.
- AC4 `HOME`/`USERPROFILE` come from an injected function so tests never touch
  the real home.
- AC5 Existing `Load` callers and tests pass unchanged.

**12 secret-store** — owns `internal/secret/**`. Depends on: none.
- AC1 The file store writes mode 0600, refuses a file or directory readable
  by others, and round-trips set, get and delete.
- AC2 The macOS adapter passes the token only on stdin: a test with a stub
  `security` on `PATH` checks the argument list holds no token.
- AC3 Same for `secret-tool` on Linux.
- AC4 Real keychain tests run only when `RIG_TEST_KEYCHAIN=1`; CI skips them.
- AC5 The Windows adapter builds on `windows` (CI runs `go vet` there).

**13 tty-and-proc** — owns `internal/tty/**`, `internal/proc/**`, `go.mod`, `go.sum`.
Depends on: none.
- AC1 `ReadSecret` on a non-terminal reads one line and strips `\r\n`.
- AC2 `proc.Interactive` runs a program with the terminal's stdin, stdout and
  stderr, still from an argument vector.
- AC3 The only new module dependency is `golang.org/x/term` (see Q13).

### Wave 2 (after phase 2's task 03, which owns `main.go`)

**14 config-command** — owns `cmd/rig/config.go`, `cmd/rig/config_test.go`,
`cmd/rig/main.go`, and a stub `cmd/rig/login.go`. Depends on: 11, phase 2's 03.
- AC1 Every `rig config` form and exit code above.
- AC2 `rig config set pushPrefixes …`, `push.prefixes` and `workflow` are
  exit 2 and write nothing.
- AC3 `main.go` dispatches `config`, `login` and `logout`.

**15 login-command** — owns `cmd/rig/login.go`, `cmd/rig/login_test.go`,
`internal/auth/**`. Depends on: 12, 13, 14.
- AC1 `rig login github` with a stub `gh` that reports logged in runs no
  `gh auth login`; with one that reports logged out, it runs it.
- AC2 `rig login jira` against an `httptest` server: a 200 stores the token
  and the references; a 401 is exit 1 and stores nothing.
- AC3 No token in stdout, stderr, config files or any argument list a stub
  sees (a test greps for a marker token).
- AC4 `--repo` keys the secret by the repo key and writes no config file.
- AC6 `rig login` with no service logs in to the forge, then to Jira when
  `tracker.kind` is `jira`; `rig login jira` takes the site from `tracker.url`.
- AC5 `rig logout github` is exit 2 and prints the `gh auth logout` command.

### Wave 3

**16 docs-and-version** — owns `docs/design.md`, `README.md`,
`cmd/rig/version.go`, `docs/phase-2.5.md`. Depends on: 14, 15.
- AC1 `version` is `0.3.0`.
- AC2 The design's "rig never holds a token" decision is replaced by the one
  above, and the phase table gains 2.5.
- AC3 The contract gains `rig config`, `rig login`, `rig logout` and
  `rig login status`, as additions only.

## The kit's side

For the kit's follow-up, not this phase: its project settings template
should make `rig config set`, `rig config unset`, `rig login` and
`rig logout` ask first, as it does for `.claude/rig.json`. Nothing an agent
runs unprompted should change where rig sends data or which credentials it
uses.

## Open questions

**Q8. Its own phase.** `rig config` and `rig login` share nothing with the
store. *Recommend: phase 2.5, 0.3.0, wave 1 alongside phase 2.*

**Q9. Holding tokens.** *Recommend: rig holds a token only for a service with
no CLI, in the keychain by default; GitHub and GitLab stay with `gh` and
`glab`.*

**Q10. `.claude/rig.json` and keys that widen rig.** *Recommend: keep
reading `.claude/rig.json` through 0.x (its `pushPrefixes` wins when
present). The generic homes are `push.prefixes` and `workflow` in the global
file only, never set by `rig config set`, so an agent or a cloned repository
cannot widen pushes or make rig run programs.*

**Q11. Format.** JSON objects give the `forge` and `tracker` sections an
INI or TOML file would, with only the standard library, and match
`.claude/rig.json`. *Recommend: JSON, `config.json`, dotted keys on an
allow-list.*

**Q12. Default scope.** With three scopes, any default is wrong for some
keys: `tracker.kind` belongs in `--global`, `tracker.project` in `--repo`.
*Recommend: `rig config set` and `unset` require a scope; `get` and `list`
show the value in effect. A login applies to every repository unless
`--repo` is given.*

**Q13. First dependency.** Reading without echo needs `golang.org/x/term`
(the Go team's module), or about 60 lines of `stty` and `SetConsoleMode`
code. *Recommend: `x/term`.*

**Q14. A token from the environment.** `RIG_JIRA_TOKEN` at use time lets CI
work without a keychain; it is never stored. *Recommend: allow it.*

**Q15. Where auth state shows.** *Recommend: `rig login status`, not
`rig status` and not a new `rig doctor`; `rig status` stays fast and
offline-tolerant.*

**Q16. When to build the Jira part.** Nothing else in rig uses a Jira token
until phase 4's tracker adapter. Building `rig login jira` now is useful only
for the login check itself. *Recommend: build it now as asked, since the
`/myself` check gives it a real test, and phase 4 then only reads the
token.*

**Q17. Tracker kinds.** `local` (rig's store, no service), `jira`, `github`
(GitHub Issues), `gitlab` (GitLab issues). "Plain files" is `local`: the
state is in git, needs no service, and does not conflict on merge. The
kit's markdown task files are not a kind rig reads; phase 3 imports them.
*Recommend: these four, default `local`.*

**Q18. Secrets in config.** A developer may commit `.rig/config.json`.
*Recommend: no config key holds an account or a token in any scope; tokens
stay in the keychain; credential-like keys are refused by name as well as by
the allow-list.*

**Q19. The store and the tracker.** *Recommend: the store holds the loop's
state under every tracker kind, and `rig task`/`rig next` never go through
the tracker. Phase 4 adds the `Tracker` port above, an `issue` field on a
task and `rig task new --issue <key>`; phase 2 and 2.5 only validate
`tracker.kind`.*

**Q20. Where per-project values go.** Three options: a `.rig/` file in each
clone, which a fresh clone or a deleted one loses; a section of the global
file keyed by the repository's remote, which every clone and worktree finds;
or deriving the value from the repository or branch name, which breaks as
soon as the names do not match the Jira key. *Recommend: the global file,
keyed by the repo key (`--repo`), with `.rig/` as a private per-clone
override above it. rig adds `/.rig/` to `.gitignore` once, when it creates
`.rig/`, and never again; a developer who removes the line to share
`.rig/config.json` keeps that choice.*

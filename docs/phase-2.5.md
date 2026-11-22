# Phase 2.5 plan: `rig config` and `rig login`

Status: draft, waiting for approval. Nothing here is built yet.

Two commands the user asked for:

- **`rig config`** sets lasting settings at two levels, global in the user's
  home and local to one project, the way `git config --global` and
  `--local` do.
- **`rig login <service>`** stores what rig needs to reach a service's API,
  such as a Jira token, globally or for one project. `rig logout` removes it.

They do not depend on the store from [phase 2](phase-2.md), so they are a
phase of their own. It ships as **0.3.0**, after phase 2's 0.2.0. Its first
wave touches no file phase 2 owns and can run alongside phase 2.

## What changes in the design

The design says **"rig never holds a token."** For GitHub and GitLab that
stays true: rig keeps using `gh` and `glab`, and `rig login github` hands
over to `gh auth login`. A service with no CLI of its own, Jira first,
has nothing to hand over to, so rig stores its token. The decision becomes:
*rig holds a token only for a service with no CLI, and only in the OS
keychain unless the user explicitly chooses a file.* See Q9.

## Config

### Files and precedence

| Scope | File |
|---|---|
| global | `~/.rig/config.json` (`%USERPROFILE%\.rig\config.json` on Windows) |
| local | `<repo>/.rig/config.json`, `<repo>` being the main checkout's root, so worktrees share it |

A key is looked up local, then global, then the built-in default. JSON
matches `.claude/rig.json` and needs only the standard library.

Keys are dotted names on an allow-list. `rig config set` refuses an unknown
key (exit 2), so a typo is never silently stored. Phase 2.5's keys:

| Key | Meaning | Scopes |
|---|---|---|
| `forge` | `github` or `gitlab` | both |
| `forge.host` | forge host, for a self-hosted instance | both |
| `jira.url` | the Jira site, `https://<site>` | both |
| `jira.user` | the account email Jira's API token belongs to | both |
| `project.id` | a ULID naming this project in the keychain; written by `rig login --local`, never by hand | local |

On disk a dotted key is nested JSON (`{"jira": {"url": …}}`).

**`pushPrefixes` is not a config key.** It decides what `rig push` may push
without a prompt, and the kit's settings template guards exactly one file
for it, `.claude/rig.json`. If `rig config set` could write it, or a new file
could hold it, an agent could widen pushes with one allowed command. So it
stays only in `.claude/rig.json`, rig never writes that file, and `.rig/`
cannot set it. `forge` and `host` keep working in `.claude/rig.json` too, and
it wins over `.rig/config.json` for them (see Q10).

No secret is ever written to either config file. A login writes only
references: the URL, the user and, for a project login, `project.id`.

### Commands

| Command | What it does | Exit codes |
|---|---|---|
| `rig config get <key> [--global\|--local] [--json]` | Prints the value in effect, or in one scope. | 0; 1 not set; 2 error or unknown key |
| `rig config set <key> <value> [--global\|--local]` | Sets it. Default `--local`; outside a repository, `--global` is required. | 0; 2 |
| `rig config unset <key> [--global\|--local]` | Removes it. Removing a key that is not set is exit 0. | 0; 2 |
| `rig config list [--global\|--local] [--show-origin] [--json]` | Every key in effect. | 0; 2 |

`--json`: `get` gives `{key, value, scope}`; `list` gives an array of
`{key, value, scope}`, where `scope` is `global`, `local`, `rig.json` or
`default`. Writes go to a temporary file and are renamed into place, so a
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
or `<service>:<host>:<project.id>` for a project login. Project scope is a
key in the keychain, never a file in the repository, so `git add .` cannot
pick up a token. The project id lives in `.rig/config.json` and survives the
repository moving.

**A file, only when asked:** `--store file` writes
`~/.rig/credentials.json` with mode 0600, and refuses if the file or
`~/.rig` is readable by anyone else. Never in the project. Not offered on
Windows, which always has Credential Manager. On Linux with no Secret
Service running, `rig login` fails and says to use `--store file`; it never
falls back silently.

Since nothing secret is ever written under the repository, rig has no
`.gitignore` to manage. `.rig/config.json` holds no secrets and can be
committed if the team wants to share `jira.url`.

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
| `rig login github\|gitlab` | If `gh auth status` (or `glab`) says logged in for the host, prints that and stops. Otherwise runs `gh auth login` (`glab auth login`) on the terminal. rig stores nothing. | 0; 1 the tool's login failed; 2 |
| `rig login jira --url <site> --user <email> [--global\|--local] [--store keychain\|file]` | Reads the token, checks it with `GET <site>/rest/api/3/myself`, then stores it and writes `jira.url` and `jira.user` to the chosen scope. Default `--global`. | 0; 1 Jira rejected the token, nothing stored; 2 |
| `rig logout jira [--global\|--local]` | Deletes the keychain entry or file line and the config references. | 0, also when already logged out; 2 |
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
internal/secret/          port: Get, Set, Delete by (service, account)
internal/secret/keychain_darwin.go, wincred_windows.go, secretservice_linux.go, file.go
internal/tty/             ReadSecret: no echo on a terminal, one line otherwise
internal/proc/            gains an interactive mode with the terminal attached, for `gh auth login`
internal/auth/jira/       the /myself check
cmd/rig/config.go, login.go
```

## Tasks

Three waves. Same rules as phase 2: tests, one code-reviewer pass with
nothing at P2 or above, a `task/` branch and a pull request, not merged.

### Wave 1 (can run alongside phase 2)

**10 config-layers** — owns `internal/config/**`. Depends on: none.
- AC1 Lookup order local → global → default, with `.claude/rig.json` winning
  for `forge` and `host`; table test over every combination.
- AC2 `Set` and `Unset` refuse unknown keys and `pushPrefixes` in any spelling.
- AC3 Writes are atomic (temp file and rename) and leave no temp file behind.
- AC4 `HOME`/`USERPROFILE` come from an injected function so tests never touch
  the real home.
- AC5 Existing `Load` callers and tests pass unchanged.

**11 secret-store** — owns `internal/secret/**`. Depends on: none.
- AC1 The file store writes mode 0600, refuses a file or directory readable
  by others, and round-trips set, get and delete.
- AC2 The macOS adapter passes the token only on stdin: a test with a stub
  `security` on `PATH` checks the argument list holds no token.
- AC3 Same for `secret-tool` on Linux.
- AC4 Real keychain tests run only when `RIG_TEST_KEYCHAIN=1`; CI skips them.
- AC5 The Windows adapter builds on `windows` (CI runs `go vet` there).

**12 tty-and-proc** — owns `internal/tty/**`, `internal/proc/**`, `go.mod`, `go.sum`.
Depends on: none.
- AC1 `ReadSecret` on a non-terminal reads one line and strips `\r\n`.
- AC2 `proc.Interactive` runs a program with the terminal's stdin, stdout and
  stderr, still from an argument vector.
- AC3 The only new module dependency is `golang.org/x/term` (see Q13).

### Wave 2 (after phase 2's task 03, which owns `main.go`)

**13 config-command** — owns `cmd/rig/config.go`, `cmd/rig/config_test.go`,
`cmd/rig/main.go`, and a stub `cmd/rig/login.go`. Depends on: 10, phase 2's 03.
- AC1 Every `rig config` form and exit code above.
- AC2 `rig config set pushPrefixes …` is exit 2 and writes nothing.
- AC3 `main.go` dispatches `config`, `login` and `logout`.

**14 login-command** — owns `cmd/rig/login.go`, `cmd/rig/login_test.go`,
`internal/auth/**`. Depends on: 11, 12, 13.
- AC1 `rig login github` with a stub `gh` that reports logged in runs no
  `gh auth login`; with one that reports logged out, it runs it.
- AC2 `rig login jira` against an `httptest` server: a 200 stores the token
  and the references; a 401 is exit 1 and stores nothing.
- AC3 No token in stdout, stderr, config files or any argument list a stub
  sees (a test greps for a marker token).
- AC4 `--local` writes `project.id` once and keys the secret by it.
- AC5 `rig logout github` is exit 2 and prints the `gh auth logout` command.

### Wave 3

**15 docs-and-version** — owns `docs/design.md`, `README.md`,
`cmd/rig/version.go`, `docs/phase-2.5.md`. Depends on: 13, 14.
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

**Q10. `.claude/rig.json`.** *Recommend: keep it; it alone holds
`pushPrefixes`, rig never writes it, and it wins over `.rig/` for `forge` and
`host`. Retiring it would mean moving the kit's guard to `.rig/` first.*

**Q11. Format.** *Recommend: JSON, `config.json`, dotted keys on an
allow-list.*

**Q12. Default scope.** `rig config set` defaults to `--local`, like git;
`rig login jira` defaults to `--global`, since one Jira account usually
serves many projects. *Recommend: as stated.*

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

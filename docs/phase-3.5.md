# Phase 3.5: self-update, hardening and tracker push

Status: **0.5.2 is built** (this document's first part). **0.6.0** adds
`rig tracker push` (its part is below).

## 0.5.2: self-update

### Goals

- `rig update` replaces the running rig with the latest signed release, or one
  named with `--to`, and `--check` only reports.
- Release binaries are signed from 0.5.2, so rig can prove what it installs.
- `update.auto` lets a user choose to be told about, or to receive, new
  releases without running a command.

### Decisions

- **Ed25519 signatures over `SHA256SUMS`.** The release signs the checksum
  file once; the checksum of each binary ties it to that signature.
- **A raw 64-byte `.sig`.** `SHA256SUMS.sig` holds the signature bytes, with no
  encoding or armour.
- **Key rotation by list.** rig embeds a list of public keys and accepts a
  signature from any one of them, so a new key can ship before the old one is
  retired.
- **Verification order:** signature, then checksum, then a run of the staged
  binary's `version`, which must print the target version. Nothing replaces the
  installed file until all three pass.
- **Replacement.** The new file is staged beside the old one and renamed over
  it. On Unix a running process keeps the old inode; on Windows the old file
  moves to `<rig>.old` and is removed on a later run. A `go install`ed copy is
  replaced by the release binary built from the same tag.
- **Releases before 0.5.2 are refused**: they are unsigned. A latest release
  older than the running rig is refused unless `--to` names it.
- **`update.auto`:** `off` (default), `notify` or `install`. Global file only,
  so a repository cannot turn it on. At most one check per 24 h, run after the
  command, with a 2 s timeout, one stderr line, and never a change of exit code.
  Silent in CI, without a terminal, with `--json` or `--format json`, and
  offline. A kernel lock file beside the binary (`.<name>.update.lock`) stops
  two processes installing at once; `rig update` takes it too.

### Security and trust

- One fixed release source (the `AindriuB/rig` GitHub releases). No flag,
  environment variable or config key changes it.
- https only, to a short allow-list of GitHub hosts, checked again on every
  redirect. Downloads have size limits. Drafts and prereleases are ignored.
- The trust root is the public key list compiled into rig. A compromised
  release page cannot make rig install an unsigned or mismatched binary.
- A failed check leaves the installed file untouched.

### Out of scope

- Updating from any other source, a mirror or a private build.
- Downgrading below 0.5.2, and rolling back after an install.
- Signing anything other than release checksums.

### Tasks

1. 01 release-signing: sign `SHA256SUMS` in the release workflow.
2. 02 selfupdate-fetch-verify: find, download and verify a signed release.
3. 03 selfupdate-replace: stage and atomically replace the binary.
4. 04 rig-update-command: `rig update`.
5. 05 update-auto-mode: `update.auto` and the post-command pass.
6. 06 docs-052: these docs.

## 0.6.0: tracker push

`rig tracker push <task> [--tree] [--dry-run]` creates issues on the
configured tracker from tasks that have none, and links each task to its
issue. It extends phase 3's tracker port, workflow `tracker` key and exec
protocol ([phase-3.md](phase-3.md)); that file is not edited. The flags, exit
codes and JSON are in [design.md](design.md).

### What leaves the machine

Per task: the issue type (from `tracker.push`), title, body, acceptance
criteria, one marker label and the parent issue's key. Acceptance criteria go
only to Jira with `tracker.jira.acceptanceField` set (on GitHub the adapter
can write them into the body as a checklist, but push does not send them), and
come from the field the workflow maps to the `acceptance` attribute. Findings,
notes, counters, events and other fields never leave. `--dry-run` prints the
exact payload per task and writes nothing.

The marker label is `rig/<repo>/<slug>-<hash>`: `<repo>` is the main checkout's
directory name made label-safe (lowercase, other characters `-`, at most 24
characters), and `<hash>` the first 8 hex characters of the SHA-256 of the
task's id, so two tasks never share a label. The whole label is at most 50
characters (GitHub's limit); a long slug is cut. Labels from earlier
builds are not migrated.

### Which type a task gets

A task's own `type` field if set (it must be in `tracker.push.types`), else its
linked issue's type, else one level below its parent's (the parent's position
in `tracker.push.levels`), else the first level. rig refuses (exit 1, nothing
created) a type or depth the workflow does not allow, a type the tracker does
not list, and a child whose type is not below its parent's on a tracker that
reports levels. A tracker that lists no types is sent none, with a warning. The
parent of a task must be linked already, or in the same run (`--tree`).

### Idempotency

1. A task that is already linked is skipped.
2. Under a per-task kernel lock, before the first create, rig writes a
   `tracker-push` intent event naming the marker and the tracker.
3. A re-run that finds an intent and no link searches the tracker for the
   marker. One hit is linked, not recreated. Several hits stop the run: link
   the right one with `rig task link`. A tracker that cannot search stops the
   run rather than risk a duplicate.
4. No hit: if the earlier attempt was under two minutes ago, rig stops and says
   when to re-run, because the tracker's search index may lag. After that it
   warns and creates, and a duplicate is then possible if the tracker was
   slow. Each create attempt writes a fresh intent.
5. If another push holds the task's lock, this run stops.

### Partial failure

Tasks are created one at a time, parents first. The first failure stops the
run with exit 2; tasks already made stay linked, and the failed task and the
rest are `not-pushed` in `--json`. If the create call returns an issue and an
error (for example, GitHub made the issue but could not attach it to its
parent), the issue is linked, listed `created-with-error`, and the run stops.
If the issue is made but linking it fails, the message names it, and a re-run
finds it by the marker.

### Per adapter

- **Jira Cloud.** Types and levels come from the project's issue types. A
  child sets `fields.parent`. The body is sent as Atlassian Document Format.
- **Jira Server and Data Center.** Levels are derived (a sub-task type below,
  Epic above, others between). A sub-task takes `parent`; a child of an epic
  takes the Epic Link field (`tracker.jira.epicField`, else found by name); if
  none is found the create fails before any write.
- **GitHub.** Issue types exist only on organisations that have them; a
  repository owned by a user, or an organisation with none, is sent no type.
  A named type the organisation lacks fails before any write. A parent is
  attached as a sub-issue after the issue is created; if that step fails the
  error names the issue as not attached.
- **GitLab.** Project work items: `Issue` (level 1) and `Task` (0), with a
  parent through the hierarchy widget. Missing labels are created first. An
  `Epic` is created in the project's direct group and keyed `epic-<iid>`, and
  only without a parent; this needs a GitLab tier with epics, and without one
  the type is refused as unsupported.
- **exec.** See below.

### Exec protocol

Version 1 stays. Two new ops, each used only when the program's `caps` say so:
`create` takes `{issue: {type, title, body, acceptance, labels, parentKey?}}`
and answers `{ok, issue}`, and the issue must carry a key; `types` takes no
arguments and answers `{ok, types: [{name, level?}]}`, a non-empty list. A
program that predates either op answers `unsupported` and keeps working; push
then refuses with "this tracker cannot create issues".

### Live checks

No live check was run for GitHub or GitLab (tasks 10, 11 and 28): no throwaway
GitHub repository or GitLab project or group was available. Their create,
sub-issue, work item parent, label and epic calls are covered only by unit
tests against fakes. Jira was likewise tested against a stub. Treat the first
real push as a trial: use `--dry-run`, then a small tree.

### Security and trust

Push writes to the tracker, so it uses the same trusted `tracker.url` and
login as the other tracker commands, and `tracker.push` comes from the global
workflow file only.

### Tasks

07 tracker-port-create, 08 workflow-push-levels, 09 jira-create,
10 github-create, 11 gitlab-create, 12 exec-create-types, 13 tracker-push,
14 docs-060 (these docs). Follow-ups: 28 gitlab-epic-keys, 29 and 32 review
fixes, 34 marker-always-unique.
